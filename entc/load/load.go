// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

// Package load is the interface for loading an ent/schema package into a Go program.
package load

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"entgo.io/ent"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

type (
	// A SchemaSpec holds a serializable version of an ent.Schema
	// and its Go package and module information.
	SchemaSpec struct {
		// Schemas defines the loaded schema descriptors.
		Schemas []*Schema

		// PkgPath is the package path of the loaded
		// ent.Schema package.
		PkgPath string

		// Module defines the module information for
		// the user schema package if exists.
		Module *packages.Module
	}

	// Config holds the configuration for loading an ent/schema package.
	Config struct {
		// Path is the path for the schema package.
		Path string
		// Names are the schema names to load. Empty means all schemas in the directory.
		Names []string
		// BuildFlags are forwarded to the package.Config when
		// loading the schema package.
		BuildFlags []string
	}
)

// entInterface holds the reflect.Type of ent.Interface.
var entInterface = reflect.TypeOf(struct{ ent.Interface }{}).Field(0).Type

// Load loads the schemas package and build the Go plugin with this info.
func (c *Config) Load() (*SchemaSpec, error) {
	spec, pos, err := c.load()
	if err != nil {
		return nil, fmt.Errorf("entc/load: parse schema dir: %w", err)
	}
	if len(c.Names) == 0 {
		return nil, fmt.Errorf("entc/load: no schema found in: %s", c.Path)
	}
	// Render the temporary Go program that marshals schema descriptors.
	src, err := renderProgram(c, spec.PkgPath)
	if err != nil {
		return nil, err
	}
	// Write the rendered program to a temp directory and ensure cleanup.
	target, cleanup, err := writeTempProgram(spec.PkgPath, src)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	// Execute the program and parse its output.
	if err := runAndCollect(target, c.BuildFlags, spec, pos); err != nil {
		return nil, err
	}
	return spec, nil
}

// load parses the schema package, discovers schema types implementing
// ent.Interface, and returns a SchemaSpec plus a name→position map.
func (c *Config) load() (*SchemaSpec, map[string]string, error) {
	schemaPkg, entPkg, err := c.loadPackages()
	if err != nil {
		return nil, nil, err
	}
	names, err := discoverSchemas(schemaPkg, entPkg)
	if err != nil {
		return nil, nil, err
	}
	c.resolveNames(names)
	return &SchemaSpec{PkgPath: schemaPkg.PkgPath, Module: schemaPkg.Module}, names, nil
}

// loadPackages runs packages.Load for the schema path and ent.Interface
// package, validates the results, and returns (schemaPkg, entPkg, err).
func (c *Config) loadPackages() (*packages.Package, *packages.Package, error) {
	pkgs, err := packages.Load(&packages.Config{
		BuildFlags: c.BuildFlags,
		Mode:       packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule,
	}, c.Path, entInterface.PkgPath())
	if err != nil {
		return nil, nil, fmt.Errorf("loading package: %w", err)
	}
	if len(pkgs) < 2 {
		// Check if the package loading failed due to Go-related
		// errors, such as 'missing go.sum entry'.
		if err := golist(c.Path, c.BuildFlags); err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("missing package information for: %s", c.Path)
	}
	// Ensure correct ordering: entPkg is the ent interface package,
	// pkg is the user schema package.
	entPkg, pkg := pkgs[0], pkgs[1]
	if pkgs[0].PkgPath != entInterface.PkgPath() {
		entPkg, pkg = pkgs[1], pkgs[0]
	}
	if len(pkg.Errors) != 0 {
		return nil, nil, c.loadError(pkg.Errors[0])
	}
	if len(entPkg.Errors) != 0 {
		return nil, nil, entPkg.Errors[0]
	}
	return pkg, entPkg, nil
}

// discoverSchemas inspects the given schema package's type information
// and returns a map of schema name → "file:line" position for every
// exported type that implements ent.Interface.
func discoverSchemas(pkg, entPkg *packages.Package) (map[string]string, error) {
	names := make(map[string]string)
	iface := entPkg.Types.Scope().Lookup(entInterface.Name()).Type().Underlying().(*types.Interface)
	for k, v := range pkg.TypesInfo.Defs {
		typ, ok := v.(*types.TypeName)
		if !ok || !k.IsExported() || !types.Implements(typ.Type(), iface) {
			continue
		}
		spec, ok := k.Obj.Decl.(*ast.TypeSpec)
		if !ok {
			return nil, fmt.Errorf("invalid declaration %T for %s", k.Obj.Decl, k.Name)
		}
		if _, ok := spec.Type.(*ast.StructType); !ok {
			return nil, fmt.Errorf("invalid spec type %T for %s", spec.Type, k.Name)
		}
		p := pkg.Fset.Position(spec.Pos())
		names[k.Name] = fmt.Sprintf("%s:%d", p.Filename, p.Line)
	}
	return names, nil
}

// resolveNames populates c.Names from the discovered schemas when it is
// empty (auto-discovery), or sorts the caller-provided names.
func (c *Config) resolveNames(names map[string]string) {
	if len(c.Names) == 0 {
		c.Names = slices.Sorted(maps.Keys(names))
	} else {
		sort.Strings(c.Names)
	}
}

// renderProgram executes the build template for the given package path
// and returns gofmt-formatted source bytes of the generated program.
func renderProgram(cfg *Config, pkgPath string) ([]byte, error) {
	var b bytes.Buffer
	err := buildTmpl.ExecuteTemplate(&b, "main", struct {
		*Config
		Package string
	}{
		Config:  cfg,
		Package: pkgPath,
	})
	if err != nil {
		return nil, fmt.Errorf("entc/load: execute template: %w", err)
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("entc/load: format template: %w", err)
	}
	return src, nil
}

// writeTempProgram creates the .entc scratch directory, writes src into
// a uniquely-named .go file, and returns the file path together with a
// cleanup function that removes the directory.
func writeTempProgram(pkgPath string, src []byte) (target string, cleanup func(), err error) {
	if err := os.MkdirAll(".entc", os.ModePerm); err != nil {
		return "", nil, err
	}
	target = fmt.Sprintf(".entc/%s.go", filename(pkgPath))
	if err := os.WriteFile(target, src, 0644); err != nil {
		return "", nil, fmt.Errorf("entc/load: write file %s: %w", target, err)
	}
	return target, func() { os.RemoveAll(".entc") }, nil
}

// runAndCollect executes the generated program via 'go run', unmarshals
// each output line into a Schema descriptor, and backfills position
// information from the pos map.
func runAndCollect(target string, buildFlags []string, spec *SchemaSpec, pos map[string]string) error {
	out, err := gorun(target, buildFlags)
	if err != nil {
		return err
	}
	schemas, err := parseSchemaOutput(out)
	if err != nil {
		return err
	}
	spec.Schemas = schemas
	for _, s := range spec.Schemas {
		s.Pos = pos[s.Name]
	}
	return nil
}

// parseSchemaOutput splits the 'go run' stdout on newlines and
// unmarshals each non-empty line as a Schema descriptor.
func parseSchemaOutput(out string) ([]*Schema, error) {
	var schemas []*Schema
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		schema, err := UnmarshalSchema([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("entc/load: unmarshal schema %s: %w", line, err)
		}
		schemas = append(schemas, schema)
	}
	return schemas, nil
}

// loadError enriches a package-level error with additional diagnostic
// context. For import-cycle errors it appends the likely cause (custom
// types declared in the schema package that are used by generated code).
func (c *Config) loadError(perr packages.Error) (err error) {
	if strings.Contains(perr.Msg, "import cycle not allowed") {
		if cause := c.cycleCause(); cause != "" {
			perr.Msg += "\n" + cause
		}
	}
	err = perr
	if perr.Pos == "" {
		// Strip "-:" prefix in case of empty position.
		err = errors.New(perr.Msg)
	}
	return err
}

// cycleCause inspects the schema source directory to identify exported
// non-schema types (e.g. "type Role int") that are referenced inside
// Fields() methods. These types, when used by the generated code, create
// the import cycle. The returned string is a human-readable suggestion
// for resolving the issue, or "" if nothing actionable was found.
func (c *Config) cycleCause() (cause string) {
	dir, err := parser.ParseDir(token.NewFileSet(), c.Path, nil, 0)
	// Ignore reporting in case of parsing
	// error, or there no packages to parse.
	if err != nil || len(dir) == 0 {
		return
	}
	pkg := cycleTargetPkg(dir, c.Path)
	if pkg == nil {
		return
	}
	locals := cycleLocalTypes(pkg)
	if len(locals) == 0 {
		return
	}
	used := cycleUsedInFields(pkg, locals)
	if len(used) == 0 {
		return
	}
	names := make([]string, 0, len(used))
	for k := range used {
		names = append(names, strconv.Quote(k))
	}
	sort.Strings(names)
	return fmt.Sprintf(
		"To resolve this issue, move the custom types used by the generated code to a separate package: %s",
		strings.Join(names, ", "),
	)
}

// cycleTargetPkg selects the Go package inside dir that corresponds to
// the schema directory. Falls back to the first package when the base
// name does not match.
func cycleTargetPkg(dir map[string]*ast.Package, schemaPath string) *ast.Package {
	pkg := dir[filepath.Base(schemaPath)]
	if pkg != nil {
		return pkg
	}
	for _, v := range dir {
		return v
	}
	return nil
}

// cycleLocalTypes returns the set of exported type names declared in
// pkg that are not themselves ent.Schema or ent.Mixin types.
func cycleLocalTypes(pkg *ast.Package) map[string]bool {
	locals := make(map[string]bool)
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, s := range g.Specs {
				ts, ok := s.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				// Non-struct types such as "type Role int".
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					locals[ts.Name.Name] = true
					continue
				}
				if !embedsSchemaOrMixin(st) {
					locals[ts.Name.Name] = true
				}
			}
		}
	}
	return locals
}

// embedsSchemaOrMixin reports whether st embeds a type named Schema or
// Mixin (either as a selector expression like "ent.Schema" or a local
// identifier "schema"/"mixin").
func embedsSchemaOrMixin(st *ast.StructType) bool {
	var found bool
	astutil.Apply(st.Fields, func(c *astutil.Cursor) bool {
		f, ok := c.Node().(*ast.Field)
		if !ok {
			return true
		}
		switch x := f.Type.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "Schema" || x.Sel.Name == "Mixin" {
				found = true
			}
		case *ast.Ident:
			// A common pattern is to create local base schema to be embedded by other schemas.
			if name := strings.ToLower(x.Name); name == "schema" || name == "mixin" {
				found = true
			}
		}
		// Stop traversing the AST in case an ~ent.Schema is embedded.
		return !found
	}, nil)
	return found
}

// cycleUsedInFields returns the subset of locals that are referenced
// inside any Fields() method body declared in pkg.
func cycleUsedInFields(pkg *ast.Package, locals map[string]bool) map[string]bool {
	used := make(map[string]bool)
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Fields" || fn.Type.Params.NumFields() != 0 || fn.Type.Results.NumFields() != 1 {
				continue
			}
			astutil.Apply(fn.Body, func(cursor *astutil.Cursor) bool {
				i, ok := cursor.Node().(*ast.Ident)
				if ok && locals[i.Name] {
					used[i.Name] = true
				}
				return true
			}, nil)
		}
	}
	return used
}

var (
	//go:embed template/main.tmpl schema.go
	files     embed.FS
	buildTmpl = templates()
)

func templates() *template.Template {
	tmpls, err := schemaTemplates()
	if err != nil {
		panic(err)
	}
	tmpl := template.Must(template.New("templates").
		ParseFS(files, "template/main.tmpl"))
	for _, t := range tmpls {
		tmpl = template.Must(tmpl.Parse(t))
	}
	return tmpl
}

// schemaTemplates turns the schema.go file and its import block into templates.
// It extracts import declarations and remaining top-level declarations from
// the embedded schema.go and returns them as separate template definitions.
func schemaTemplates() ([]string, error) {
	var (
		imports []string
		code    bytes.Buffer
		fset    = token.NewFileSet()
		src, _  = files.ReadFile("schema.go")
	)
	f, err := parser.ParseFile(fset, "schema.go", src, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse schema file: %w", err)
	}
	for _, decl := range f.Decls {
		if decl, ok := decl.(*ast.GenDecl); ok && decl.Tok == token.IMPORT {
			for _, spec := range decl.Specs {
				imports = append(imports, spec.(*ast.ImportSpec).Path.Value)
			}
			continue
		}
		if err := format.Node(&code, fset, decl); err != nil {
			return nil, fmt.Errorf("format node: %w", err)
		}
		code.WriteByte('\n')
	}
	return []string{
		fmt.Sprintf(`{{ define "schema" }} %s {{ end }}`, code.String()),
		fmt.Sprintf(`{{ define "imports" }} %s {{ end }}`, strings.Join(imports, "\n")),
	}, nil
}

func filename(pkg string) string {
	name := strings.ReplaceAll(pkg, "/", "_")
	return fmt.Sprintf("entc_%s_%d", name, time.Now().Unix())
}

// gorun runs 'go run' on target and returns its stdout.
func gorun(target string, buildFlags []string) (string, error) {
	s, err := gocmd("run", target, buildFlags)
	if err != nil {
		return "", fmt.Errorf("entc/load: %s", err)
	}
	return s, nil
}

// golist checks if 'go list' can be executed on the given target.
func golist(target string, buildFlags []string) error {
	_, err := gocmd("list", target, buildFlags)
	return err
}

// gocmd runs a go subcommand and returns its stdout. On failure the
// trimmed stderr is returned as the error, making the underlying go
// toolchain message directly visible to the caller.
func gocmd(command, target string, buildFlags []string) (string, error) {
	args := []string{command}
	args = append(args, buildFlags...)
	args = append(args, target)
	cmd := exec.Command("go", args...)
	stderr := bytes.NewBuffer(nil)
	stdout := bytes.NewBuffer(nil)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return "", errors.New(strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
