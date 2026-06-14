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

// Load loads the schemas package and build the Go plugin with this info.
//
// The flow is split into focused stages: load resolves the schema package and
// discovers its schemas, generate renders the loader program, run executes it,
// and parseSchemas decodes the result before the source positions are restored.
func (c *Config) Load() (*SchemaSpec, error) {
	spec, pos, err := c.load()
	if err != nil {
		return nil, fmt.Errorf("entc/load: parse schema dir: %w", err)
	}
	if len(c.Names) == 0 {
		return nil, fmt.Errorf("entc/load: no schema found in: %s", c.Path)
	}
	src, err := c.generate(spec.PkgPath)
	if err != nil {
		return nil, err
	}
	out, err := c.run(src, spec.PkgPath)
	if err != nil {
		return nil, err
	}
	spec.Schemas, err = parseSchemas(out)
	if err != nil {
		return nil, err
	}
	for _, s := range spec.Schemas {
		s.Pos = pos[s.Name]
	}
	return spec, nil
}

// generate renders the temporary loader program for the given schema package
// and returns its gofmt-ed Go source. It is independent of the filesystem and
// of 'go run', which makes the template output easy to test in isolation.
func (c *Config) generate(pkgPath string) ([]byte, error) {
	var b bytes.Buffer
	err := buildTmpl.ExecuteTemplate(&b, "main", struct {
		*Config
		Package string
	}{
		Config:  c,
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

// run writes the generated program into a temporary ".entc" directory, executes
// it with 'go run', and returns its raw output. The temporary directory is
// always removed before returning.
func (c *Config) run(src []byte, pkgPath string) (string, error) {
	if err := os.MkdirAll(".entc", os.ModePerm); err != nil {
		return "", err
	}
	defer os.RemoveAll(".entc")
	target := fmt.Sprintf(".entc/%s.go", filename(pkgPath))
	if err := os.WriteFile(target, src, 0644); err != nil {
		return "", fmt.Errorf("entc/load: write file %s: %w", target, err)
	}
	return gorun(target, c.BuildFlags)
}

// parseSchemas decodes the newline-separated schema descriptors printed by the
// loader program into Schema objects.
func parseSchemas(out string) ([]*Schema, error) {
	var schemas []*Schema
	for _, line := range strings.Split(out, "\n") {
		schema, err := UnmarshalSchema([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("entc/load: unmarshal schema %s: %w", line, err)
		}
		schemas = append(schemas, schema)
	}
	return schemas, nil
}

// entInterface holds the reflect.Type of ent.Interface.
var entInterface = reflect.TypeOf(struct{ ent.Interface }{}).Field(0).Type

// load resolves the schema package and discovers the schema types it declares.
// It returns the spec together with a name->"file:line" map for the schemas.
func (c *Config) load() (*SchemaSpec, map[string]string, error) {
	schemaPkg, entPkg, err := c.loadPackages()
	if err != nil {
		return nil, nil, err
	}
	names, err := c.schemaNames(schemaPkg, entPkg)
	if err != nil {
		return nil, nil, err
	}
	return &SchemaSpec{PkgPath: schemaPkg.PkgPath, Module: schemaPkg.Module}, names, nil
}

// loadPackages loads the user schema package along with the ent package that
// defines ent.Interface, surfaces any package-level errors, and reports which
// of the two loaded packages is which.
func (c *Config) loadPackages() (schemaPkg, entPkg *packages.Package, err error) {
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
	// Report errors against the requested order (schema first, ent second)
	// before resolving identities, so the schema diagnostics (e.g. import
	// cycles) keep flowing through loadError.
	entPkg, schemaPkg = pkgs[0], pkgs[1]
	if len(schemaPkg.Errors) != 0 {
		return nil, nil, c.loadError(schemaPkg.Errors[0])
	}
	if len(entPkg.Errors) != 0 {
		return nil, nil, entPkg.Errors[0]
	}
	if pkgs[0].PkgPath != entInterface.PkgPath() {
		entPkg, schemaPkg = pkgs[1], pkgs[0]
	}
	return schemaPkg, entPkg, nil
}

// schemaNames discovers the exported types declared in schemaPkg that implement
// ent.Interface, returning a name->"file:line" map. It also resolves c.Names:
// when empty it is filled with all discovered schemas (sorted), otherwise the
// caller-provided subset is sorted in place.
func (c *Config) schemaNames(schemaPkg, entPkg *packages.Package) (map[string]string, error) {
	names := make(map[string]string)
	iface := entPkg.Types.Scope().Lookup(entInterface.Name()).Type().Underlying().(*types.Interface)
	for k, v := range schemaPkg.TypesInfo.Defs {
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
		p := schemaPkg.Fset.Position(spec.Pos())
		names[k.Name] = fmt.Sprintf("%s:%d", p.Filename, p.Line)
	}
	if len(c.Names) == 0 {
		c.Names = slices.Sorted(maps.Keys(names))
	} else {
		sort.Strings(c.Names)
	}
	return names, nil
}

// loadError converts a package error into the error returned to the caller.
// Import-cycle errors are enriched with a hint pointing at the custom types
// that likely cause the cycle, and the empty "-:" position prefix is stripped.
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

// cycleCause inspects the schema package source and, when possible, reports the
// custom (non-schema) types referenced by schema fields that are the likely
// cause of an import cycle.
func (c *Config) cycleCause() (cause string) {
	dir, err := parser.ParseDir(token.NewFileSet(), c.Path, nil, 0)
	// Ignore reporting in case of parsing
	// error, or there no packages to parse.
	if err != nil || len(dir) == 0 {
		return
	}
	pkg := schemaASTPackage(dir, c.Path)
	// Package local declarations used by schema fields.
	locals := localSchemaTypes(pkg)
	if len(locals) == 0 {
		return
	}
	// Usage of local declarations by schema fields.
	names := usedLocalTypes(pkg, locals)
	if len(names) > 0 {
		cause = fmt.Sprintf("To resolve this issue, move the custom types used by the generated code to a separate package: %s", strings.Join(names, ", "))
	}
	return
}

// schemaASTPackage returns the package that likely holds the schema from a
// parsed directory: the one matching the directory name, otherwise the first
// (and, in practice, only) parsed package.
func schemaASTPackage(dir map[string]*ast.Package, path string) *ast.Package {
	if pkg := dir[filepath.Base(path)]; pkg != nil {
		return pkg
	}
	for _, pkg := range dir {
		return pkg
	}
	return nil
}

// localSchemaTypes collects the names of exported, package-local types that are
// not themselves ent schemas or mixins. These are the custom types that may be
// referenced by schema fields.
func localSchemaTypes(pkg *ast.Package) map[string]bool {
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
				if !embedsSchema(st) {
					locals[ts.Name.Name] = true
				}
			}
		}
	}
	return locals
}

// embedsSchema reports whether the struct embeds an ent.Schema or Mixin, either
// directly or through a local base schema (a common pattern is a local base
// schema embedded by other schemas).
func embedsSchema(st *ast.StructType) bool {
	var embed bool
	astutil.Apply(st.Fields, func(c *astutil.Cursor) bool {
		f, ok := c.Node().(*ast.Field)
		if ok {
			switch x := f.Type.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "Schema" || x.Sel.Name == "Mixin" {
					embed = true
				}
			case *ast.Ident:
				if name := strings.ToLower(x.Name); name == "schema" || name == "mixin" {
					embed = true
				}
			}
		}
		// Stop traversing the AST in case an ~ent.Schema is embedded.
		return !embed
	}, nil)
	return embed
}

// usedLocalTypes returns the sorted, quoted names of the given local types that
// are referenced in the bodies of schema "Fields" methods.
func usedLocalTypes(pkg *ast.Package, locals map[string]bool) []string {
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
	names := make([]string, 0, len(used))
	for k := range used {
		names = append(names, strconv.Quote(k))
	}
	sort.Strings(names)
	return names
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

// gorun runs the 'go run' command on the given target and returns its output.
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

// gocmd runs a go command on the given target and returns its standard output.
// On failure, the trimmed standard error is returned as the error.
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
