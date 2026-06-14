// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"ariga.io/atlas/sql/migrate"
)

type (
	// WriteDriver is a driver that writes all driver exec operations to its writer.
	// Note that this driver is used only for printing or writing statements to SQL
	// files, and may require manual changes to the generated SQL statements.
	WriteDriver struct {
		dialect.Driver // optional driver for query calls.
		io.Writer      // target for exec statements.
		FormatFunc     func(string) (string, error)
	}
	// DirWriter implements the io.Writer interface
	// for writing to an Atlas managed directory.
	DirWriter struct {
		Dir       migrate.Dir       // target directory.
		Formatter migrate.Formatter // optional formatter.
		b         bytes.Buffer      // working buffer.
		changes   []*migrate.Change // changes to flush.
	}
)

// Write implements the io.Writer interface.
func (d *DirWriter) Write(p []byte) (int, error) {
	return d.b.Write(trimReturning(p))
}

// Change converts all written statement so far into a migration
// change with the given comment.
func (d *DirWriter) Change(comment string) {
	// Trim semicolon and new line, because formatter adds it.
	d.changes = append(d.changes, &migrate.Change{Comment: comment, Cmd: strings.TrimRight(d.b.String(), ";\n")})
	d.b.Reset()
}

// Flush flushes the written statements to the directory.
func (d *DirWriter) Flush(name string) error {
	switch {
	case d.b.Len() != 0:
		return fmt.Errorf("writer has undocumented change. Use Change or FlushChange instead")
	case len(d.changes) == 0:
		return errors.New("writer has no changes to flush")
	default:
		return migrate.NewPlanner(nil, d.Dir, migrate.PlanFormat(d.Formatter)).
			WritePlan(&migrate.Plan{
				Name:    name,
				Changes: d.changes,
			})
	}
}

// FlushChange combines Change and Flush.
func (d *DirWriter) FlushChange(name, comment string) error {
	d.Change(comment)
	return d.Flush(name)
}

// NewWriteDriver creates a dialect.Driver that writes all driver exec statement to its writer.
func NewWriteDriver(dialect string, w io.Writer) *WriteDriver {
	return &WriteDriver{
		Writer: w,
		Driver: nopDriver{dialect: dialect},
	}
}

// Exec implements the dialect.Driver.Exec method.
func (w *WriteDriver) Exec(_ context.Context, query string, args, res any) error {
	if rr, ok := res.(*sql.Result); ok {
		*rr = noResult{}
	}
	if !strings.HasSuffix(query, ";") {
		query += ";"
	}
	if args != nil {
		args, ok := args.([]any)
		if !ok {
			return fmt.Errorf("unexpected args type: %T", args)
		}
		query = w.expandArgs(query, args)
	}
	_, err := io.WriteString(w, query+"\n")
	return err
}

// Query implements the dialect.Driver.Query method.
func (w *WriteDriver) Query(ctx context.Context, query string, args, res any) error {
	if strings.HasPrefix(query, "INSERT") || strings.HasPrefix(query, "UPDATE") {
		if err := w.Exec(ctx, query, args, nil); err != nil {
			return err
		}
		// If the query has a RETURNING clause, mock its columns in the result.
		if rr, ok := res.(*sql.Rows); ok {
			*rr = sql.Rows{ColumnScanner: &noRows{cols: mockReturningColumns(query)}}
		}
		return nil
	}
	switch w.Driver.(type) {
	case nil, nopDriver:
		return errors.New("query is not supported by the WriteDriver")
	default:
		return w.Driver.Query(ctx, query, args, res)
	}
}

// mockReturningColumns reports the column names that the RETURNING clause of an
// INSERT/UPDATE statement would yield, used to mock the columns of sql.Rows.
// It returns nil when the query contains a malformed (unterminated) quote,
// preserving the previous best-effort behavior.
func mockReturningColumns(query string) []string {
	start, end, ok := returningClause(query)
	if !ok {
		return nil
	}
	var clause string
	if start >= 0 {
		clause = query[start:end]
	}
	return returningColumns(clause)
}

// expandArgs combines to arguments and statement into a single statement to
// print or write into a file (before editing).
// Note, the output may be incorrect or unsafe SQL and require manual changes.
func (w *WriteDriver) expandArgs(query string, args []any) string {
	var (
		b    strings.Builder
		p    = w.placeholder()
		scan = w.scanPlaceholder()
	)
	for i := 0; i < len(query); i++ {
		switch query[i] {
		case p:
			idx, size := scan(query[i+1:])
			// Unrecognized placeholder.
			if idx < 0 || idx >= len(args) {
				return query
			}
			i += size
			v, err := w.formatArg(args[idx])
			if err != nil {
				// Unexpected formatting error.
				return query
			}
			b.WriteString(v)
		case '\'', '"', '`': // string or identifier
			frag, skip := skipQuoted(query, i)
			if skip == -1 {
				// Unexpected EOS.
				return query
			}
			b.WriteString(frag)
			i = skip
		default:
			b.WriteByte(query[i])
		}
	}
	return b.String()
}

func (w *WriteDriver) scanPlaceholder() func(string) (int, int) {
	switch w.Dialect() {
	case dialect.Postgres:
		return func(s string) (int, int) {
			var i int
			for i < len(s) && unicode.IsDigit(rune(s[i])) {
				i++
			}
			idx, err := strconv.ParseInt(s[:i], 10, 64)
			if err != nil {
				return -1, 0
			}
			// Placeholders are 1-based.
			return int(idx) - 1, i
		}
	default:
		idx := -1
		return func(string) (int, int) {
			idx++
			return idx, 0
		}
	}
}

func (w *WriteDriver) placeholder() byte {
	if w.Dialect() == dialect.Postgres {
		return '$'
	}
	return '?'
}

func (w *WriteDriver) formatArg(v any) (string, error) {
	if w.FormatFunc != nil {
		return w.FormatFunc(fmt.Sprint(v))
	}
	switch v := v.(type) {
	case nil:
		return "NULL", nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v), nil
	case float32, float64:
		return fmt.Sprintf("%g", v), nil
	case bool:
		if v {
			return "1", nil
		} else {
			return "0", nil
		}
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
	case json.RawMessage:
		return "'" + strings.ReplaceAll(string(v), "'", "''") + "'", nil
	case []byte:
		return "{{ BINARY_VALUE }}", nil
	case time.Time:
		return "{{ TIME_VALUE }}", nil
	case fmt.Stringer:
		return "'" + strings.ReplaceAll(v.String(), "'", "''") + "'", nil
	default:
		return "{{ VALUE }}", nil
	}
}

var reReturning = regexp.MustCompile(`(?i)^\s?RETURNING`)

// isSQLSpace reports whether b is one of the ASCII whitespace bytes matched by
// the regexp \s class (tab, newline, form feed, carriage return and space).
func isSQLSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\f', '\r':
		return true
	default:
		return false
	}
}

// matchReturning reports whether s begins with an optional single whitespace
// byte followed by the case-insensitive keyword "RETURNING". It mirrors the
// reReturning regexp but allocates nothing per call and works uniformly over
// both []byte and string inputs, so callers can share a single scan convention.
func matchReturning[T []byte | string](s T) bool {
	var i int
	if len(s) > 0 && isSQLSpace(s[0]) {
		i = 1
	}
	const kw = "RETURNING"
	if len(s)-i < len(kw) {
		return false
	}
	for k := 0; k < len(kw); k++ {
		c := s[i+k]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c != kw[k] {
			return false
		}
	}
	return true
}

// skipQuoted returns the quoted string or identifier that starts at idx (whose
// byte is the opening quote) together with the index of its closing quote.
// Backslash-escaped characters are skipped. It returns an index of -1 when the
// quote is never closed (unexpected end of statement).
func skipQuoted[T []byte | string](query T, idx int) (T, int) {
	for j := idx + 1; j < len(query); j++ {
		switch query[j] {
		case '\\':
			j++
		case query[idx]:
			return query[idx : j+1], j
		}
	}
	// Unexpected EOS.
	return query, -1
}

// returningClause scans query for the first unquoted RETURNING clause using the
// shared string/identifier skipping rules. When scanning completes without
// malformed quoting, ok is true and:
//
//   - start is the index where the clause begins (the optional leading space and
//     the RETURNING keyword), or a negative value when there is no such clause;
//   - end is the index of the terminating unquoted ';' or len(query) when the
//     clause runs to the end of the statement.
//
// A false ok signals a malformed (unterminated) quote, in which case start/end
// are unspecified and callers should treat the statement as best-effort.
func returningClause[T []byte | string](query T) (start, end int, ok bool) {
	for i := 0; i < len(query); i++ {
		switch q := query[i]; {
		case q == '\'', q == '"', q == '`': // string or identifier
			if _, skip := skipQuoted(query, i); skip != -1 {
				i = skip
			} else {
				return -1, 0, false
			}
		case matchReturning(query[i:]):
			// Forward until the next unquoted ';' appears, or we reach the
			// end of the statement, honoring the same quote skipping rules.
			for j := i; j < len(query); j++ {
				switch query[j] {
				case '\'', '"', '`': // string or identifier
					if _, skip := skipQuoted(query, j); skip != -1 {
						j = skip
					} else {
						return -1, 0, false
					}
				case ';':
					return i, j, true
				}
			}
			return i, len(query), true
		}
	}
	return -1, len(query), true
}

// returningColumns extracts the comma-separated column names from a RETURNING
// clause as located by returningClause, dropping the leading RETURNING keyword
// and trimming the whitespace surrounding each name.
func returningColumns(clause string) []string {
	cols := strings.Split(reReturning.ReplaceAllString(clause, ""), ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return cols
}

// trimReturning trims any RETURNING suffix from INSERT/UPDATE queries.
// Note, that the output may be incorrect or unsafe SQL and require manual changes.
func trimReturning(query []byte) []byte {
	start, end, ok := returningClause(query)
	// Leave the statement untouched on malformed quoting or when there is no
	// RETURNING clause to trim.
	if !ok || start < 0 {
		return query
	}
	var b bytes.Buffer
	b.Grow(len(query))
	b.Write(query[:start])
	// Preserve the terminating ';' (and anything following it) when present.
	if end < len(query) {
		b.Write(query[end:])
	}
	return b.Bytes()
}

// Tx writes the transaction start.
func (w *WriteDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(w), nil
}

// noResult represents a zero result.
type noResult struct{}

func (noResult) LastInsertId() (int64, error) { return 0, nil }
func (noResult) RowsAffected() (int64, error) { return 0, nil }

// noRows represents no rows.
type noRows struct {
	sql.ColumnScanner
	cols []string
	done bool
}

func (*noRows) Close() error { return nil }
func (*noRows) Err() error   { return nil }
func (r *noRows) Next() bool {
	if !r.done {
		r.done = true
		return true
	}
	return false
}
func (r *noRows) Columns() ([]string, error) { return r.cols, nil }
func (*noRows) Scan(...any) error            { return nil }

type nopDriver struct {
	dialect.Driver
	dialect string
}

func (d nopDriver) Dialect() string { return d.dialect }

func (nopDriver) Query(context.Context, string, any, any) error {
	return nil
}
