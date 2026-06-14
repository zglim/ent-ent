// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package load

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/schema/field"

	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	cfg := &Config{Path: "./testdata/valid"}
	spec, err := cfg.Load()
	require.NoError(t, err)
	require.Len(t, spec.Schemas, 3)
	require.Equal(t, "entgo.io/ent/entc/load/testdata/valid", spec.PkgPath)

	require.Equal(t, "Group", spec.Schemas[0].Name, "ordered alphabetically")
	require.Equal(t, "Tag", spec.Schemas[1].Name)
	require.Equal(t, "User", spec.Schemas[2].Name)
}

func TestLoadWrongPath(t *testing.T) {
	cfg := &Config{Path: "./boring"}
	plg, err := cfg.Load()
	require.Error(t, err)
	require.Nil(t, plg)
}

func TestLoadSpecific(t *testing.T) {
	cfg := &Config{Path: "./testdata/valid", Names: []string{"User"}}
	spec, err := cfg.Load()
	require.NoError(t, err)
	require.Len(t, spec.Schemas, 1)
	require.Equal(t, "User", spec.Schemas[0].Name)
	require.Equal(t, "entgo.io/ent/entc/load/testdata/valid", spec.PkgPath)
}

func TestLoadNoSchema(t *testing.T) {
	cfg := &Config{Path: "./testdata/invalid"}
	schemas, err := cfg.Load()
	require.Error(t, err)
	require.Empty(t, schemas)
}

func TestLoadSchemaFailure(t *testing.T) {
	cfg := &Config{Path: "./testdata/failure"}
	spec, err := cfg.Load()
	require.Error(t, err)
	require.Nil(t, spec)
}

func TestLoadBaseSchema(t *testing.T) {
	cfg := &Config{Path: "./testdata/base"}
	spec, err := cfg.Load()
	require.NoError(t, err)
	require.Len(t, spec.Schemas, 1)
	require.Len(t, spec.Schemas[0].Fields, 2, "embedded base schema")
	f1 := spec.Schemas[0].Fields[0]
	require.Equal(t, "base_field", f1.Name)
	require.Equal(t, field.TypeInt, f1.Info.Type)
	f2 := spec.Schemas[0].Fields[1]
	require.Equal(t, "user_field", f2.Name)
	require.Equal(t, field.TypeString, f2.Info.Type)
}

func TestLoadTags(t *testing.T) {
	all, err := (&Config{
		Path: "./testdata/buildflags",
	}).Load()
	require.NoError(t, err)

	require.Len(t, all.Schemas, 2)
	require.Equal(t, "Group", all.Schemas[0].Name, "ordered alphabetically")
	require.Equal(t, "User", all.Schemas[1].Name)

	notags, err := (&Config{
		Path:       "./testdata/buildflags",
		BuildFlags: []string{"-tags", "hidegroups"},
	}).Load()
	require.NoError(t, err)

	require.Len(t, notags.Schemas, 1)
	require.Equal(t, "User", notags.Schemas[0].Name)

	require.Equal(t, all.Schemas[1], notags.Schemas[0])
}

func TestLoadCycleError(t *testing.T) {
	cfg := &Config{Path: "./testdata/cycle"}
	spec, err := cfg.Load()
	require.Nil(t, spec)
	require.EqualError(t, err, `entc/load: parse schema dir: import cycle not allowed: import stack: [entgo.io/ent/entc/load/testdata/cycle entgo.io/ent/entc/load/testdata/cycle/fakent entgo.io/ent/entc/load/testdata/cycle]
To resolve this issue, move the custom types used by the generated code to a separate package: "Enum", "Used"`)
}

// ---------------------------------------------------------------------------
// Tests for extracted helper functions
// ---------------------------------------------------------------------------

func TestLoadPackages_Valid(t *testing.T) {
	cfg := &Config{Path: "./testdata/valid"}
	schemaPkg, entPkg, err := cfg.loadPackages()
	require.NoError(t, err)
	require.NotNil(t, schemaPkg)
	require.NotNil(t, entPkg)
	require.Equal(t, "entgo.io/ent/entc/load/testdata/valid", schemaPkg.PkgPath)
	require.Equal(t, entInterface.PkgPath(), entPkg.PkgPath)
}

func TestLoadPackages_WrongPath(t *testing.T) {
	cfg := &Config{Path: "./does/not/exist"}
	_, _, err := cfg.loadPackages()
	require.Error(t, err)
}

func TestDiscoverSchemas_Valid(t *testing.T) {
	cfg := &Config{Path: "./testdata/valid"}
	schemaPkg, entPkg, err := cfg.loadPackages()
	require.NoError(t, err)

	names, err := discoverSchemas(schemaPkg, entPkg)
	require.NoError(t, err)
	require.Contains(t, names, "User")
	require.Contains(t, names, "Group")
	require.Contains(t, names, "Tag")
	// Position strings should be "file:line" format.
	for _, pos := range names {
		require.Contains(t, pos, ".go:")
	}
}

func TestResolveNames_AutoDiscovery(t *testing.T) {
	cfg := &Config{}
	names := map[string]string{
		"Zebra": "z.go:1",
		"Alpha": "a.go:1",
		"Middle": "m.go:1",
	}
	cfg.resolveNames(names)
	require.Equal(t, []string{"Alpha", "Middle", "Zebra"}, cfg.Names)
}

func TestResolveNames_ExplicitNames(t *testing.T) {
	cfg := &Config{Names: []string{"User", "Group"}}
	names := map[string]string{
		"User":  "u.go:1",
		"Group": "g.go:1",
		"Tag":   "t.go:1",
	}
	cfg.resolveNames(names)
	// Caller-provided names are sorted in place.
	require.Equal(t, []string{"Group", "User"}, cfg.Names)
}

func TestRenderProgram(t *testing.T) {
	cfg := &Config{
		Path:  "./testdata/valid",
		Names: []string{"User"},
	}
	src, err := renderProgram(cfg, "entgo.io/ent/entc/load/testdata/valid")
	require.NoError(t, err)
	require.NotEmpty(t, src)
	// Rendered source should contain the package import and the schema name.
	s := string(src)
	require.Contains(t, s, "entgo.io/ent/entc/load/testdata/valid")
	require.Contains(t, s, "User")
}

func TestWriteTempProgram_Cleanup(t *testing.T) {
	src := []byte("package main\nfunc main() {}\n")
	target, cleanup, err := writeTempProgram("example.com/test", src)
	require.NoError(t, err)
	require.FileExists(t, target)
	require.Contains(t, target, ".entc/")

	// After cleanup, the .entc directory should be gone.
	cleanup()
	_, err = os.Stat(".entc")
	require.True(t, os.IsNotExist(err), "expected .entc directory to be removed after cleanup")
}

func TestParseSchemaOutput_Empty(t *testing.T) {
	schemas, err := parseSchemaOutput("")
	require.NoError(t, err)
	require.Empty(t, schemas)
}

func TestParseSchemaOutput_InvalidLine(t *testing.T) {
	_, err := parseSchemaOutput("not valid json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unmarshal schema")
}

func TestCycleTargetPkg_MatchByBaseName(t *testing.T) {
	dir := map[string]*ast.Package{
		"schema": {Name: "schema"},
		"other":  {Name: "other"},
	}
	// When schemaPath's base matches a key in dir, that package is returned.
	pkg := cycleTargetPkg(dir, "/some/path/schema")
	require.NotNil(t, pkg)
	require.Equal(t, "schema", pkg.Name)
}

func TestCycleTargetPkg_Fallback(t *testing.T) {
	dir := map[string]*ast.Package{
		"only": {Name: "only"},
	}
	// When no key matches, the first (and only) package is returned.
	pkg := cycleTargetPkg(dir, "/some/path/doesnotmatch")
	require.NotNil(t, pkg)
	require.Equal(t, "only", pkg.Name)
}

func TestCycleTargetPkg_Empty(t *testing.T) {
	pkg := cycleTargetPkg(nil, "/some/path")
	require.Nil(t, pkg)
}

func TestCycleLocalTypes(t *testing.T) {
	src := `package test

import "entgo.io/ent"

type User struct {
	ent.Schema
}

type Role int

type Status int

type notExported struct{}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	require.NoError(t, err)
	pkg := &ast.Package{
		Files: map[string]*ast.File{"test.go": f},
	}
	locals := cycleLocalTypes(pkg)
	// Role and Status are non-schema exported types.
	require.True(t, locals["Role"])
	require.True(t, locals["Status"])
	// User embeds ent.Schema, so it is not a "local" type.
	require.False(t, locals["User"])
	// notExported is unexported.
	require.False(t, locals["notExported"])
}

func TestCycleLocalTypes_WithMixin(t *testing.T) {
	src := `package test

import "entgo.io/ent"

type Base struct {
	ent.Mixin
}

type Helper int
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	require.NoError(t, err)
	pkg := &ast.Package{
		Files: map[string]*ast.File{"test.go": f},
	}
	locals := cycleLocalTypes(pkg)
	require.False(t, locals["Base"], "Mixin-embedding type should not be local")
	require.True(t, locals["Helper"])
}

func TestEmbedsSchemaOrMixin(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "ent.Schema",
			src:  `package t; import "entgo.io/ent"; type T struct{ ent.Schema }`,
			want: true,
		},
		{
			name: "ent.Mixin",
			src:  `package t; import "entgo.io/ent"; type T struct{ ent.Mixin }`,
			want: true,
		},
		{
			name: "local schema ident",
			src:  `package t; type schema struct{}; type T struct{ schema }`,
			want: true,
		},
		{
			name: "no embed",
			src:  `package t; type T struct{ Name string }`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "t.go", tt.src, 0)
			require.NoError(t, err)
			// Get the last type spec which is T.
			var st *ast.StructType
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name != "T" {
						continue
					}
					st, _ = ts.Type.(*ast.StructType)
				}
			}
			require.NotNil(t, st)
			require.Equal(t, tt.want, embedsSchemaOrMixin(st))
		})
	}
}

func TestCycleUsedInFields(t *testing.T) {
	src := `package test

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("r").GoType(Role(0)),
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	require.NoError(t, err)
	pkg := &ast.Package{
		Files: map[string]*ast.File{"test.go": f},
	}
	locals := map[string]bool{"Role": true, "Unused": true}
	used := cycleUsedInFields(pkg, locals)
	require.True(t, used["Role"])
	require.False(t, used["Unused"])
}

func TestCycleCause_Integration(t *testing.T) {
	cfg := &Config{Path: "./testdata/cycle"}
	cause := cfg.cycleCause()
	require.Contains(t, cause, `"Enum"`)
	require.Contains(t, cause, `"Used"`)
	require.NotContains(t, cause, `"NotUsed"`)
}

func TestFilename(t *testing.T) {
	name := filename("entgo.io/ent/schema")
	require.True(t, strings.HasPrefix(name, "entc_entgo.io_ent_schema_"))
}

func TestSchemaTemplates(t *testing.T) {
	tmpls, err := schemaTemplates()
	require.NoError(t, err)
	require.Len(t, tmpls, 2)
	require.Contains(t, tmpls[0], `define "schema"`)
	require.Contains(t, tmpls[1], `define "imports"`)
}

func TestLoadPositionBackfill(t *testing.T) {
	cfg := &Config{Path: "./testdata/valid", Names: []string{"User"}}
	spec, err := cfg.Load()
	require.NoError(t, err)
	require.Len(t, spec.Schemas, 1)
	// Position should be filled with "file:line".
	require.NotEmpty(t, spec.Schemas[0].Pos)
	absPath, _ := filepath.Abs("./testdata/valid")
	require.Contains(t, spec.Schemas[0].Pos, absPath)
}
