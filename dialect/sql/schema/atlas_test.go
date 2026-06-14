// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"testing"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"

	"entgo.io/ent/dialect/entsql"

	"github.com/stretchr/testify/require"
)

// TestAtImplicitIndexName locks in the shared implicit unique-index name
// matcher for the naming scheme each dialect feeds it.
func TestAtImplicitIndexName(t *testing.T) {
	tests := []struct {
		name         string
		idx          string
		base, prefix string
		min          int64
		want         bool
	}{
		// MySQL: base is the column name, numbered variants are "<c>_<i>" with i > 1.
		{"mysql/base", "c", "c", "c_", 1, true},
		{"mysql/numbered", "c_2", "c", "c_", 1, true},
		{"mysql/min-excluded", "c_1", "c", "c_", 1, false},
		{"mysql/non-numeric", "c_x", "c", "c_", 1, false},
		{"mysql/unrelated", "d", "c", "c_", 1, false},
		// PostgreSQL: base and prefix are "<t>_<c>_key", numbered variants have i > 0.
		{"postgres/base", "t_c_key", "t_c_key", "t_c_key", 0, true},
		{"postgres/numbered", "t_c_key1", "t_c_key", "t_c_key", 0, true},
		{"postgres/unrelated", "t_c_idx", "t_c_key", "t_c_key", 0, false},
		// SQLite: base is the column name, numbered variants are "sqlite_autoindex_<t>_<i>".
		{"sqlite/base", "c", "c", "sqlite_autoindex_t_", 0, true},
		{"sqlite/numbered", "sqlite_autoindex_t_1", "c", "sqlite_autoindex_t_", 0, true},
		{"sqlite/unrelated", "x", "c", "sqlite_autoindex_t_", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, atImplicitIndexName(tt.idx, tt.base, tt.prefix, tt.min))
		})
	}
}

// TestAtImplicitUniqueIndex verifies, through each dialect's atUniqueC, that the
// shared helper generates the dialect-specific implicit index name and skips it
// when an explicit index already matches that naming scheme.
func TestAtImplicitUniqueIndex(t *testing.T) {
	col := &Column{Name: "name"}
	t1 := &Table{Name: "users", Columns: []*Column{col}}
	newCol := func() *schema.Column { return schema.NewStringColumn("name", "varchar") }

	t.Run("MySQL", func(t *testing.T) {
		t2 := &schema.Table{Name: "users"}
		(&MySQL{}).atUniqueC(t1, col, t2, newCol())
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "name", t2.Indexes[0].Name)
		require.True(t, t2.Indexes[0].Unique)
	})
	t.Run("Postgres", func(t *testing.T) {
		t2 := &schema.Table{Name: "users"}
		(&Postgres{}).atUniqueC(t1, col, t2, newCol())
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_name_key", t2.Indexes[0].Name)
	})
	t.Run("SQLite", func(t *testing.T) {
		t2 := &schema.Table{Name: "users"}
		(&SQLite{}).atUniqueC(t1, col, t2, newCol())
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "users_name_key", t2.Indexes[0].Name)
	})
	t.Run("ExplicitMatchSuppressesImplicit", func(t *testing.T) {
		// An explicitly-defined unique index matching the implicit naming scheme
		// must prevent atUniqueC from adding a duplicate (added later by atIndexes).
		mysqlT1 := &Table{
			Name:    "users",
			Columns: []*Column{col},
			Indexes: []*Index{{Name: "name", Unique: true, Columns: []*Column{col}}},
		}
		t2 := &schema.Table{Name: "users"}
		(&MySQL{}).atUniqueC(mysqlT1, col, t2, newCol())
		require.Empty(t, t2.Indexes)

		pgT1 := &Table{
			Name:    "users",
			Columns: []*Column{col},
			Indexes: []*Index{{Name: "users_name_key", Unique: true, Columns: []*Column{col}}},
		}
		t2 = &schema.Table{Name: "users"}
		(&Postgres{}).atUniqueC(pgT1, col, t2, newCol())
		require.Empty(t, t2.Indexes)
	})
	t.Run("NonUniqueExplicitDoesNotSuppress", func(t *testing.T) {
		// A non-unique index with the same name must not suppress the implicit one.
		nonUniqueT1 := &Table{
			Name:    "users",
			Columns: []*Column{col},
			Indexes: []*Index{{Name: "name", Unique: false, Columns: []*Column{col}}},
		}
		t2 := &schema.Table{Name: "users"}
		(&MySQL{}).atUniqueC(nonUniqueT1, col, t2, newCol())
		require.Len(t, t2.Indexes, 1)
		require.Equal(t, "name", t2.Indexes[0].Name)
	})
}

// TestAtIndexParts verifies the shared column lookup / IndexPart construction and
// the per-dialect decorate hooks (sub-part length, operator class, none).
func TestAtIndexParts(t *testing.T) {
	colA := schema.NewIntColumn("a", "int")
	colB := schema.NewIntColumn("b", "int")
	t2 := func() *schema.Table {
		return &schema.Table{Name: "t", Columns: []*schema.Column{colA, colB}}
	}

	t.Run("SQLite/PlainParts", func(t *testing.T) {
		idx1 := &Index{Name: "i", Columns: []*Column{{Name: "a"}, {Name: "b"}}}
		idx2 := &schema.Index{Name: "i"}
		require.NoError(t, (&SQLite{}).atIndex(idx1, t2(), idx2))
		require.Len(t, idx2.Parts, 2)
		require.Same(t, colA, idx2.Parts[0].C)
		require.Same(t, colB, idx2.Parts[1].C)
		require.Empty(t, idx2.Parts[0].Attrs)
	})
	t.Run("MySQL/SubPart", func(t *testing.T) {
		idx1 := &Index{
			Name:       "i",
			Columns:    []*Column{{Name: "a"}},
			Annotation: &entsql.IndexAnnotation{Prefix: 10},
		}
		idx2 := &schema.Index{Name: "i"}
		require.NoError(t, (&MySQL{}).atIndex(idx1, t2(), idx2))
		require.Len(t, idx2.Parts, 1)
		require.Len(t, idx2.Parts[0].Attrs, 1)
		sp, ok := idx2.Parts[0].Attrs[0].(*mysql.SubPart)
		require.True(t, ok)
		require.Equal(t, 10, sp.Len)
	})
	t.Run("Postgres/OpClass", func(t *testing.T) {
		idx1 := &Index{
			Name:       "i",
			Columns:    []*Column{{Name: "a"}},
			Annotation: &entsql.IndexAnnotation{OpClass: "gin_trgm_ops"},
		}
		idx2 := &schema.Index{Name: "i"}
		require.NoError(t, (&Postgres{version: "13.0.0"}).atIndex(idx1, t2(), idx2))
		require.Len(t, idx2.Parts, 1)
		require.Len(t, idx2.Parts[0].Attrs, 1)
		_, ok := idx2.Parts[0].Attrs[0].(*postgres.IndexOpClass)
		require.True(t, ok)
	})
	t.Run("ErrUnknownColumn", func(t *testing.T) {
		idx1 := &Index{Name: "i", Columns: []*Column{{Name: "missing"}}}
		idx2 := &schema.Index{Name: "i"}
		err := (&SQLite{}).atIndex(idx1, t2(), idx2)
		require.EqualError(t, err, `unexpected index "i" column: "missing"`)
	})
}

// TestAtDefaultIncrementC verifies the shared auto-increment handling used by the
// attribute-based dialects (MySQL, SQLite): annotate the column when no default
// is set, or drop the table-level attribute when a default already exists.
func TestAtDefaultIncrementC(t *testing.T) {
	t.Run("MySQL/Annotate", func(t *testing.T) {
		tb := &schema.Table{Name: "t"}
		c := schema.NewIntColumn("id", "bigint")
		(&MySQL{}).atIncrementC(tb, c)
		require.Len(t, c.Attrs, 1)
		_, ok := c.Attrs[0].(*mysql.AutoIncrement)
		require.True(t, ok)
	})
	t.Run("MySQL/DefaultDropsTableAttr", func(t *testing.T) {
		tb := &schema.Table{Name: "t", Attrs: []schema.Attr{&mysql.AutoIncrement{}}}
		c := schema.NewIntColumn("id", "bigint")
		c.SetDefault(&schema.RawExpr{X: "1"})
		(&MySQL{}).atIncrementC(tb, c)
		require.Empty(t, tb.Attrs)
		require.Empty(t, c.Attrs)
	})
	t.Run("SQLite/Annotate", func(t *testing.T) {
		tb := &schema.Table{Name: "t"}
		c := schema.NewIntColumn("id", "integer")
		(&SQLite{}).atIncrementC(tb, c)
		require.Len(t, c.Attrs, 1)
		_, ok := c.Attrs[0].(*sqlite.AutoIncrement)
		require.True(t, ok)
	})
	t.Run("SQLite/DefaultDropsTableAttr", func(t *testing.T) {
		tb := &schema.Table{Name: "t", Attrs: []schema.Attr{&sqlite.AutoIncrement{}}}
		c := schema.NewIntColumn("id", "integer")
		c.SetDefault(&schema.RawExpr{X: "1"})
		(&SQLite{}).atIncrementC(tb, c)
		require.Empty(t, tb.Attrs)
		require.Empty(t, c.Attrs)
	})
}
