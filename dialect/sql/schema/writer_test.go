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

// TestMatchReturning guards that the hand-written matcher stays equivalent to
// the reReturning regexp it replaces in the hot scanning loops.
func TestMatchReturning(t *testing.T) {
	for _, s := range []string{
		"", "R", "RETURN", "RETURNING", "returning x", "ReTuRnInG id",
		" RETURNING", "\tRETURNING", "\nRETURNING", "\fRETURNING", "\rRETURNING",
		"  RETURNING", "xRETURNING", "RETURNINX", " returning",
	} {
		require.Equalf(t, reReturning.MatchString(s), matchReturning(s), "string %q", s)
		require.Equalf(t, reReturning.Match([]byte(s)), matchReturning([]byte(s)), "bytes %q", s)
	}
}

// TestReturningClause covers the shared scan that locates a RETURNING clause,
// including quoted content that must not be mistaken for the keyword.
func TestReturningClause(t *testing.T) {
	for _, tt := range []struct {
		name   string
		query  string
		clause string // expected query[start:end]; "" means no clause located
		found  bool
		ok     bool
	}{
		{"none", `INSERT INTO "t" ("c") VALUES ('v')`, "", false, true},
		{"keyword in string", `INSERT INTO "t" ("c") VALUES ('x RETURNING y')`, "", false, true},
		{"keyword in identifier", "INSERT INTO `RETURNING` (`c`) VALUES ('v')", "", false, true},
		{"simple", `INSERT INTO "t" ("c") VALUES ('v') RETURNING id`, ` RETURNING id`, true, true},
		{"quoted cols", `INSERT INTO "t" ("c") VALUES ('v') RETURNING "id", "name"`, ` RETURNING "id", "name"`, true, true},
		{"terminated", `INSERT INTO "t" ("c") VALUES ('v') RETURNING "id"; DROP "x"`, ` RETURNING "id"`, true, true},
		{"no leading space", `INSERT INTO "t" ("c") VALUES ('v')RETURNING id`, `RETURNING id`, true, true},
		{"malformed quote", `INSERT INTO "t ("c") VALUES ('v') RETURNING id`, "", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start, end, ok := returningClause(tt.query)
			require.Equal(t, tt.ok, ok)
			if !ok {
				return
			}
			require.Equal(t, tt.found, start >= 0)
			if start >= 0 {
				require.Equal(t, tt.clause, tt.query[start:end])
			}
			// The []byte path used by trimReturning must agree with the string path.
			bs, be, bok := returningClause([]byte(tt.query))
			require.Equal(t, ok, bok)
			require.Equal(t, start, bs)
			require.Equal(t, end, be)
		})
	}
}

// TestReturningColumns covers the column-name extraction from a located clause.
func TestReturningColumns(t *testing.T) {
	require.Equal(t, []string{"id"}, returningColumns(` RETURNING id`))
	require.Equal(t, []string{"id", `"name"`}, returningColumns(` RETURNING id, "name"`))
	require.Equal(t, []string{`"id"`, `"name"`}, returningColumns(`RETURNING "id", "name"`))
	require.Equal(t, []string{""}, returningColumns(""))
}

// TestTrimReturning covers dropping the RETURNING suffix while keeping quoted
// content and any terminating statement intact.
func TestTrimReturning(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{
			"postgres identifiers",
			`INSERT INTO "users" ("name") VALUES ('a8m') RETURNING "id";`,
			`INSERT INTO "users" ("name") VALUES ('a8m');`,
		},
		{
			"no leading space",
			"INSERT INTO `users` (`name`) VALUES ('a8m')RETURNING `id`;",
			"INSERT INTO `users` (`name`) VALUES ('a8m');",
		},
		{
			"keyword inside string is kept",
			`INSERT INTO "t" ("c") VALUES ('x RETURNING y');`,
			`INSERT INTO "t" ("c") VALUES ('x RETURNING y');`,
		},
		{
			"no returning",
			`INSERT INTO "t" ("c") VALUES ('v');`,
			`INSERT INTO "t" ("c") VALUES ('v');`,
		},
		{
			"malformed quote is untouched",
			`INSERT INTO "t" ("c") VALUES ('unclosed RETURNING "id"`,
			`INSERT INTO "t" ("c") VALUES ('unclosed RETURNING "id"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(trimReturning([]byte(tt.in))))
		})
	}
}

// TestExpandArgs covers placeholder expansion across dialects together with the
// quote-skipping convention that protects literal placeholders inside strings.
func TestExpandArgs(t *testing.T) {
	b := &bytes.Buffer{}
	pg := NewWriteDriver(dialect.Postgres, b)
	require.Equal(t,
		`INSERT INTO "users" ("name", "age") VALUES ('a8m', 42)`,
		pg.expandArgs(`INSERT INTO "users" ("name", "age") VALUES ($1, $2)`, []any{"a8m", 42}),
	)
	// A '$1' inside a string literal must not be expanded as a placeholder.
	require.Equal(t,
		`UPDATE "t" SET "a" = '$1', "b" = 7`,
		pg.expandArgs(`UPDATE "t" SET "a" = '$1', "b" = $1`, []any{7}),
	)

	my := NewWriteDriver(dialect.MySQL, b)
	require.Equal(t,
		"UPDATE `t` SET `a` = 1, `b` = 'x'",
		my.expandArgs("UPDATE `t` SET `a` = ?, `b` = ?", []any{1, "x"}),
	)
	// A '?' inside a string literal must not consume a positional argument.
	require.Equal(t,
		"UPDATE `t` SET `a` = '?', `b` = 5",
		my.expandArgs("UPDATE `t` SET `a` = '?', `b` = ?", []any{5}),
	)
}

// TestMockReturningColumns covers the columns mocked for sql.Rows from a
// RETURNING clause, including malformed and missing clauses.
func TestMockReturningColumns(t *testing.T) {
	require.Equal(t, []string{"id"},
		mockReturningColumns(`INSERT INTO "users" (name) VALUES('a8m') RETURNING id`))
	require.Equal(t, []string{`"id"`, `"name"`},
		mockReturningColumns(`INSERT INTO "users" (name) VALUES('a8m') RETURNING "id", "name"; DROP "x"`))
	// No RETURNING clause yields a single empty column name (legacy behavior).
	require.Equal(t, []string{""},
		mockReturningColumns(`INSERT INTO "users" (name) VALUES('a8m')`))
	// A malformed quote yields nil columns.
	require.Nil(t,
		mockReturningColumns(`INSERT INTO "users VALUES('a8m') RETURNING id`))
}
