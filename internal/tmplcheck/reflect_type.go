// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"fmt"
	"reflect"
	"strings"
)

type (
	// Type models a static type during template AST validation.
	// A nil Type represents an unconstrained dynamic value (such as any).
	Type interface {
		// ID returns a canonical comparable identity for cycle detection in
		// recursive named template invocations, or "" if the type is dynamic.
		ID() string

		// Member resolves a dot field, method, or map key access (.Name) on this type.
		// It returns the member's Type (or nil for an unconstrained dynamic value)
		// and an error if this type cannot have member name.
		Member(name string) (Type, error)

		// Index resolves an index operation (index x k) on this type.
		// It returns the element Type (or nil for dynamic) and an error if
		// this type cannot be indexed.
		Index() (Type, error)

		// Range resolves a range operation (range x) on this type.
		// It returns the key/index Type, the element Type, and an error if
		// this type cannot be ranged over.
		Range() (key Type, elem Type, err error)
	}

	// reflectType wraps a Go reflect.Type to implement Type with addressability tracking.
	reflectType struct {
		t           reflect.Type
		addressable bool
	}

	// nilDataType represents untyped nil data passed to a named template
	// when invoked without a data pipeline (e.g. {{template "name"}}).
	nilDataType struct{}

	// multiType represents a union of types from divergent branches.
	multiType struct {
		types []Type
	}
)

const dynamicTypeID = "<dynamic>"

var errorType = reflect.TypeFor[error]()

// ReflectType returns a Type implementation wrapping the Go reflect.Type.
// By default, standalone values (root data, function returns) are unaddressable.
// If t is nil, or if dereferencing t results in nil or interface{}, ReflectType returns nil.
func ReflectType(t reflect.Type) Type {
	return newReflectType(t, false)
}

// ID returns a canonical comparable identity for cycle detection in recursive
// named template invocations.
func (r *reflectType) ID() string {
	return fmt.Sprintf("reflect:%p:addr=%t", r.t, r.addressable)
}

// Member resolves a dot field, method, or map key access (.Name) on this type.
func (r *reflectType) Member(name string) (Type, error) {
	// First check direct methods on r.t (e.g. pointer receiver methods when r.t is a pointer).
	if m, ok := r.t.MethodByName(name); ok {
		if out, valid := validMethodReturn(m.Type); valid {
			return ReflectType(out), nil
		}
		return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, r.t)
	}

	cur, isAddr := derefWithAddr(r.t, r.addressable)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Structs and maps have named members; other types checked for methods.
	case reflect.Struct:
		if sf, ok := cur.FieldByName(name); ok && sf.IsExported() {
			// In Go text/template, accessing a struct field of func type returns
			// the func value without invoking it; only the 'call' builtin invokes it.
			return newReflectType(sf.Type, isAddr), nil
		}
		if m, ok := cur.MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return ReflectType(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, cur)
		}
		if isAddr {
			if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
				if out, valid := validMethodReturn(m.Type); valid {
					return ReflectType(out), nil
				}
				return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, reflect.PointerTo(cur))
			}
		}
		return nil, fmt.Errorf("field %q not declared in type %s", name, cur)

	case reflect.Map:
		if reflect.TypeFor[string]().AssignableTo(cur.Key()) {
			return ReflectType(cur.Elem()), nil
		}
		return nil, fmt.Errorf("field %q not declared in type %s", name, cur)

	default:
		if m, ok := cur.MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return ReflectType(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, cur)
		}
		if isAddr {
			if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
				if out, valid := validMethodReturn(m.Type); valid {
					return ReflectType(out), nil
				}
				return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, reflect.PointerTo(cur))
			}
		}
		return nil, fmt.Errorf("field %q not declared in type %s", name, cur)
	}
}

// Index resolves an index operation (index x k) on this type.
func (r *reflectType) Index() (Type, error) {
	cur, isAddr := derefWithAddr(r.t, r.addressable)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Only slice, array, map, and string support indexing.
	case reflect.Slice:
		return newReflectType(cur.Elem(), true), nil
	case reflect.Array:
		return newReflectType(cur.Elem(), isAddr), nil
	case reflect.Map:
		return ReflectType(cur.Elem()), nil
	case reflect.String:
		return ReflectType(reflect.TypeFor[byte]()), nil
	default:
		return nil, fmt.Errorf("cannot index type %s", cur)
	}
}

// Range resolves a range operation (range x) on this type.
func (r *reflectType) Range() (Type, Type, error) {
	cur, isAddr := derefWithAddr(r.t, r.addressable)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Only collection kinds and integers support range.
	case reflect.Slice:
		return ReflectType(reflect.TypeFor[int]()), newReflectType(cur.Elem(), true), nil
	case reflect.Array:
		return ReflectType(reflect.TypeFor[int]()), newReflectType(cur.Elem(), isAddr), nil
	case reflect.Map:
		return ReflectType(cur.Key()), ReflectType(cur.Elem()), nil
	case reflect.Chan:
		return nil, ReflectType(cur.Elem()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return ReflectType(reflect.TypeFor[int]()), ReflectType(cur), nil
	default:
		return nil, nil, fmt.Errorf("cannot range over type %s", cur)
	}
}

// ID returns the constant "nil" identifier.
func (nilDataType) ID() string {
	return "nil"
}

// Member always fails on nil data.
func (nilDataType) Member(name string) (Type, error) {
	return nil, fmt.Errorf("nil data; no entry for key %q", name)
}

// Index always fails on nil data.
func (nilDataType) Index() (Type, error) {
	return nil, fmt.Errorf("cannot index nil data")
}

// Range always fails on nil data.
func (nilDataType) Range() (Type, Type, error) {
	return nil, nil, fmt.Errorf("cannot range over nil data")
}

// ID returns a composite identifier of all union branch types.
func (m *multiType) ID() string {
	ids := make([]string, 0, len(m.types))
	for _, t := range m.types {
		ids = append(ids, typeID(t))
	}
	return "union:" + strings.Join(ids, "|")
}

// Member resolves member access across union alternatives.
func (m *multiType) Member(name string) (Type, error) {
	var results []Type
	var lastErr error
	for _, t := range m.types {
		if t == nil {
			return nil, nil
		}
		res, err := t.Member(name)
		if err == nil {
			results = append(results, res)
		} else {
			lastErr = err
		}
	}
	if len(results) > 0 {
		return mergeTypeList(results), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("field %q not declared", name)
}

// Index resolves indexing across union alternatives.
func (m *multiType) Index() (Type, error) {
	var results []Type
	var lastErr error
	for _, t := range m.types {
		if t == nil {
			return nil, nil
		}
		res, err := t.Index()
		if err == nil {
			results = append(results, res)
		} else {
			lastErr = err
		}
	}
	if len(results) > 0 {
		return mergeTypeList(results), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("cannot index union type")
}

// Range resolves ranging across union alternatives.
func (m *multiType) Range() (Type, Type, error) {
	var keyResults, elemResults []Type
	var lastErr error
	for _, t := range m.types {
		if t == nil {
			return nil, nil, nil
		}
		k, e, err := t.Range()
		if err == nil {
			keyResults = append(keyResults, k)
			elemResults = append(elemResults, e)
		} else {
			lastErr = err
		}
	}
	if len(elemResults) > 0 {
		return mergeTypeList(keyResults), mergeTypeList(elemResults), nil
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, fmt.Errorf("cannot range over union type")
}

// newReflectType constructs a reflectType with explicit addressability tracking.
func newReflectType(t reflect.Type, addressable bool) Type {
	if t == nil {
		return nil
	}
	cur := deref(t)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil
	}
	return &reflectType{t: t, addressable: addressable}
}

// derefWithAddr unwraps pointer types while tracking addressability.
func derefWithAddr(t reflect.Type, addr bool) (reflect.Type, bool) {
	cur := t
	for cur != nil && cur.Kind() == reflect.Ptr {
		cur = cur.Elem()
		addr = true
	}
	return cur, addr
}

// deref unwraps pointer types until reaching the underlying value type.
func deref(t reflect.Type) reflect.Type {
	cur := t
	for cur != nil && cur.Kind() == reflect.Ptr {
		cur = cur.Elem()
	}
	return cur
}

// validMethodReturn verifies that mt has a valid template method/function return signature
// (either 1 return value, or 2 return values where the 2nd is error), and returns the primary return type.
func validMethodReturn(mt reflect.Type) (reflect.Type, bool) {
	if mt == nil || mt.Kind() != reflect.Func {
		return nil, false
	}
	if mt.NumOut() == 1 {
		return mt.Out(0), true
	}
	if mt.NumOut() == 2 && mt.Out(1) == errorType {
		return mt.Out(0), true
	}
	return nil, false
}

// typeID returns the canonical string ID of t, or dynamicTypeID when t is nil or has an empty ID.
func typeID(t Type) string {
	if t == nil {
		return dynamicTypeID
	}
	id := t.ID()
	if id == "" {
		return dynamicTypeID
	}
	return id
}

// mergeTypes combines two static Types into a single Type.
// If either is nil (dynamic), the result is nil (dynamic).
// If both have identical IDs, t1 is returned.
// Otherwise, a multiType containing both alternatives is returned.
func mergeTypes(t1, t2 Type) Type {
	if t1 == nil || t2 == nil {
		return nil
	}
	if typeID(t1) == typeID(t2) {
		return t1
	}
	return mergeTypeList([]Type{t1, t2})
}

// mergeTypeList flattens and deduplicates a slice of Types into a canonical union Type.
func mergeTypeList(types []Type) Type {
	flattened := make([]Type, 0, len(types))
	for _, t := range types {
		if t == nil {
			return nil
		}
		if mt, ok := t.(*multiType); ok {
			flattened = append(flattened, mt.types...)
		} else {
			flattened = append(flattened, t)
		}
	}
	seen := make(map[string]bool, len(flattened))
	dedup := make([]Type, 0, len(flattened))
	for _, t := range flattened {
		id := typeID(t)
		if id == dynamicTypeID {
			return nil
		}
		if !seen[id] {
			seen[id] = true
			dedup = append(dedup, t)
		}
	}
	if len(dedup) == 0 {
		return nil
	}
	if len(dedup) == 1 {
		return dedup[0]
	}
	return &multiType{types: dedup}
}
