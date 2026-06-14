// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package schema

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"ariga.io/atlas/sql/schema"
)

// atUniqueColumn handles the common pattern of adding an implicit unique index
// for a UNIQUE column. It checks whether an explicit index already covers the
// column (via isImplicit) and, if not, adds a unique index with the given name
// to the atlas table.
//
// Each dialect calls this from its atUniqueC implementation, providing its own
// implicit-index name matcher and the desired index name.
func atUniqueColumn(t1 *Table, c1 *Column, t2 *schema.Table, c2 *schema.Column, indexName string, isImplicit func(*Index, *Table, *Column) bool) {
	for _, idx := range t1.Indexes {
		// Index also defined explicitly, and will be added in atIndexes.
		if idx.Unique && isImplicit(idx, t1, c1) {
			return
		}
	}
	t2.AddIndexes(schema.NewUniqueIndex(indexName).AddColumns(c2))
}

// atImplicitIndexSuffix checks whether idxName matches the pattern formed by
// prefix followed by a numeric suffix >= minSuffix. An exact match on prefix
// alone (empty suffix) is NOT considered valid; the caller should handle
// exact-match checks separately if needed.
//
// This is used by dialect-specific atImplicitIndexName implementations to
// match patterns like:
//
//	PostgreSQL: "<table>_<col>_key2", ...  (prefix="<table>_<col>_key", minSuffix=1)
//	MySQL:      "<col>_2", "<col>_3", ...  (prefix="<col>_",           minSuffix=2)
//	SQLite:     "sqlite_autoindex_<table>_1", ... (prefix="sqlite_autoindex_<table>_", minSuffix=1)
func atImplicitIndexSuffix(idxName, prefix string, minSuffix int64) bool {
	if !strings.HasPrefix(idxName, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(idxName, prefix)
	if suffix == "" {
		return false // exact match on prefix; caller handles this separately
	}
	i, err := strconv.ParseInt(suffix, 10, 64)
	return err == nil && i >= minSuffix
}

// atIncrementColumn handles the common pattern for auto-increment columns:
// if the column has a default value, the table-level auto-increment attribute
// is removed (the default takes precedence); otherwise, the column-level
// auto-increment attribute is added.
//
// removeType is the reflect.Type of the dialect-specific table-level attribute
// to remove (e.g. reflect.TypeOf(&mysql.AutoIncrement{})).
// addAttr is the dialect-specific column-level attribute to add
// (e.g. &mysql.AutoIncrement{}).
func atIncrementColumn(t *schema.Table, c *schema.Column, hasDefault bool, removeType reflect.Type, addAttr schema.Attr) {
	if hasDefault {
		t.Attrs = removeAttr(t.Attrs, removeType)
	} else {
		c.AddAttrs(addAttr)
	}
}

// atIndexParts builds atlas IndexPart entries for each column in the given
// ent index and adds them to idx2. The partAttrs callback is invoked for
// each column pair to return any dialect-specific attributes to attach to
// the IndexPart (e.g. MySQL SubPart prefix lengths, PostgreSQL operator
// classes). If partAttrs is nil, plain parts without extra attributes are
// created.
func atIndexParts(idx1 *Index, t2 *schema.Table, idx2 *schema.Index, partAttrs func(*Column, *schema.Column) ([]schema.Attr, error)) error {
	for _, c1 := range idx1.Columns {
		c2, ok := t2.Column(c1.Name)
		if !ok {
			return fmt.Errorf("unexpected index %q column: %q", idx1.Name, c1.Name)
		}
		part := &schema.IndexPart{C: c2}
		if partAttrs != nil {
			attrs, err := partAttrs(c1, c2)
			if err != nil {
				return err
			}
			part.Attrs = append(part.Attrs, attrs...)
		}
		idx2.AddParts(part)
	}
	return nil
}
