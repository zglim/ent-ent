// Copyright 2019-present Facebook Inc. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package load

import (
	"encoding/json"
	"fmt"
	"reflect"

	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Schema represents an ent.Schema that was loaded from a complied user package.
type Schema struct {
	Name         string         `json:"name,omitempty"`
	Pos          string         `json:"-"`
	View         bool           `json:"view,omitempty"`
	Config       ent.Config     `json:"config,omitempty"`
	Edges        []*Edge        `json:"edges,omitempty"`
	Fields       []*Field       `json:"fields,omitempty"`
	Indexes      []*Index       `json:"indexes,omitempty"`
	Hooks        []*Position    `json:"hooks,omitempty"`
	Interceptors []*Position    `json:"interceptors,omitempty"`
	Policy       []*Position    `json:"policy,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
}

// Position describes a position in the schema.
type Position struct {
	Index      int  // Index in the field/hook list.
	MixedIn    bool // Indicates if the schema object was mixed-in.
	MixinIndex int  // Mixin index in the mixin list.
}

// Field represents an ent.Field that was loaded from a complied user package.
type Field struct {
	Name             string                  `json:"name,omitempty"`
	Info             *field.TypeInfo         `json:"type,omitempty"`
	ValueScanner     bool                    `json:"value_scanner,omitempty"`
	Tag              string                  `json:"tag,omitempty"`
	Size             *int64                  `json:"size,omitempty"`
	Enums            []struct{ N, V string } `json:"enums,omitempty"`
	Unique           bool                    `json:"unique,omitempty"`
	Nillable         bool                    `json:"nillable,omitempty"`
	Optional         bool                    `json:"optional,omitempty"`
	Default          bool                    `json:"default,omitempty"`
	DefaultValue     any                     `json:"default_value,omitempty"`
	DefaultKind      reflect.Kind            `json:"default_kind,omitempty"`
	UpdateDefault    bool                    `json:"update_default,omitempty"`
	Immutable        bool                    `json:"immutable,omitempty"`
	Validators       int                     `json:"validators,omitempty"`
	StorageKey       string                  `json:"storage_key,omitempty"`
	Position         *Position               `json:"position,omitempty"`
	Sensitive        bool                    `json:"sensitive,omitempty"`
	SchemaType       map[string]string       `json:"schema_type,omitempty"`
	Annotations      map[string]any          `json:"annotations,omitempty"`
	Comment          string                  `json:"comment,omitempty"`
	Deprecated       bool                    `json:"deprecated,omitempty"`
	DeprecatedReason string                  `json:"deprecated_reason,omitempty"`
}

// Edge represents an ent.Edge that was loaded from a complied user package.
type Edge struct {
	Name        string                 `json:"name,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Tag         string                 `json:"tag,omitempty"`
	Field       string                 `json:"field,omitempty"`
	RefName     string                 `json:"ref_name,omitempty"`
	Ref         *Edge                  `json:"ref,omitempty"`
	Through     *struct{ N, T string } `json:"through,omitempty"`
	Unique      bool                   `json:"unique,omitempty"`
	Inverse     bool                   `json:"inverse,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Immutable   bool                   `json:"immutable,omitempty"`
	StorageKey  *edge.StorageKey       `json:"storage_key,omitempty"`
	Annotations map[string]any         `json:"annotations,omitempty"`
	Comment     string                 `json:"comment,omitempty"`
}

// Index represents an ent.Index that was loaded from a complied user package.
type Index struct {
	Unique      bool           `json:"unique,omitempty"`
	Edges       []string       `json:"edges,omitempty"`
	Fields      []string       `json:"fields,omitempty"`
	StorageKey  string         `json:"storage_key,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// NewEdge creates an loaded edge from edge descriptor.
func NewEdge(ed *edge.Descriptor) *Edge {
	ne := &Edge{
		Tag:         ed.Tag,
		Type:        ed.Type,
		Name:        ed.Name,
		Field:       ed.Field,
		Unique:      ed.Unique,
		Inverse:     ed.Inverse,
		Required:    ed.Required,
		Immutable:   ed.Immutable,
		RefName:     ed.RefName,
		Through:     ed.Through,
		StorageKey:  ed.StorageKey,
		Comment:     ed.Comment,
		Annotations: make(map[string]any),
	}
	for _, at := range ed.Annotations {
		ne.addAnnotation(at)
	}
	if ref := ed.Ref; ref != nil {
		ne.Ref = NewEdge(ref)
		ne.StorageKey = ne.Ref.StorageKey
	}
	return ne
}

// NewField creates a loaded field from field descriptor.
func NewField(fd *field.Descriptor) (*Field, error) {
	if fd.Err != nil {
		return nil, fmt.Errorf("field %q: %v", fd.Name, fd.Err)
	}
	sf := &Field{
		Name:             fd.Name,
		Info:             fd.Info,
		ValueScanner:     fd.ValueScanner != nil,
		Tag:              fd.Tag,
		Enums:            fd.Enums,
		Unique:           fd.Unique,
		Nillable:         fd.Nillable,
		Optional:         fd.Optional,
		Default:          fd.Default != nil,
		UpdateDefault:    fd.UpdateDefault != nil,
		Immutable:        fd.Immutable,
		StorageKey:       fd.StorageKey,
		Validators:       len(fd.Validators),
		Sensitive:        fd.Sensitive,
		SchemaType:       fd.SchemaType,
		Annotations:      make(map[string]any),
		Comment:          fd.Comment,
		Deprecated:       fd.Deprecated,
		DeprecatedReason: fd.DeprecatedReason,
	}
	for _, at := range fd.Annotations {
		sf.addAnnotation(at)
	}
	if sf.Info == nil {
		return nil, fmt.Errorf("missing type info for field %q", sf.Name)
	}
	if size := int64(fd.Size); size != 0 {
		sf.Size = &size
	}
	if sf.Default {
		sf.DefaultKind = reflect.TypeOf(fd.Default).Kind()
	}
	// If the default value can be encoded to the generator.
	// For example, not a function like time.Now.
	if _, err := json.Marshal(fd.Default); err == nil {
		sf.DefaultValue = fd.Default
	}
	return sf, nil
}

// NewIndex creates an loaded index from index descriptor.
func NewIndex(idx *index.Descriptor) *Index {
	ni := &Index{
		Edges:       idx.Edges,
		Fields:      idx.Fields,
		Unique:      idx.Unique,
		StorageKey:  idx.StorageKey,
		Annotations: make(map[string]any),
	}
	for _, at := range idx.Annotations {
		ni.addAnnotation(at)
	}
	return ni
}

// MarshalSchema encodes the ent.Schema interface into a JSON
// that can be decoded into the Schema objects declared above.
func MarshalSchema(schema ent.Interface) (b []byte, err error) {
	s := &Schema{
		Config:      schema.Config(),
		Name:        indirect(reflect.TypeOf(schema)).Name(),
		Annotations: make(map[string]any),
	}
	_, s.View = schema.(ent.Viewer)
	if err := s.loadMixin(schema); err != nil {
		return nil, fmt.Errorf("schema %q: %w", s.Name, err)
	}
	// Schema annotations override mixed-in annotations.
	for _, at := range schema.Annotations() {
		if e, ok := at.(interface{ Err() error }); ok && e.Err() != nil {
			return nil, fmt.Errorf("schema %q: %w", s.Name, e.Err())
		}
		s.addAnnotation(at)
	}
	if err := s.loadObjects(schema, -1); err != nil {
		return nil, fmt.Errorf("schema %q: %w", s.Name, err)
	}
	return json.Marshal(s)
}

// UnmarshalSchema decodes the given buffer to a loaded schema.
func UnmarshalSchema(buf []byte) (*Schema, error) {
	s := &Schema{}
	if err := json.Unmarshal(buf, s); err != nil {
		return nil, err
	}
	for _, f := range s.Fields {
		if err := f.defaults(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// loadMixin loads mixin to schema from ent.Interface.
func (s *Schema) loadMixin(schema ent.Interface) error {
	mixin, err := safeMixin(schema)
	if err != nil {
		return err
	}
	for i, mx := range mixin {
		if err := s.loadObjects(mx, i); err != nil {
			return fmt.Errorf("mixin %q: %w", indirect(reflect.TypeOf(mx)).Name(), err)
		}
		for _, at := range mx.Annotations() {
			s.addAnnotation(at)
		}
	}
	return nil
}

// schemaObjects is the common set of object accessors implemented by both
// ent.Interface and ent.Mixin. It lets loadObjects collect them through a
// single code path regardless of the source.
type schemaObjects interface {
	Fields() []ent.Field
	Edges() []ent.Edge
	Indexes() []ent.Index
	Hooks() []ent.Hook
	Interceptors() []ent.Interceptor
	Policy() ent.Policy
}

// loadObjects collects the fields, edges, indexes, hooks, interceptors and policy
// exposed by src into the schema. mixinIndex is the index of the mixin src belongs
// to, or -1 when src is the schema itself.
func (s *Schema) loadObjects(src schemaObjects, mixinIndex int) error {
	fields, err := safeFields(src)
	if err != nil {
		return err
	}
	for i, f := range fields {
		sf, err := NewField(f.Descriptor())
		if err != nil {
			return err
		}
		sf.Position = position(i, mixinIndex)
		s.Fields = append(s.Fields, sf)
	}
	edges, err := safeEdges(src)
	if err != nil {
		return err
	}
	for _, e := range edges {
		s.Edges = append(s.Edges, NewEdge(e.Descriptor()))
	}
	indexes, err := safeIndexes(src)
	if err != nil {
		return err
	}
	for _, idx := range indexes {
		s.Indexes = append(s.Indexes, NewIndex(idx.Descriptor()))
	}
	hooks, err := safeHooks(src)
	if err != nil {
		return err
	}
	for i := range hooks {
		s.Hooks = append(s.Hooks, position(i, mixinIndex))
	}
	inters, err := safeInterceptors(src)
	if err != nil {
		return err
	}
	for i := range inters {
		s.Interceptors = append(s.Interceptors, position(i, mixinIndex))
	}
	policy, err := safePolicy(src)
	if err != nil {
		return err
	}
	if policy != nil {
		s.Policy = append(s.Policy, position(0, mixinIndex))
	}
	return nil
}

// position returns the Position of a schema object found at the given index.
// A non-negative mixinIndex marks the object as mixed-in from that mixin.
func position(index, mixinIndex int) *Position {
	p := &Position{Index: index}
	if mixinIndex >= 0 {
		p.MixedIn = true
		p.MixinIndex = mixinIndex
	}
	return p
}

func (s *Schema) addAnnotation(an schema.Annotation) {
	curr, ok := s.Annotations[an.Name()]
	if !ok {
		s.Annotations[an.Name()] = an
		return
	}
	if m, ok := curr.(schema.Merger); ok {
		s.Annotations[an.Name()] = m.Merge(an)
	}
}

func (e *Edge) addAnnotation(an schema.Annotation) {
	addAnnotation(e.Annotations, an)
}

func (i *Index) addAnnotation(an schema.Annotation) {
	addAnnotation(i.Annotations, an)
}

func (f *Field) addAnnotation(an schema.Annotation) {
	addAnnotation(f.Annotations, an)
}

func addAnnotation(annotations map[string]any, an schema.Annotation) {
	curr, ok := annotations[an.Name()]
	if !ok {
		annotations[an.Name()] = an
		return
	}
	if m, ok := curr.(schema.Merger); ok {
		annotations[an.Name()] = m.Merge(an)
	}
}

func (f *Field) defaults() error {
	if !f.Default || !f.Info.Numeric() || f.DefaultKind == reflect.Func {
		return nil
	}
	n, ok := f.DefaultValue.(float64)
	if !ok {
		return fmt.Errorf("unexpected default value type for field: %q", f.Name)
	}
	switch t := f.Info.Type; {
	case t >= field.TypeInt8 && t <= field.TypeInt64:
		f.DefaultValue = int64(n)
	case t >= field.TypeUint8 && t <= field.TypeUint64:
		f.DefaultValue = uint64(n)
	}
	return nil
}

// safeCall invokes fn and recovers from a panic, converting it into an error
// labeled with name. It centralizes the panic protection shared by the safe*
// accessors below so marshaling never crashes on a faulty user schema.
func safeCall[T any](name string, fn func() T) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panics: %v", name, r)
			var zero T
			v = zero
		}
	}()
	return fn(), nil
}

// safeFields wraps the schema.Fields and mixin.Fields method with recover to ensure no panics in marshaling.
func safeFields(fd interface{ Fields() []ent.Field }) ([]ent.Field, error) {
	return safeCall(fmt.Sprintf("%T.Fields", fd), fd.Fields)
}

// safeEdges wraps the schema.Edges method with recover to ensure no panics in marshaling.
func safeEdges(schema interface{ Edges() []ent.Edge }) ([]ent.Edge, error) {
	return safeCall("schema.Edges", schema.Edges)
}

// safeIndexes wraps the schema.Indexes method with recover to ensure no panics in marshaling.
func safeIndexes(schema interface{ Indexes() []ent.Index }) ([]ent.Index, error) {
	return safeCall("schema.Indexes", schema.Indexes)
}

// safeMixin wraps the schema.Mixin method with recover to ensure no panics in marshaling.
func safeMixin(schema ent.Interface) ([]ent.Mixin, error) {
	return safeCall("schema.Mixin", schema.Mixin)
}

// safeHooks wraps the schema.Hooks method with recover to ensure no panics in marshaling.
func safeHooks(schema interface{ Hooks() []ent.Hook }) ([]ent.Hook, error) {
	return safeCall("schema.Hooks", schema.Hooks)
}

// safeInterceptors wraps the schema.Interceptors method with recover to ensure no panics in marshaling.
func safeInterceptors(schema interface{ Interceptors() []ent.Interceptor }) ([]ent.Interceptor, error) {
	return safeCall("schema.Interceptors", schema.Interceptors)
}

// safePolicy wraps the schema.Policy method with recover to ensure no panics in marshaling.
func safePolicy(schema interface{ Policy() ent.Policy }) (ent.Policy, error) {
	return safeCall("schema.Policy", schema.Policy)
}

func indirect(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}
