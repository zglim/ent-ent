// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package sqljson

import (
	"fmt"
	"reflect"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
)

// arrayAppender abstracts the SQL differences between the MySQL and SQLite
// implementations of appending elements to a JSON array. The shared
// CASE-WHEN flow lives in appendToArray, so each dialect only needs to
// describe its own SQL specifics.
type arrayAppender interface {
	// jsonType writes the expression returning the JSON type of the value
	// at the column's path. It is used to detect a NULL or JSON "null".
	jsonType(b *sql.Builder, column string, opts []Option)
	// jsonNull is the textual JSON null literal that jsonType returns
	// (already quoted, e.g. 'null' or 'NULL').
	jsonNull() string
	// initArray writes a JSON array literal built from elems, used when the
	// current value is NULL or JSON "null". When nested is true the literal
	// is embedded as a JSON_SET value and must be a JSON expression;
	// otherwise it is assigned directly to the column.
	initArray(b *sql.Builder, elems []any, nested bool)
	// appendArray writes the call that appends elems into the existing
	// (non-null) JSON array located at the column's path.
	appendArray(b *sql.Builder, column string, elems []any, opts []Option)
}

// appendToArray builds the CASE-WHEN statement shared by MySQL and SQLite for
// appending elems to a JSON array, optionally located at the given path. The
// dialect-specific SQL is delegated to the given arrayAppender.
//
// Following the Go semantics, the column/key is initialized with the given
// array in case its current value is NULL or a JSON "null"; otherwise the
// elements are appended to the existing array.
func appendToArray(u *sql.UpdateBuilder, column string, elems []any, opts []Option, d arrayAppender) {
	setCase(u, column, when{
		Cond: func(b *sql.Builder) {
			d.jsonType(b, column, opts)
			b.WriteOp(sql.OpIsNull)
			b.WriteString(" OR ")
			d.jsonType(b, column, opts)
			b.WriteOp(sql.OpEQ).WriteString(d.jsonNull())
		},
		Then: func(b *sql.Builder) {
			// If a path was provided, set the value at this path; otherwise,
			// the top-level value is the JSON array itself.
			if len(opts) > 0 {
				b.WriteString("JSON_SET").Wrap(func(b *sql.Builder) {
					b.Ident(column).Comma()
					identPath(column, opts...).mysqlPath(b)
					b.Comma()
					d.initArray(b, elems, true)
				})
			} else {
				d.initArray(b, elems, false)
			}
		},
		Else: func(b *sql.Builder) {
			d.appendArray(b, column, elems, opts)
		},
	})
}

// appendElems writes the call "fn(column, path, arg, path, arg, ...)" used by
// the MySQL/SQLite array-append functions, delegating the per-element path and
// argument formatting to the given writers.
func appendElems(b *sql.Builder, fn, column string, elems []any, path func(*sql.Builder), arg func(*sql.Builder, any)) {
	b.WriteString(fn).Wrap(func(b *sql.Builder) {
		b.Ident(column).Comma()
		for i, e := range elems {
			if i > 0 {
				b.Comma()
			}
			path(b)
			b.Comma()
			arg(b, e)
		}
	})
}

// appendIndexPath returns a writer for the array-append path used by SQLite's
// JSON_INSERT, i.e. the column path suffixed with the append index "[#]"
// ('$[#]' for the top-level array or '$.a[#]' for a nested one).
func appendIndexPath(column string, opts ...Option) func(*sql.Builder) {
	p := identPath(column, opts...)
	p.Path = append(p.Path, "[#]")
	return p.mysqlPath
}

type sqlite struct{}

// Append implements the driver.Append method.
func (d *sqlite) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	appendToArray(u, column, elems, opts, d)
}

func (*sqlite) jsonType(b *sql.Builder, column string, opts []Option) {
	identPath(column, opts...).mysqlFunc("JSON_TYPE", b)
}

func (*sqlite) jsonNull() string { return "'null'" }

func (*sqlite) initArray(b *sql.Builder, elems []any, nested bool) {
	// As a JSON_SET value the document must be parsed back into JSON,
	// while at the top level it is assigned to the column as-is.
	if nested {
		b.Argf("JSON(?)", marshalArg(elems))
	} else {
		b.Arg(marshalArg(elems))
	}
}

func (d *sqlite) appendArray(b *sql.Builder, column string, elems []any, opts []Option) {
	appendElems(b, "JSON_INSERT", column, elems, appendIndexPath(column, opts...), d.appendArg)
}

func (d *sqlite) appendArg(b *sql.Builder, v any) {
	switch {
	case !isPrimitive(v):
		b.Argf("JSON(?)", marshalArg(v))
	default:
		b.Arg(v)
	}
}

type mysql struct{}

// Append implements the driver.Append method.
func (d *mysql) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	appendToArray(u, column, elems, opts, d)
}

func (*mysql) jsonType(b *sql.Builder, column string, opts []Option) {
	b.WriteString("JSON_TYPE(")
	identPath(column, opts...).mysqlFunc("JSON_EXTRACT", b)
	b.WriteByte(')')
}

func (*mysql) jsonNull() string { return "'NULL'" }

func (d *mysql) initArray(b *sql.Builder, elems []any, _ bool) {
	b.WriteString("JSON_ARRAY(").Args(d.marshalArgs(elems)...).WriteByte(')')
}

func (d *mysql) appendArray(b *sql.Builder, column string, elems []any, opts []Option) {
	appendElems(b, "JSON_ARRAY_APPEND", column, elems, identPath(column, opts...).mysqlPath, d.appendArg)
}

func (d *mysql) marshalArgs(args []any) []any {
	vs := make([]any, len(args))
	for i, v := range args {
		if !isPrimitive(v) {
			v = marshalArg(v)
		}
		vs[i] = v
	}
	return vs
}

func (d *mysql) appendArg(b *sql.Builder, v any) {
	switch {
	case !isPrimitive(v):
		b.Argf("CAST(? AS JSON)", marshalArg(v))
	default:
		b.Arg(v)
	}
}

type postgres struct{}

// Append implements the driver.Append method.
func (*postgres) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	setCase(u, column, when{
		Cond: func(b *sql.Builder) {
			// Compare the jsonb value at the path against NULL and JSON "null".
			value := identPath(column, append(opts, Cast("jsonb"))...)
			value.value(b)
			b.WriteOp(sql.OpIsNull)
			b.WriteString(" OR ")
			value.value(b)
			b.WriteOp(sql.OpEQ).WriteString("'null'::jsonb")
		},
		Then: func(b *sql.Builder) {
			if len(opts) > 0 {
				pgJSONBSet(b, column, opts, func(b *sql.Builder) {
					b.Arg(marshalArg(elems))
				})
			} else {
				b.Arg(marshalArg(elems))
			}
		},
		Else: func(b *sql.Builder) {
			if len(opts) > 0 {
				pgJSONBSet(b, column, opts, func(b *sql.Builder) {
					identPath(column, opts...).value(b)
					b.WriteString(" || ").Arg(marshalArg(elems))
				})
			} else {
				b.Ident(column).WriteString(" || ").Arg(marshalArg(elems))
			}
		},
	})
}

// pgJSONBSet writes "jsonb_set(column, '{path}', value, true)", delegating the
// value expression (the new array or the concatenation) to the given writer.
func pgJSONBSet(b *sql.Builder, column string, opts []Option, value func(*sql.Builder)) {
	b.WriteString("jsonb_set").Wrap(func(b *sql.Builder) {
		b.Ident(column).Comma()
		identPath(column, opts...).pgArrayPath(b)
		b.Comma()
		value(b)
		b.Comma().WriteString("true")
	})
}

// driver groups all dialect-specific methods.
type driver interface {
	Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option)
}

func newDriver(name string) (driver, error) {
	switch name {
	case dialect.SQLite:
		return (*sqlite)(nil), nil
	case dialect.MySQL:
		return (*mysql)(nil), nil
	case dialect.Postgres:
		return (*postgres)(nil), nil
	default:
		return nil, fmt.Errorf("sqljson: unknown driver %q", name)
	}
}

type when struct{ Cond, Then, Else func(*sql.Builder) }

// setCase sets the column value using the "CASE WHEN" statement.
// The x defines the condition/predicate, t is the true (if) case,
// and 'f' defines the false (else).
func setCase(u *sql.UpdateBuilder, column string, w when) {
	u.Set(column, sql.ExprFunc(func(b *sql.Builder) {
		b.WriteString("CASE WHEN ").Wrap(func(b *sql.Builder) {
			w.Cond(b)
		})
		b.WriteString(" THEN ")
		w.Then(b)
		b.WriteString(" ELSE ")
		w.Else(b)
		b.WriteString(" END")
	}))
}

func isPrimitive(v any) bool {
	switch reflect.TypeOf(v).Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.Struct, reflect.Ptr, reflect.Interface:
		return false
	}
	return true
}
