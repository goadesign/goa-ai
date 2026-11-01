// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"fmt"
	"reflect"
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

	// reflectType wraps a Go reflect.Type to implement Type.
	reflectType struct {
		t reflect.Type
	}
)

var errorType = reflect.TypeFor[error]()

// ReflectType returns a Type implementation wrapping the Go reflect.Type.
// If t is nil, or if dereferencing t results in nil or interface{}, ReflectType returns nil.
func ReflectType(t reflect.Type) Type {
	if t == nil {
		return nil
	}
	cur := deref(t)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil
	}
	return &reflectType{t: t}
}

// ID returns a canonical comparable identity for cycle detection in recursive
// named template invocations.
func (r *reflectType) ID() string {
	return fmt.Sprintf("reflect:%p", r.t)
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

	cur := deref(r.t)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Structs and maps have named members; other types checked for methods.
	case reflect.Struct:
		if sf, ok := cur.FieldByName(name); ok && sf.IsExported() {
			if sf.Type.Kind() == reflect.Func {
				if out, valid := validMethodReturn(sf.Type); valid {
					return ReflectType(out), nil
				}
				return nil, fmt.Errorf("field %q on type %s has invalid template function signature", name, cur)
			}
			return ReflectType(sf.Type), nil
		}
		if m, ok := cur.MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return ReflectType(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, cur)
		}
		if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return ReflectType(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, reflect.PointerTo(cur))
		}
		return nil, fmt.Errorf("field %q not declared in type %s", name, cur)

	case reflect.Map:
		if reflect.TypeFor[string]().AssignableTo(cur.Key()) || cur.Key().Kind() == reflect.String || cur.Key().Kind() == reflect.Interface {
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
		if m, ok := reflect.PointerTo(cur).MethodByName(name); ok {
			if out, valid := validMethodReturn(m.Type); valid {
				return ReflectType(out), nil
			}
			return nil, fmt.Errorf("method %q on type %s has invalid template return signature", name, reflect.PointerTo(cur))
		}
		return nil, fmt.Errorf("field %q not declared in type %s", name, cur)
	}
}

// Index resolves an index operation (index x k) on this type.
func (r *reflectType) Index() (Type, error) {
	cur := deref(r.t)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Only slice, array, map, and string support indexing.
	case reflect.Slice, reflect.Array, reflect.Map:
		return ReflectType(cur.Elem()), nil
	case reflect.String:
		return ReflectType(reflect.TypeFor[byte]()), nil
	default:
		return nil, fmt.Errorf("cannot index type %s", cur)
	}
}

// Range resolves a range operation (range x) on this type.
func (r *reflectType) Range() (Type, Type, error) {
	cur := deref(r.t)
	if cur == nil || cur.Kind() == reflect.Interface {
		return nil, nil, nil
	}

	switch cur.Kind() { //nolint:exhaustive // Only collection kinds and integers support range.
	case reflect.Slice, reflect.Array:
		return ReflectType(reflect.TypeFor[int]()), ReflectType(cur.Elem()), nil
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
