// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"fmt"
	"reflect"
	"testing"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"

	"github.com/stretchr/testify/require"
)

func TestAtImplicitIndexSuffix(t *testing.T) {
	tests := []struct {
		name      string
		idxName   string
		prefix    string
		minSuffix int64
		want      bool
	}{
		// PostgreSQL-style: "<table>_<col>_key" with suffix >= 1
		// Note: exact match (no suffix) is NOT matched by the helper;
		// it's handled by the caller (e.g. Postgres.atImplicitIndexName).
		{name: "pg exact not matched", idxName: "users_name_key", prefix: "users_name_key", minSuffix: 1, want: false},
		{name: "pg suffix 1", idxName: "users_name_key1", prefix: "users_name_key", minSuffix: 1, want: true},
		{name: "pg suffix 2", idxName: "users_name_key2", prefix: "users_name_key", minSuffix: 1, want: true},
		{name: "pg suffix 10", idxName: "users_name_key10", prefix: "users_name_key", minSuffix: 1, want: true},
		{name: "pg suffix 0 rejected", idxName: "users_name_key0", prefix: "users_name_key", minSuffix: 1, want: false},
		{name: "pg no match", idxName: "other_index", prefix: "users_name_key", minSuffix: 1, want: false},
		{name: "pg partial prefix", idxName: "users_name", prefix: "users_name_key", minSuffix: 1, want: false},

		// MySQL-style: "<col>_" with suffix >= 2
		{name: "mysql suffix 2", idxName: "name_2", prefix: "name_", minSuffix: 2, want: true},
		{name: "mysql suffix 3", idxName: "name_3", prefix: "name_", minSuffix: 2, want: true},
		{name: "mysql suffix 1 rejected", idxName: "name_1", prefix: "name_", minSuffix: 2, want: false},
		{name: "mysql suffix 0 rejected", idxName: "name_0", prefix: "name_", minSuffix: 2, want: false},
		{name: "mysql exact prefix rejected", idxName: "name_", prefix: "name_", minSuffix: 2, want: false},
		{name: "mysql no match", idxName: "other", prefix: "name_", minSuffix: 2, want: false},

		// SQLite-style: "sqlite_autoindex_<table>_" with suffix >= 1
		{name: "sqlite suffix 1", idxName: "sqlite_autoindex_users_1", prefix: "sqlite_autoindex_users_", minSuffix: 1, want: true},
		{name: "sqlite suffix 5", idxName: "sqlite_autoindex_users_5", prefix: "sqlite_autoindex_users_", minSuffix: 1, want: true},
		{name: "sqlite suffix 0 rejected", idxName: "sqlite_autoindex_users_0", prefix: "sqlite_autoindex_users_", minSuffix: 1, want: false},
		{name: "sqlite no match", idxName: "users_1", prefix: "sqlite_autoindex_users_", minSuffix: 1, want: false},

		// Edge cases
		{name: "empty idx name", idxName: "", prefix: "name_", minSuffix: 1, want: false},
		{name: "empty prefix empty idx", idxName: "", prefix: "", minSuffix: 1, want: false}, // no suffix number
		{name: "non-numeric suffix", idxName: "name_abc", prefix: "name_", minSuffix: 1, want: false},
		{name: "negative suffix", idxName: "name_-1", prefix: "name_", minSuffix: 1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := atImplicitIndexSuffix(tt.idxName, tt.prefix, tt.minSuffix)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMySQL_AtImplicitIndexName(t *testing.T) {
	d := &MySQL{}
	col := &Column{Name: "name"}
	tests := []struct {
		idxName string
		want    bool
	}{
		{"name", true},      // exact match
		{"name_2", true},    // suffix >= 2
		{"name_3", true},    // suffix >= 2
		{"name_10", true},   // suffix >= 2
		{"name_1", false},   // suffix < 2
		{"name_0", false},   // suffix < 2
		{"name_", false},    // no suffix number
		{"other", false},    // no match
		{"name_key", false}, // not a valid suffix
	}
	for _, tt := range tests {
		t.Run(tt.idxName, func(t *testing.T) {
			got := d.atImplicitIndexName(&Index{Name: tt.idxName}, col)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPostgres_AtImplicitIndexName(t *testing.T) {
	d := &Postgres{}
	tbl := &Table{Name: "users"}
	col := &Column{Name: "name"}
	tests := []struct {
		idxName string
		want    bool
	}{
		{"users_name_key", true},   // exact match
		{"users_name_key1", true},  // suffix >= 1
		{"users_name_key2", true},  // suffix >= 1
		{"users_name_key10", true}, // suffix >= 1
		{"users_name_key0", false}, // suffix < 1
		{"users_name", false},      // no match
		{"other_key", false},       // no match
	}
	for _, tt := range tests {
		t.Run(tt.idxName, func(t *testing.T) {
			got := d.atImplicitIndexName(&Index{Name: tt.idxName}, tbl, col)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSQLite_AtImplicitIndexName(t *testing.T) {
	d := &SQLite{}
	tbl := &Table{Name: "users"}
	col := &Column{Name: "name"}
	tests := []struct {
		idxName string
		want    bool
	}{
		{"name", true},                       // exact match on column name
		{"sqlite_autoindex_users_1", true},   // autoindex suffix >= 1
		{"sqlite_autoindex_users_5", true},   // autoindex suffix >= 1
		{"sqlite_autoindex_users_0", false},  // autoindex suffix < 1
		{"sqlite_autoindex_other_1", false},  // wrong table
		{"users_name_key", false},            // not a match
	}
	for _, tt := range tests {
		t.Run(tt.idxName, func(t *testing.T) {
			got := d.atImplicitIndexName(&Index{Name: tt.idxName}, tbl, col)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAtUniqueColumn(t *testing.T) {
	t.Run("no existing index", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		t1 := &Table{Name: "users", Columns: []*Column{c1}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		atUniqueColumn(t1, c1, t2, c2, "users_email_key", func(*Index, *Table, *Column) bool {
			return false
		})
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_email_key", t2.Indexes[0].Name)
		require.True(t, t2.Indexes[0].Unique)
	})

	t.Run("existing implicit index", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		idx := &Index{Name: "email", Unique: true, Columns: []*Column{c1}}
		t1 := &Table{Name: "users", Columns: []*Column{c1}, Indexes: []*Index{idx}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		atUniqueColumn(t1, c1, t2, c2, "users_email_key", func(i *Index, _ *Table, c *Column) bool {
			return i.Name == c.Name
		})
		// Should not add another index since the implicit one exists.
		require.Empty(t, t2.Indexes)
	})

	t.Run("existing non-matching index", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		idx := &Index{Name: "custom_idx", Unique: true, Columns: []*Column{c1}}
		t1 := &Table{Name: "users", Columns: []*Column{c1}, Indexes: []*Index{idx}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		atUniqueColumn(t1, c1, t2, c2, "users_email_key", func(*Index, *Table, *Column) bool {
			return false // no implicit match
		})
		// Should add the unique index since no implicit match found.
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_email_key", t2.Indexes[0].Name)
	})
}

func TestAtIncrementColumn(t *testing.T) {
	t.Run("with default removes table attr", func(t *testing.T) {
		tbl := &schema.Table{
			Attrs: []schema.Attr{&mysql.AutoIncrement{}},
		}
		col := &schema.Column{}
		atIncrementColumn(tbl, col, true, reflect.TypeOf(&mysql.AutoIncrement{}), &mysql.AutoIncrement{})
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})

	t.Run("without default adds column attr", func(t *testing.T) {
		tbl := &schema.Table{
			Attrs: []schema.Attr{&mysql.AutoIncrement{}},
		}
		col := &schema.Column{}
		atIncrementColumn(tbl, col, false, reflect.TypeOf(&mysql.AutoIncrement{}), &mysql.AutoIncrement{})
		require.Len(t, tbl.Attrs, 1) // table attr untouched
		require.Len(t, col.Attrs, 1) // column attr added
		require.IsType(t, &mysql.AutoIncrement{}, col.Attrs[0])
	})

	t.Run("sqlite autoincrement", func(t *testing.T) {
		tbl := &schema.Table{
			Attrs: []schema.Attr{&sqlite.AutoIncrement{Seq: 100}},
		}
		col := &schema.Column{}
		atIncrementColumn(tbl, col, true, reflect.TypeOf(&sqlite.AutoIncrement{}), &sqlite.AutoIncrement{})
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})

	t.Run("postgres identity", func(t *testing.T) {
		tbl := &schema.Table{
			Attrs: []schema.Attr{&postgres.Identity{}},
		}
		col := &schema.Column{}
		atIncrementColumn(tbl, col, true, reflect.TypeOf(&postgres.Identity{}), &postgres.Identity{})
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})
}

func TestAtIndexParts(t *testing.T) {
	c1 := &Column{Name: "name", Type: field.TypeString}
	c2 := &Column{Name: "age", Type: field.TypeInt}

	t.Run("basic parts without attrs", func(t *testing.T) {
		idx1 := &Index{Name: "idx", Columns: []*Column{c1, c2}}
		t2 := schema.NewTable("users").
			AddColumns(schema.NewStringColumn("name", "varchar"), schema.NewIntColumn("age", "int"))
		idx2 := schema.NewIndex("idx")

		err := atIndexParts(idx1, t2, idx2, nil)
		require.NoError(t, err)
		require.Len(t, idx2.Parts, 2)
		require.Equal(t, "name", idx2.Parts[0].C.Name)
		require.Equal(t, "age", idx2.Parts[1].C.Name)
	})

	t.Run("parts with attrs callback", func(t *testing.T) {
		idx1 := &Index{Name: "idx", Columns: []*Column{c1}}
		t2 := schema.NewTable("users").
			AddColumns(schema.NewStringColumn("name", "varchar"))
		idx2 := schema.NewIndex("idx")

		err := atIndexParts(idx1, t2, idx2, func(c1 *Column, _ *schema.Column) ([]schema.Attr, error) {
			if c1.Name == "name" {
				return []schema.Attr{&mysql.SubPart{Len: 100}}, nil
			}
			return nil, nil
		})
		require.NoError(t, err)
		require.Len(t, idx2.Parts, 1)
		require.Len(t, idx2.Parts[0].Attrs, 1)
		require.IsType(t, &mysql.SubPart{}, idx2.Parts[0].Attrs[0])
	})

	t.Run("missing column error", func(t *testing.T) {
		idx1 := &Index{Name: "idx", Columns: []*Column{{Name: "missing"}}}
		t2 := schema.NewTable("users")
		idx2 := schema.NewIndex("idx")

		err := atIndexParts(idx1, t2, idx2, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing")
	})

	t.Run("callback error propagated", func(t *testing.T) {
		idx1 := &Index{Name: "idx", Columns: []*Column{c1}}
		t2 := schema.NewTable("users").
			AddColumns(schema.NewStringColumn("name", "varchar"))
		idx2 := schema.NewIndex("idx")

		expected := fmt.Errorf("callback error")
		err := atIndexParts(idx1, t2, idx2, func(*Column, *schema.Column) ([]schema.Attr, error) {
			return nil, expected
		})
		require.ErrorIs(t, err, expected)
	})
}

// TestDialectUniqueIndexNaming ensures each dialect generates the correct
// implicit unique index name.
func TestDialectUniqueIndexNaming(t *testing.T) {
	t.Run("MySQL uses column name", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		t1 := &Table{Name: "users", Columns: []*Column{c1}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		(&MySQL{}).atUniqueC(t1, c1, t2, c2)
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "email", t2.Indexes[0].Name)
	})

	t.Run("Postgres uses table_column_key", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		t1 := &Table{Name: "users", Columns: []*Column{c1}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		(&Postgres{}).atUniqueC(t1, c1, t2, c2)
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_email_key", t2.Indexes[0].Name)
	})

	t.Run("SQLite uses table_column_key", func(t *testing.T) {
		c1 := &Column{Name: "email", Type: field.TypeString, Unique: true}
		t1 := &Table{Name: "users", Columns: []*Column{c1}}
		c2 := schema.NewStringColumn("email", "varchar")
		t2 := schema.NewTable("users").AddColumns(c2)

		(&SQLite{}).atUniqueC(t1, c1, t2, c2)
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_email_key", t2.Indexes[0].Name)
	})
}

// TestDialectIncrementColumn ensures increment column handling works
// correctly for each dialect after the shared helper refactoring.
func TestDialectIncrementColumn(t *testing.T) {
	t.Run("MySQL without default adds autoincrement", func(t *testing.T) {
		tbl := schema.NewTable("users")
		col := schema.NewIntColumn("id", "bigint")
		(&MySQL{}).atIncrementC(tbl, col)
		require.Len(t, col.Attrs, 1)
		require.IsType(t, &mysql.AutoIncrement{}, col.Attrs[0])
	})

	t.Run("MySQL with default removes autoincrement from table", func(t *testing.T) {
		tbl := schema.NewTable("users").AddAttrs(&mysql.AutoIncrement{})
		col := schema.NewIntColumn("id", "bigint").SetDefault(&schema.Literal{V: "1"})
		(&MySQL{}).atIncrementC(tbl, col)
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})

	t.Run("SQLite without default adds autoincrement", func(t *testing.T) {
		tbl := schema.NewTable("users")
		col := schema.NewIntColumn("id", "integer")
		(&SQLite{}).atIncrementC(tbl, col)
		require.Len(t, col.Attrs, 1)
		require.IsType(t, &sqlite.AutoIncrement{}, col.Attrs[0])
	})

	t.Run("SQLite with default removes autoincrement from table", func(t *testing.T) {
		tbl := schema.NewTable("users").AddAttrs(&sqlite.AutoIncrement{Seq: 100})
		col := schema.NewIntColumn("id", "integer").SetDefault(&schema.Literal{V: "1"})
		(&SQLite{}).atIncrementC(tbl, col)
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})

	t.Run("Postgres without default adds identity", func(t *testing.T) {
		tbl := schema.NewTable("users")
		col := schema.NewIntColumn("id", "bigint")
		(&Postgres{}).atIncrementC(tbl, col)
		require.Len(t, col.Attrs, 1)
		require.IsType(t, &postgres.Identity{}, col.Attrs[0])
	})

	t.Run("Postgres with default removes identity from table", func(t *testing.T) {
		tbl := schema.NewTable("users").AddAttrs(&postgres.Identity{})
		col := schema.NewIntColumn("id", "bigint").SetDefault(&schema.Literal{V: "1"})
		(&Postgres{}).atIncrementC(tbl, col)
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})

	t.Run("Postgres serial type removes identity from table", func(t *testing.T) {
		tbl := schema.NewTable("users").AddAttrs(&postgres.Identity{})
		col := &schema.Column{
			Name: "id",
			Type: &schema.ColumnType{Type: &postgres.SerialType{T: postgres.TypeSerial}},
		}
		(&Postgres{}).atIncrementC(tbl, col)
		require.Empty(t, tbl.Attrs)
		require.Empty(t, col.Attrs)
	})
}

// TestDialectAtIndex ensures that index part building works correctly
// for each dialect after the shared helper refactoring.
func TestDialectAtIndex(t *testing.T) {
	t.Run("MySQL with prefix annotation", func(t *testing.T) {
		c1 := &Column{Name: "name", Type: field.TypeString}
		idx1 := &Index{
			Name:       "idx",
			Columns:    []*Column{c1},
			Annotation: &entsql.IndexAnnotation{Prefix: 100},
		}
		t2 := schema.NewTable("users").AddColumns(schema.NewStringColumn("name", "varchar(255)"))
		idx2 := schema.NewIndex("idx")

		d := &MySQL{}
		require.NoError(t, d.atIndex(idx1, t2, idx2))
		require.Len(t, idx2.Parts, 1)
		require.Len(t, idx2.Parts[0].Attrs, 1)
		sp, ok := idx2.Parts[0].Attrs[0].(*mysql.SubPart)
		require.True(t, ok)
		require.Equal(t, 100, sp.Len)
	})

	t.Run("SQLite basic", func(t *testing.T) {
		c1 := &Column{Name: "name", Type: field.TypeString}
		idx1 := &Index{Name: "idx", Columns: []*Column{c1}}
		t2 := schema.NewTable("users").AddColumns(schema.NewStringColumn("name", "text"))
		idx2 := schema.NewIndex("idx")

		d := &SQLite{}
		require.NoError(t, d.atIndex(idx1, t2, idx2))
		require.Len(t, idx2.Parts, 1)
		require.Equal(t, "name", idx2.Parts[0].C.Name)
		require.Empty(t, idx2.Parts[0].Attrs)
	})

	t.Run("SQLite with where clause", func(t *testing.T) {
		c1 := &Column{Name: "active", Type: field.TypeBool}
		idx1 := &Index{
			Name:       "idx",
			Columns:    []*Column{c1},
			Annotation: &entsql.IndexAnnotation{Where: "active = true"},
		}
		t2 := schema.NewTable("users").AddColumns(schema.NewBoolColumn("active", "bool"))
		idx2 := schema.NewIndex("idx")

		d := &SQLite{}
		require.NoError(t, d.atIndex(idx1, t2, idx2))
		require.Len(t, idx2.Attrs, 1)
		pred, ok := idx2.Attrs[0].(*sqlite.IndexPredicate)
		require.True(t, ok)
		require.Equal(t, "active = true", pred.P)
	})

	t.Run("Postgres with opclass", func(t *testing.T) {
		c1 := &Column{Name: "data", Type: field.TypeString}
		idx1 := &Index{
			Name:    "idx",
			Columns: []*Column{c1},
			Annotation: &entsql.IndexAnnotation{
				OpClass:        "text_pattern_ops",
				OpClassColumns: map[string]string{"data": "text_pattern_ops"},
			},
		}
		t2 := schema.NewTable("users").AddColumns(schema.NewStringColumn("data", "text"))
		idx2 := schema.NewIndex("idx")

		d := &Postgres{}
		require.NoError(t, d.atIndex(idx1, t2, idx2))
		require.Len(t, idx2.Parts, 1)
		require.Len(t, idx2.Parts[0].Attrs, 1)
	})

	t.Run("missing column returns error", func(t *testing.T) {
		c1 := &Column{Name: "missing", Type: field.TypeString}
		idx1 := &Index{Name: "idx", Columns: []*Column{c1}}
		t2 := schema.NewTable("users")
		idx2 := schema.NewIndex("idx")

		for _, d := range []interface {
			atIndex(*Index, *schema.Table, *schema.Index) error
		}{
			&MySQL{}, &Postgres{}, &SQLite{},
		} {
			err := d.atIndex(idx1, t2, idx2)
			require.Error(t, err)
			require.Contains(t, err.Error(), "missing")
		}
	})
}
