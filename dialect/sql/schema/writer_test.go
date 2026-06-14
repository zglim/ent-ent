// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"

	"ariga.io/atlas/sql/migrate"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWriteDriver(t *testing.T) {
	b := &bytes.Buffer{}
	w := NewWriteDriver(dialect.MySQL, b)
	ctx := context.Background()
	tx, err := w.Tx(ctx)
	require.NoError(t, err)
	err = tx.Query(ctx, "SELECT `name` FROM `users`", nil, nil)
	require.EqualError(t, err, "query is not supported by the WriteDriver")
	err = tx.Exec(ctx, "ALTER TABLE `users` ADD COLUMN `age` int", nil, nil)
	require.NoError(t, err)
	err = tx.Exec(ctx, "ALTER TABLE `users` ADD COLUMN `NAME` varchar(100);", nil, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	lines := strings.Split(b.String(), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, "ALTER TABLE `users` ADD COLUMN `age` int;", lines[0])
	require.Equal(t, "ALTER TABLE `users` ADD COLUMN `NAME` varchar(100);", lines[1])
	require.Empty(t, lines[2], "file ends with blank line")

	b.Reset()
	query, args := sql.Update("users").Schema("test").Set("a", 1).Set("b", "a").Set("c", "'c'").Set("d", true).Where(sql.EQ("p", 0.2)).Query()
	err = w.Exec(ctx, query, args, nil)
	require.NoError(t, err)
	require.Equal(t, "UPDATE `test`.`users` SET `a` = 1, `b` = 'a', `c` = '''c''', `d` = 1 WHERE `p` = 0.2;\n", b.String())

	b.Reset()
	query, args = sql.Dialect(dialect.MySQL).Update("users").Schema("test").Set("a", "{}").Where(sqljson.ValueIsNull("a")).Query()
	err = w.Exec(ctx, query, args, nil)
	require.NoError(t, err)
	require.Equal(t, "UPDATE `test`.`users` SET `a` = '{}' WHERE JSON_CONTAINS(`a`, 'null', '$');\n", b.String())

	b.Reset()
	w = NewWriteDriver(dialect.Postgres, b)
	query, args = sql.Dialect(dialect.Postgres).Update("users").Set("id", uuid.Nil).Set("a", 1).Set("b", time.Now()).Query()
	err = w.Exec(ctx, query, args, nil)
	require.NoError(t, err)
	require.Equal(t, `UPDATE "users" SET "id" = '00000000-0000-0000-0000-000000000000', "a" = 1, "b" = {{ TIME_VALUE }};`+"\n", b.String())

	b.Reset()
	err = w.Exec(ctx, `INSERT INTO "users" (name) VALUES("a8m") RETURNING id`, nil, nil)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" (name) VALUES("a8m") RETURNING id;`+"\n", b.String())

	// batchCreator uses tx.Query when doing an insert
	b.Reset()
	err = w.Query(ctx, `INSERT INTO "users" (name) VALUES("a8m") RETURNING id`, nil, nil)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" (name) VALUES("a8m") RETURNING id;`+"\n", b.String())

	// correct columns are extracted from a returning clause and returned by sql.ColumnScanner.
	for q, cols := range map[string][]string{
		`INSERT INTO "users" (name) VALUES("a8m") RETURNING id`:                          {"id"},
		`INSERT INTO "users" (name) VALUES("a8m") RETURNING id, "name"`:                  {"id", `"name"`},
		`INSERT INTO "users" (name) VALUES("a8m") RETURNING "id", "name"`:                {`"id"`, `"name"`},
		`INSERT INTO "users" (name) VALUES("a8m") RETURNING "id", "name"; DROP "groups"`: {`"id"`, `"name"`},
	} {
		var rows sql.Rows
		err = w.Query(ctx, q, nil, &rows)
		require.NoError(t, err)
		require.True(t, rows.Next())
		c, err := rows.Columns()
		require.NoError(t, err)
		require.Equal(t, cols, c)
		require.NoError(t, rows.Scan())
	}
	b.Reset()
}

func TestSkipQuoted(t *testing.T) {
	tests := []struct {
		name  string
		query string
		idx   int
		want  string
		end   int
	}{
		{
			name:  "single-quoted string",
			query: "'hello world' rest",
			idx:   0,
			want:  "'hello world'",
			end:   12,
		},
		{
			name:  "double-quoted identifier",
			query: `"col_name" FROM`,
			idx:   0,
			want:  `"col_name"`,
			end:   9,
		},
		{
			name:  "backtick identifier",
			query: "`table` WHERE",
			idx:   0,
			want:  "`table`",
			end:   6,
		},
		{
			name:  "backslash-escaped quote inside",
			query: `'it\'s a test' end`,
			idx:   0,
			want:  `'it\'s a test'`,
			end:   13,
		},
		{
			name:  "backslash escape",
			query: `'it\'s' end`,
			idx:   0,
			want:  `'it\'s'`,
			end:   6,
		},
		{
			name:  "quoted in middle",
			query: "SELECT 'value' FROM t",
			idx:   7,
			want:  "'value'",
			end:   13,
		},
		{
			name:  "unterminated string",
			query: "'unterminated",
			idx:   0,
			want:  "'unterminated",
			end:   -1,
		},
		{
			name:  "unterminated double quote",
			query: `"unterminated`,
			idx:   0,
			want:  `"unterminated`,
			end:   -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, end := skipQuoted(tt.query, tt.idx)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.end, end)
		})
	}
}

func TestSQLScanner(t *testing.T) {
	t.Run("basic iteration", func(t *testing.T) {
		sc := newSQLScanner("SELECT 1")
		var chars []byte
		for sc.next() {
			chars = append(chars, sc.cur())
		}
		require.Equal(t, []byte("SELECT 1"), chars)
	})

	t.Run("quoted string skipping", func(t *testing.T) {
		sc := newSQLScanner("a 'hello' b")
		var parts []string
		for sc.next() {
			if sc.isQuoted() {
				parts = append(parts, sc.quoted())
			} else {
				parts = append(parts, string(sc.cur()))
			}
		}
		require.Equal(t, []string{"a", " ", "'hello'", " ", "b"}, parts)
		require.False(t, sc.malformed())
	})

	t.Run("double-quoted identifier", func(t *testing.T) {
		sc := newSQLScanner(`x "col" y`)
		var parts []string
		for sc.next() {
			if sc.isQuoted() {
				parts = append(parts, sc.quoted())
			} else {
				parts = append(parts, string(sc.cur()))
			}
		}
		require.Equal(t, []string{"x", " ", `"col"`, " ", "y"}, parts)
	})

	t.Run("backtick identifier", func(t *testing.T) {
		sc := newSQLScanner("a `tbl` b")
		var parts []string
		for sc.next() {
			if sc.isQuoted() {
				parts = append(parts, sc.quoted())
			} else {
				parts = append(parts, string(sc.cur()))
			}
		}
		require.Equal(t, []string{"a", " ", "`tbl`", " ", "b"}, parts)
	})

	t.Run("malformed unterminated", func(t *testing.T) {
		sc := newSQLScanner("a 'unterminated")
		for sc.next() {
			sc.isQuoted()
		}
		require.True(t, sc.malformed())
	})

	t.Run("empty input", func(t *testing.T) {
		sc := newSQLScanner("")
		require.False(t, sc.next())
	})
}

func TestFindReturningClause(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "simple returning id",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id`,
			want:  " RETURNING id",
		},
		{
			name:  "returning multiple columns",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id, "name"`,
			want:  ` RETURNING id, "name"`,
		},
		{
			name:  "returning with semicolon",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING "id", "name"; DROP "groups"`,
			want:  ` RETURNING "id", "name"`,
		},
		{
			name:  "no returning clause",
			query: `INSERT INTO "users" (name) VALUES("a8m")`,
			want:  "",
		},
		{
			name:  "returning not confused with string content",
			query: `INSERT INTO t (v) VALUES('RETURNING fake') RETURNING id`,
			want:  " RETURNING id",
		},
		{
			name:  "lowercase returning",
			query: `INSERT INTO t VALUES(1) returning id`,
			want:  " returning id",
		},
		{
			name:  "returning no space prefix",
			query: `INSERT INTO t VALUES(1)RETURNING id`,
			want:  "RETURNING id",
		},
		{
			name:  "update returning",
			query: `UPDATE "users" SET a=1 RETURNING id`,
			want:  " RETURNING id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findReturningClause(tt.query)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseReturningColumns(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "single column",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id`,
			want:  []string{"id"},
		},
		{
			name:  "multiple columns",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id, "name"`,
			want:  []string{"id", `"name"`},
		},
		{
			name:  "quoted columns",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING "id", "name"`,
			want:  []string{`"id"`, `"name"`},
		},
		{
			name:  "with semicolon and trailing SQL",
			query: `INSERT INTO "users" (name) VALUES("a8m") RETURNING "id", "name"; DROP "groups"`,
			want:  []string{`"id"`, `"name"`},
		},
		{
			name:  "no returning clause",
			query: `INSERT INTO "users" (name) VALUES("a8m")`,
			want:  nil,
		},
		{
			name:  "returning inside string literal ignored",
			query: `INSERT INTO t (v) VALUES('RETURNING fake') RETURNING id`,
			want:  []string{"id"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseReturningColumns(tt.query)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTrimReturning(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple returning trimmed",
			input: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id;`,
			want:  `INSERT INTO "users" (name) VALUES("a8m");`,
		},
		{
			name:  "returning multiple cols trimmed",
			input: `INSERT INTO "users" (name) VALUES("a8m") RETURNING id, "name";`,
			want:  `INSERT INTO "users" (name) VALUES("a8m");`,
		},
		{
			name:  "no returning unchanged",
			input: `INSERT INTO "users" (name) VALUES("a8m");`,
			want:  `INSERT INTO "users" (name) VALUES("a8m");`,
		},
		{
			name:  "returning in string not trimmed",
			input: `INSERT INTO t (v) VALUES('RETURNING fake');`,
			want:  `INSERT INTO t (v) VALUES('RETURNING fake');`,
		},
		{
			name:  "backtick quoted returning preserved",
			input: "INSERT INTO `users` (`name`) VALUES ('val') RETURNING `id`;",
			want:  "INSERT INTO `users` (`name`) VALUES ('val');",
		},
		{
			name:  "no space before returning",
			input: "INSERT INTO `users` (`name`) VALUES ('val')RETURNING `id`;",
			want:  "INSERT INTO `users` (`name`) VALUES ('val');",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimReturning([]byte(tt.input))
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestExpandArgs(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		query   string
		args    []any
		want    string
	}{
		{
			name:    "mysql question marks",
			dialect: dialect.MySQL,
			query:   "INSERT INTO t (a, b) VALUES (?, ?)",
			args:    []any{1, "hello"},
			want:    "INSERT INTO t (a, b) VALUES (1, 'hello')",
		},
		{
			name:    "postgres dollar placeholders",
			dialect: dialect.Postgres,
			query:   `INSERT INTO "t" (a, b) VALUES ($1, $2)`,
			args:    []any{42, "world"},
			want:    `INSERT INTO "t" (a, b) VALUES (42, 'world')`,
		},
		{
			name:    "quoted content not treated as placeholder",
			dialect: dialect.MySQL,
			query:   "INSERT INTO t (a) VALUES ('?')",
			args:    []any{1},
			want:    "INSERT INTO t (a) VALUES ('?')",
		},
		{
			name:    "double-quoted identifier with placeholder after",
			dialect: dialect.Postgres,
			query:   `UPDATE "t" SET "a" = $1 WHERE "b" = $2`,
			args:    []any{"val", 10},
			want:    `UPDATE "t" SET "a" = 'val' WHERE "b" = 10`,
		},
		{
			name:    "string with escaped quote",
			dialect: dialect.MySQL,
			query:   "INSERT INTO t (a) VALUES (?, ?)",
			args:    []any{"it's", nil},
			want:    "INSERT INTO t (a) VALUES ('it''s', NULL)",
		},
		{
			name:    "boolean values",
			dialect: dialect.MySQL,
			query:   "UPDATE t SET a=?, b=?",
			args:    []any{true, false},
			want:    "UPDATE t SET a=1, b=0",
		},
		{
			name:    "float value",
			dialect: dialect.MySQL,
			query:   "SELECT * WHERE x=?",
			args:    []any{0.2},
			want:    "SELECT * WHERE x=0.2",
		},
		{
			name:    "no args returns query unchanged",
			dialect: dialect.MySQL,
			query:   "SELECT 1",
			args:    nil,
			want:    "SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWriteDriver(tt.dialect, &bytes.Buffer{})
			if tt.args == nil {
				// expandArgs is not called for nil args in Exec,
				// but test that it returns query unchanged if called.
				require.Equal(t, tt.query, w.expandArgs(tt.query, tt.args))
				return
			}
			got := w.expandArgs(tt.query, tt.args)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatArg(t *testing.T) {
	w := NewWriteDriver(dialect.MySQL, &bytes.Buffer{})
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, "NULL"},
		{"int", 42, "42"},
		{"int64", int64(100), "100"},
		{"float64", 3.14, "3.14"},
		{"bool true", true, "1"},
		{"bool false", false, "0"},
		{"string", "hello", "'hello'"},
		{"string with quote", "it's", "'it''s'"},
		{"byte slice", []byte{0x01}, "{{ BINARY_VALUE }}"},
		{"time", time.Now(), "{{ TIME_VALUE }}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := w.formatArg(tt.val)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDirWriter(t *testing.T) {
	for _, tt := range []struct {
		dialect  string
		exec     []string
		comments []string
		args     [][]any
		want     string
	}{
		{
			dialect.MySQL,
			[]string{
				"UPDATE `test`.`users` SET `a` = ?",
				"UPDATE `test`.`users` SET `b` = ?",
			},
			[]string{
				"Comment 1.",
				"Comment 2.",
			},
			[][]any{
				{1},
				{2},
			},
			"-- Comment 1.\nUPDATE `test`.`users` SET `a` = 1;\n-- Comment 2.\nUPDATE `test`.`users` SET `b` = 2;\n",
		},
		{
			dialect.Postgres,
			[]string{
				"INSERT INTO \"users\" (\"name\", \"email\") VALUES ($1, $2) RETURNING \"id\"",
				"INSERT INTO \"groups\" (\"name\") VALUES ($1) RETURNING \"id\"",
			},
			[]string{
				"Seed users table",
				"Seed groups table",
			},
			[][]any{
				{"masseelch", "j@ariga.io"},
				{"admins"},
			},
			strings.Join([]string{
				"-- Seed users table\nINSERT INTO \"users\" (\"name\", \"email\") VALUES ('masseelch', 'j@ariga.io');\n",
				"-- Seed groups table\nINSERT INTO \"groups\" (\"name\") VALUES ('admins');\n",
			}, ""),
		},
		{
			dialect.SQLite,
			[]string{
				"INSERT INTO `users` (`name`, `email`) VALUES (?, ?) RETURNING `id`",
				"INSERT INTO `groups` (`name`) VALUES (?) RETURNING `id`",
			},
			[]string{
				"Seed users table",
				"Seed groups table",
			},
			[][]any{
				{"masseelch", "j@ariga.io"},
				{"admins"},
			},
			strings.Join([]string{
				"-- Seed users table\nINSERT INTO `users` (`name`, `email`) VALUES ('masseelch', 'j@ariga.io');\n",
				"-- Seed groups table\nINSERT INTO `groups` (`name`) VALUES ('admins');\n",
			}, ""),
		},
		{
			dialect.SQLite + " no space",
			[]string{"INSERT INTO `users` (`name`) VALUES (?)RETURNING `id`"},
			[]string{"Seed users table"},
			[][]any{{"masseelch"}},
			"-- Seed users table\nINSERT INTO `users` (`name`) VALUES ('masseelch');\n",
		},
	} {
		t.Run(tt.dialect, func(t *testing.T) {
			var (
				p   = t.TempDir()
				dir = func() migrate.Dir {
					d, err := migrate.NewLocalDir(p)
					require.NoError(t, err)
					return d
				}()
				w   = &DirWriter{Dir: dir}
				drv = NewWriteDriver(tt.dialect, w)
			)
			for i := range tt.exec {
				require.NoError(t, drv.Exec(context.Background(), tt.exec[i], tt.args[i], nil))
				w.Change(tt.comments[i])
			}
			require.NoError(t, w.Flush("migration_file"))
			files, err := os.ReadDir(p)
			require.NoError(t, err)
			require.Len(t, files, 2)
			require.Contains(t, files[0].Name(), "_migration_file.sql")
			buf, err := os.ReadFile(filepath.Join(p, files[0].Name()))
			require.NoError(t, err)
			require.Equal(t, tt.want, string(buf))
			require.Equal(t, "atlas.sum", files[1].Name())
		})
	}
}
