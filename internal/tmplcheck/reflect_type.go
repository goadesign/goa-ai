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
		Range(twoVars bool) (key Type, elem Type, err error)
	}

	// reflectType wraps a Go reflect.Type to implement Type with addressability tracking.
	reflectType struct {
		t           reflect.Type
		addressable bool
		knownTruth  *bool
	}

	// nilDataType represents untyped nil data passed to a named template
	// when invoked without a data pipeline (e.g. {{template "name"}}).
	nilDataType struct{}

	// multiType represents a union of types from divergent branches.
	multiType struct {
		types      []Type
		knownTruth *bool
	}

	// knownTruthType wraps a non-reflected Type with statically known boolean truthiness.
	knownTruthType struct {
		base  Type
		truth bool
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

// WithKnownTruth returns a copy of t tagged with statically known boolean truthiness.
func WithKnownTruth(t Type, truth bool) Type {
	return withKnownTruth(t, truth)
}

// ID returns a canonical comparable identity for cycle detection in recursive
// named template invocations.
func (r *reflectType) ID() string {
	if r.knownTruth != nil {
		return fmt.Sprintf("reflect:%p:addr=%t:truth=%t", r.t, r.addressable, *r.knownTruth)
	}
	return fmt.Sprintf("reflect:%p:addr=%t", r.t, r.addressable)
}

// Member resolves a dot field, method, or map key access (.Name) on this type.
func (r *reflectType) Member(name string) (Type, error) {
	// First check direct methods on r.t (e.g. pointer receiver methods when r.t is a pointer).
	if m, ok := r.t.MethodByName(name); ok {
		if out, valid := validMethodReturn(m.Type); valid {
			return unwrapFuncReturn(out), nil
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
			fieldAddr := isFieldAddressable(cur, sf.Index, isAddr)
			return newReflectType(sf.Type, fieldAddr), nil
		}
		if m, ok := cur.MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return unwrapFuncReturn(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, cur)
		}
		if isAddr {
			if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
				if out, valid := validMethodReturn(m.Type); valid {
					return unwrapFuncReturn(out), nil
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
				return unwrapFuncReturn(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, cur)
		}
		if isAddr {
			if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
				if out, valid := validMethodReturn(m.Type); valid {
					return unwrapFuncReturn(out), nil
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
func (r *reflectType) Range(twoVars bool) (Type, Type, error) {
	cur, isAddr := derefWithAddr(r.t, r.addressable)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Only collection kinds, integers, and iter functions support range.
	case reflect.Slice:
		return ReflectType(reflect.TypeFor[int]()), newReflectType(cur.Elem(), true), nil
	case reflect.Array:
		return ReflectType(reflect.TypeFor[int]()), newReflectType(cur.Elem(), isAddr), nil
	case reflect.Map:
		return ReflectType(cur.Key()), ReflectType(cur.Elem()), nil
	case reflect.Chan:
		return ReflectType(reflect.TypeFor[int]()), ReflectType(cur.Elem()), nil
	case reflect.Func:
		if cur.CanSeq() {
			if twoVars {
				return nil, nil, fmt.Errorf("cannot range over %s with two variables", cur)
			}
			return nil, ReflectType(cur.In(0).In(0)), nil
		}
		if cur.CanSeq2() {
			yield := cur.In(0)
			if twoVars {
				return ReflectType(yield.In(0)), ReflectType(yield.In(1)), nil
			}
			return nil, ReflectType(yield.In(0)), nil
		}
		return nil, nil, fmt.Errorf("cannot range over type %s", cur)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if twoVars {
			return nil, nil, fmt.Errorf("cannot range over %s with two variables", cur)
		}
		return nil, ReflectType(cur), nil
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

// Range allows ranging over untyped nil data (which acts as empty collection).
func (nilDataType) Range(_ bool) (Type, Type, error) {
	return nilDataType{}, nilDataType{}, nil
}

// ID returns a composite identifier of all union branch types.
func (m *multiType) ID() string {
	ids := make([]string, 0, len(m.types))
	for _, t := range m.types {
		ids = append(ids, typeID(t))
	}
	base := "union:" + strings.Join(ids, "|")
	if m.knownTruth != nil {
		return fmt.Sprintf("%s:truth=%t", base, *m.knownTruth)
	}
	return base
}

// Member resolves member access across union alternatives. An operation is valid only if all alternatives support it.
func (m *multiType) Member(name string) (Type, error) {
	results := make([]Type, 0, len(m.types))
	for _, t := range m.types {
		if t == nil {
			continue
		}
		res, err := t.Member(name)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return mergeTypeList(results), nil
}

// Index resolves indexing across union alternatives. An operation is valid only if all alternatives support it.
func (m *multiType) Index() (Type, error) {
	results := make([]Type, 0, len(m.types))
	for _, t := range m.types {
		if t == nil {
			continue
		}
		res, err := t.Index()
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return mergeTypeList(results), nil
}

// Range resolves ranging across union alternatives. An operation is valid only if all alternatives support it.
func (m *multiType) Range(twoVars bool) (Type, Type, error) {
	keyResults := make([]Type, 0, len(m.types))
	elemResults := make([]Type, 0, len(m.types))
	for _, t := range m.types {
		if t == nil {
			continue
		}
		k, e, err := t.Range(twoVars)
		if err != nil {
			return nil, nil, err
		}
		keyResults = append(keyResults, k)
		elemResults = append(elemResults, e)
	}
	return mergeTypeList(keyResults), mergeTypeList(elemResults), nil
}

// ID returns a canonical comparable identity for cycle detection in recursive named template invocations.
func (k *knownTruthType) ID() string {
	return fmt.Sprintf("%s:truth=%t", typeID(k.base), k.truth)
}

// Member resolves a dot field, method, or map key access on the underlying type.
func (k *knownTruthType) Member(name string) (Type, error) {
	if k.base == nil {
		return nil, nil
	}
	return k.base.Member(name)
}

// Index resolves an index operation on the underlying type.
func (k *knownTruthType) Index() (Type, error) {
	if k.base == nil {
		return nil, nil
	}
	return k.base.Index()
}

// Range resolves a range operation on the underlying type.
func (k *knownTruthType) Range(twoVars bool) (Type, Type, error) {
	if k.base == nil {
		return nil, nil, nil
	}
	return k.base.Range(twoVars)
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

// withKnownTruth attaches a known boolean truth value to a Type.
func withKnownTruth(t Type, truth bool) Type {
	if t == nil {
		return &knownTruthType{base: nil, truth: truth}
	}
	if isNilDataType(t) && !truth {
		return nilDataType{}
	}
	switch v := t.(type) {
	case *reflectType:
		clone := *v
		clone.knownTruth = &truth
		return &clone
	case *multiType:
		clone := *v
		clone.knownTruth = &truth
		return &clone
	case *knownTruthType:
		return &knownTruthType{base: v.base, truth: truth}
	default:
		return &knownTruthType{base: withoutKnownTruth(t), truth: truth}
	}
}

// withoutKnownTruth returns a copy of t with knownTruth cleared.
func withoutKnownTruth(t Type) Type {
	if t == nil {
		return nil
	}
	switch v := t.(type) {
	case *reflectType:
		if v.knownTruth == nil {
			return v
		}
		clone := *v
		clone.knownTruth = nil
		return &clone
	case *multiType:
		if v.knownTruth == nil {
			return v
		}
		clone := *v
		clone.knownTruth = nil
		return &clone
	case *knownTruthType:
		return v.base
	default:
		return t
	}
}

// unwrapAndOrOperand unwraps reflect.Value operands to dynamic (nil Type) for built-in and/or.
func unwrapAndOrOperand(t Type) Type {
	if t == nil {
		return nil
	}
	clean := withoutKnownTruth(t)
	if clean == nil {
		return t
	}
	if rt, ok := clean.(*reflectType); ok {
		if rt.t == reflect.TypeFor[reflect.Value]() {
			if k, tr := staticTruth(nil, t); k {
				return withKnownTruth(nil, tr)
			}
			return nil
		}
		return t
	}
	if mt, ok := clean.(*multiType); ok {
		mapped := make([]Type, len(mt.types))
		for i, alt := range mt.types {
			mapped[i] = unwrapAndOrOperand(alt)
		}
		merged := mergeTypeList(mapped)
		if k, tr := staticTruth(nil, t); k {
			return withKnownTruth(merged, tr)
		}
		return merged
	}
	return t
}

// unwrapFuncReturn converts a function or method return reflect.Type to a Type,
// unwrapping reflect.Value to dynamic (nil Type) per text/template execution semantics.
func unwrapFuncReturn(out reflect.Type) Type {
	if out == nil || out == reflect.TypeFor[reflect.Value]() {
		return nil
	}
	return ReflectType(out)
}

// derefWithAddr unwraps pointer types while tracking addressability and terminating on recursive pointer cycles.
func derefWithAddr(t reflect.Type, addr bool) (reflect.Type, bool) {
	cur := t
	var seen map[reflect.Type]bool
	for cur != nil && cur.Kind() == reflect.Ptr {
		elem := cur.Elem()
		if elem == cur {
			break
		}
		if elem != nil && elem.Kind() == reflect.Ptr {
			if seen == nil {
				seen = make(map[reflect.Type]bool)
			}
			if seen[elem] {
				break
			}
			seen[cur] = true
		}
		cur = elem
		addr = true
	}
	return cur, addr
}

// deref unwraps pointer types until reaching the underlying value type.
func deref(t reflect.Type) reflect.Type {
	cur, _ := derefWithAddr(t, false)
	return cur
}

// isFieldAddressable reports whether accessing a field path produces an addressable value.
func isFieldAddressable(structType reflect.Type, index []int, baseAddressable bool) bool {
	if baseAddressable {
		return true
	}
	cur := structType
	for i := 0; i < len(index)-1; i++ {
		sf := cur.Field(index[i])
		t := sf.Type
		if t.Kind() == reflect.Ptr {
			return true
		}
		cur = t
	}
	return false
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
// If both have identical IDs, t1 is returned.
// Otherwise, a multiType containing both alternatives is returned.
func mergeTypes(t1, t2 Type) Type {
	if typeID(t1) == typeID(t2) {
		return t1
	}
	return mergeTypeList([]Type{t1, t2})
}

// mergeTypeList flattens and deduplicates a slice of Types into a canonical union Type.
func mergeTypeList(types []Type) Type {
	if len(types) == 0 {
		return nil
	}
	allKnownTruth := true
	var commonTruth bool
	hasDynamic := false
	for i, t := range types {
		if withoutKnownTruth(t) == nil {
			hasDynamic = true
		}
		k, tr := staticTruth(nil, t)
		switch {
		case !k:
			allKnownTruth = false
		case i == 0:
			commonTruth = tr
		case commonTruth != tr:
			allKnownTruth = false
		}
	}
	if hasDynamic {
		if allKnownTruth {
			return withKnownTruth(nil, commonTruth)
		}
		return nil
	}

	flattened := make([]Type, 0, len(types))
	for _, t := range types {
		clean := withoutKnownTruth(t)
		if mt, ok := clean.(*multiType); ok {
			flattened = append(flattened, mt.types...)
		} else {
			flattened = append(flattened, clean)
		}
	}
	seen := make(map[string]bool, len(flattened))
	dedup := make([]Type, 0, len(flattened))
	for _, t := range flattened {
		id := typeID(t)
		if id == dynamicTypeID {
			if allKnownTruth {
				return withKnownTruth(nil, commonTruth)
			}
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
	var res Type
	if len(dedup) == 1 {
		res = dedup[0]
	} else {
		res = &multiType{types: dedup}
	}
	if allKnownTruth {
		return withKnownTruth(res, commonTruth)
	}
	return res
}
