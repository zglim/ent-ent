// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/schema/field"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
)

// Postgres adapter for Atlas migration engine.
type Postgres struct {
	dialect.Driver
	schema  string
	version string
}

// init loads the Postgres version from the database for later use in the migration process.
// It returns an error if the server version is lower than v10.
func (d *Postgres) init(ctx context.Context) error {
	if d.version != "" {
		return nil // already initialized.
	}
	rows := &sql.Rows{}
	if err := d.Query(ctx, "SHOW server_version_num", []any{}, rows); err != nil {
		return fmt.Errorf("querying server version %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("server_version_num variable was not found")
	}
	var version string
	if err := rows.Scan(&version); err != nil {
		return fmt.Errorf("scanning version: %w", err)
	}
	if len(version) < 6 {
		return fmt.Errorf("malformed version: %s", version)
	}
	d.version = fmt.Sprintf("%s.%s.%s", version[:2], version[2:4], version[4:])
	if compareVersions(d.version, "10.0.0") == -1 {
		return fmt.Errorf("unsupported postgres version: %s", d.version)
	}
	return nil
}

// tableExist checks if a table exists in the database and current schema.
func (d *Postgres) tableExist(ctx context.Context, conn dialect.ExecQuerier, name string) (bool, error) {
	query, args := sql.Dialect(dialect.Postgres).
		Select(sql.Count("*")).From(sql.Table("tables").Schema("information_schema")).
		Where(sql.And(
			d.matchSchema(),
			sql.EQ("table_name", name),
		)).Query()
	return exist(ctx, conn, query, args...)
}

// matchSchema returns the predicate for matching table schema.
func (d *Postgres) matchSchema(columns ...string) *sql.Predicate {
	column := "table_schema"
	if len(columns) > 0 {
		column = columns[0]
	}
	if d.schema != "" {
		return sql.EQ(column, d.schema)
	}
	return sql.EQ(column, sql.Raw("CURRENT_SCHEMA()"))
}

// maxCharSize defines the maximum size of limited character types in Postgres (10 MB).
const maxCharSize = 10 << 20

func (d *Postgres) atOpen(conn dialect.ExecQuerier) (migrate.Driver, error) {
	return postgres.Open(&db{ExecQuerier: conn})
}

func (d *Postgres) atTable(t1 *Table, t2 *schema.Table) {
	if t1.Annotation != nil {
		setAtChecks(t1, t2)
	}
}

func (d *Postgres) supportsDefault(*Column) bool {
	// PostgreSQL supports default values for all standard types.
	return true
}

func (d *Postgres) atTypeC(c1 *Column, c2 *schema.Column) error {
	if c1.SchemaType != nil && c1.SchemaType[dialect.Postgres] != "" {
		t, err := postgres.ParseType(strings.ToLower(c1.SchemaType[dialect.Postgres]))
		if err != nil {
			return err
		}
		c2.Type.Type = t
		if s, ok := t.(*postgres.SerialType); c1.foreign != nil && ok {
			c2.Type.Type = s.IntegerType()
		}
		return nil
	}
	var t schema.Type
	switch c1.Type {
	case field.TypeBool:
		t = &schema.BoolType{T: postgres.TypeBoolean}
	case field.TypeUint8, field.TypeInt8, field.TypeInt16:
		t = &schema.IntegerType{T: postgres.TypeSmallInt}
	case field.TypeUint16, field.TypeInt32:
		t = &schema.IntegerType{T: postgres.TypeInt}
	case field.TypeUint32, field.TypeInt, field.TypeUint, field.TypeInt64, field.TypeUint64:
		t = &schema.IntegerType{T: postgres.TypeBigInt}
	case field.TypeFloat32:
		t = &schema.FloatType{T: c1.scanTypeOr(postgres.TypeReal)}
	case field.TypeFloat64:
		t = &schema.FloatType{T: c1.scanTypeOr(postgres.TypeDouble)}
	case field.TypeBytes:
		t = &schema.BinaryType{T: postgres.TypeBytea}
	case field.TypeUUID:
		t = &postgres.UUIDType{T: postgres.TypeUUID}
	case field.TypeJSON:
		t = &schema.JSONType{T: postgres.TypeJSONB}
	case field.TypeString:
		t = &schema.StringType{T: postgres.TypeVarChar}
		if c1.Size > maxCharSize {
			t = &schema.StringType{T: postgres.TypeText}
		}
	case field.TypeTime:
		t = &schema.TimeType{T: c1.scanTypeOr(postgres.TypeTimestampWTZ)}
	case field.TypeEnum:
		// Although atlas supports enum types, we keep backwards compatibility
		// with previous versions of ent and use varchar (see cType).
		t = &schema.StringType{T: postgres.TypeVarChar}
	case field.TypeOther:
		t = &schema.UnsupportedType{T: c1.typ}
	default:
		t, err := postgres.ParseType(strings.ToLower(c1.typ))
		if err != nil {
			return err
		}
		c2.Type.Type = t
	}
	c2.Type.Type = t
	return nil
}

func (d *Postgres) atUniqueC(t1 *Table, c1 *Column, t2 *schema.Table, c2 *schema.Column) {
	// For UNIQUE columns, PostgreSQL creates an implicit index named
	// "<table>_<column>_key<i>".
	name := fmt.Sprintf("%s_%s_key", t1.Name, c1.Name)
	atImplicitUniqueIndex(t1, t2, c2, name, func(idx *Index) bool {
		return atImplicitIndexName(idx.Name, name, name, 0)
	})
}

func (d *Postgres) atIncrementC(t *schema.Table, c *schema.Column) {
	// Skip marking this column as an identity in case it is
	// serial type or a default was already defined for it.
	if _, ok := c.Type.Type.(*postgres.SerialType); ok || c.Default != nil {
		t.Attrs = removeAttr(t.Attrs, reflect.TypeOf(&postgres.Identity{}))
		return
	}
	id := &postgres.Identity{}
	for _, a := range t.Attrs {
		if a, ok := a.(*postgres.Identity); ok {
			id = a
		}
	}
	c.AddAttrs(id)
}

func (d *Postgres) atIncrementT(t *schema.Table, v int64) {
	t.AddAttrs(&postgres.Identity{Sequence: &postgres.Sequence{Start: v}})
}

// indexOpClass returns a map holding the operator-class mapping if exists.
func indexOpClass(idx *Index) map[string]string {
	opc := make(map[string]string)
	if idx.Annotation == nil {
		return opc
	}
	// If operator-class (without a name) was defined on
	// the annotation, map it to the single column index.
	if idx.Annotation.OpClass != "" && len(idx.Columns) == 1 {
		opc[idx.Columns[0].Name] = idx.Annotation.OpClass
	}
	for column, op := range idx.Annotation.OpClassColumns {
		opc[column] = op
	}
	return opc
}

func (d *Postgres) atIndex(idx1 *Index, t2 *schema.Table, idx2 *schema.Index) error {
	opc := indexOpClass(idx1)
	if err := atIndexParts(idx1, t2, idx2, func(c1 *Column, part *schema.IndexPart) error {
		v, ok := opc[c1.Name]
		if !ok {
			return nil
		}
		var op postgres.IndexOpClass
		if err := op.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("unmarshalling operator-class %q for column %q: %v", v, c1.Name, err)
		}
		part.Attrs = append(part.Attrs, &op)
		return nil
	}); err != nil {
		return err
	}
	if t, ok := indexType(idx1, dialect.Postgres); ok {
		idx2.AddAttrs(&postgres.IndexType{T: t})
	}
	if ant, supportsInclude := idx1.Annotation, compareVersions(d.version, "11.0.0") >= 0; ant != nil && len(ant.IncludeColumns) > 0 && supportsInclude {
		columns := make([]*schema.Column, len(ant.IncludeColumns))
		for i, ic := range ant.IncludeColumns {
			c, ok := t2.Column(ic)
			if !ok {
				return fmt.Errorf("include column %q was not found for index %q", ic, idx1.Name)
			}
			columns[i] = c
		}
		idx2.AddAttrs(&postgres.IndexInclude{Columns: columns})
	}
	if idx1.Annotation != nil && idx1.Annotation.Where != "" {
		idx2.AddAttrs(&postgres.IndexPredicate{P: idx1.Annotation.Where})
	}
	return nil
}

func (*Postgres) atTypeRangeSQL(ts ...string) string {
	for i := range ts {
		ts[i] = fmt.Sprintf("('%s')", ts[i])
	}
	return fmt.Sprintf(`INSERT INTO "%s" ("type") VALUES %s`, TypeTable, strings.Join(ts, ", "))
}
