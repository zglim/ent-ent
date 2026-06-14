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

type sqlite struct{}

// Append implements the driver.Append method.
func (sqlite) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	appendCase(u, column, when{
		Cond: jsonNullCheck(column, opts, func(b *sql.Builder, col string, opts []Option) {
			b.WriteString("JSON_TYPE").Wrap(func(b *sql.Builder) {
				b.Ident(col).Comma()
				identPath(col, opts...).mysqlPath(b)
			})
		}, "'null'"),
		Then: func(b *sql.Builder) {
			if len(opts) > 0 {
				jsonSetWithArg(b, column, opts, "JSON(?)", marshalArg(elems))
			} else {
				b.Arg(marshalArg(elems))
			}
		},
		Else: func(b *sql.Builder) {
			b.WriteString("JSON_INSERT").Wrap(func(b *sql.Builder) {
				b.Ident(column).Comma()
				for i, e := range elems {
					if i > 0 {
						b.Comma()
					}
					appendIndexPath(column, opts, b)
					b.Comma()
					writeArg(b, e, "JSON(?)")
				}
			})
		},
	})
}

type mysql struct{}

// Append implements the driver.Append method.
func (mysql) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	appendCase(u, column, when{
		Cond: jsonNullCheck(column, opts, func(b *sql.Builder, col string, opts []Option) {
			b.WriteString("JSON_TYPE").Wrap(func(b *sql.Builder) {
				b.WriteString("JSON_EXTRACT").Wrap(func(b *sql.Builder) {
					b.Ident(col).Comma()
					identPath(col, opts...).mysqlPath(b)
				})
			})
		}, "'NULL'"),
		Then: func(b *sql.Builder) {
			if len(opts) > 0 {
				b.WriteString("JSON_SET").Wrap(func(b *sql.Builder) {
					b.Ident(column).Comma()
					identPath(column, opts...).mysqlPath(b)
					b.Comma()
					jsonArrayArgs(b, elems)
				})
			} else {
				jsonArrayArgs(b, elems)
			}
		},
		Else: func(b *sql.Builder) {
			b.WriteString("JSON_ARRAY_APPEND").Wrap(func(b *sql.Builder) {
				b.Ident(column).Comma()
				for i, e := range elems {
					if i > 0 {
						b.Comma()
					}
					appendPath(column, opts, b)
					b.Comma()
					writeArg(b, e, "CAST(? AS JSON)")
				}
			})
		},
	})
}

type postgres struct{}

// Append implements the driver.Append method.
func (postgres) Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option) {
	setCase(u, column, when{
		Cond: func(b *sql.Builder) {
			valuePath(b, column, append(opts, Cast("jsonb"))...)
			b.WriteOp(sql.OpIsNull)
			b.WriteString(" OR ")
			valuePath(b, column, append(opts, Cast("jsonb"))...)
			b.WriteOp(sql.OpEQ).WriteString("'null'::jsonb")
		},
		Then: func(b *sql.Builder) {
			if len(opts) > 0 {
				b.WriteString("jsonb_set").Wrap(func(b *sql.Builder) {
					b.Ident(column).Comma()
					identPath(column, opts...).pgArrayPath(b)
					b.Comma().Arg(marshalArg(elems))
					b.Comma().WriteString("true")
				})
			} else {
				b.Arg(marshalArg(elems))
			}
		},
		Else: func(b *sql.Builder) {
			if len(opts) > 0 {
				b.WriteString("jsonb_set").Wrap(func(b *sql.Builder) {
					b.Ident(column).Comma()
					identPath(column, opts...).pgArrayPath(b)
					b.Comma()
					path := identPath(column, opts...)
					path.value(b)
					b.WriteString(" || ").Arg(marshalArg(elems))
					b.Comma().WriteString("true")
				})
			} else {
				b.Ident(column).WriteString(" || ").Arg(marshalArg(elems))
			}
		},
	})
}

// driver groups all dialect-specific methods.
type driver interface {
	Append(u *sql.UpdateBuilder, column string, elems []any, opts ...Option)
}

func newDriver(name string) (driver, error) {
	switch name {
	case dialect.SQLite:
		return sqlite{}, nil
	case dialect.MySQL:
		return mysql{}, nil
	case dialect.Postgres:
		return postgres{}, nil
	default:
		return nil, fmt.Errorf("sqljson: unknown driver %q", name)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers for the CASE WHEN / SET pattern used by Append.
// ---------------------------------------------------------------------------

type when struct{ Cond, Then, Else func(*sql.Builder) }

// setCase sets the column value using the "CASE WHEN" statement.
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

// appendCase is like setCase but specialised for the JSON-append pattern that
// is shared between SQLite and MySQL: both test whether the target is SQL NULL
// or JSON null, and both branch into a "set" (then) vs "append" (else) path.
// PostgreSQL keeps its own setCase call because its condition and branch
// expressions differ too much to fit this helper.
func appendCase(u *sql.UpdateBuilder, column string, w when) {
	setCase(u, column, w)
}

// jsonNullCheck returns a builder func that writes:
//
//	<typeExpr> IS NULL OR <typeExpr> = <nullLit>
//
// The typeExpr callback receives the column name and options so each dialect
// can emit its own JSON-type expression (e.g. JSON_TYPE(col, '$') for SQLite
// vs JSON_TYPE(JSON_EXTRACT(col, '$')) for MySQL).
func jsonNullCheck(column string, opts []Option, typeExpr func(*sql.Builder, string, []Option), nullLit string) func(*sql.Builder) {
	return func(b *sql.Builder) {
		typeExpr(b, column, opts)
		b.WriteOp(sql.OpIsNull)
		b.WriteString(" OR ")
		typeExpr(b, column, opts)
		b.WriteOp(sql.OpEQ).WriteString(nullLit)
	}
}

// jsonArrayArgs writes JSON_ARRAY(?, ?, ...) with one placeholder per element.
// Non-primitive elements are JSON-marshalled before being passed as arguments.
func jsonArrayArgs(b *sql.Builder, elems []any) {
	b.WriteString("JSON_ARRAY(")
	args := make([]any, len(elems))
	for i, e := range elems {
		if !isPrimitive(e) {
			args[i] = marshalArg(e)
		} else {
			args[i] = e
		}
	}
	b.Args(args...)
	b.WriteByte(')')
}

// jsonSetWithArg writes JSON_SET(col, path, <fmt>) where the argument value
// is formatted according to argFmt (e.g. "JSON(?)" for SQLite).
func jsonSetWithArg(b *sql.Builder, column string, opts []Option, argFmt string, arg any) {
	b.WriteString("JSON_SET").Wrap(func(b *sql.Builder) {
		b.Ident(column).Comma()
		identPath(column, opts...).mysqlPath(b)
		b.Comma().Argf(argFmt, arg)
	})
}

// appendPath writes the JSON path used for append operations.
// With options it emits the column's mysql-style path (e.g. '$.a');
// without options it emits '$' (top-level).
func appendPath(column string, opts []Option, b *sql.Builder) {
	identPath(column, opts...).mysqlPath(b)
}

// appendIndexPath writes the JSON path used for element-wise append.
// With options it appends "[#]" to the column's path (e.g. '$.a[#]');
// without options it emits '$[#]' (top-level array append).
func appendIndexPath(column string, opts []Option, b *sql.Builder) {
	if len(opts) > 0 {
		p := identPath(column, opts...)
		p.Path = append(p.Path, "[#]")
		p.mysqlPath(b)
	} else {
		b.WriteString("'$[#]'")
	}
}

// writeArg writes a single append argument. Non-primitive values are
// JSON-marshalled and formatted with the given format string (e.g.
// "JSON(?)" for SQLite, "CAST(? AS JSON)" for MySQL). Primitive values
// are written as plain arguments.
func writeArg(b *sql.Builder, v any, nonPrimitiveFmt string) {
	if !isPrimitive(v) {
		b.Argf(nonPrimitiveFmt, marshalArg(v))
	} else {
		b.Arg(v)
	}
}

// ---------------------------------------------------------------------------
// General-purpose helpers.
// ---------------------------------------------------------------------------

func isPrimitive(v any) bool {
	switch reflect.TypeOf(v).Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.Struct, reflect.Ptr, reflect.Interface:
		return false
	}
	return true
}
