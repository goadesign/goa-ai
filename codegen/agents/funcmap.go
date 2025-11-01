package codegen

import (
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// templateFuncMap returns the set of helper functions made available to the
// code generation templates. We keep the definition centralized so all sections
// share the same helpers (e.g., goify for consistent identifier casing).
func templateFuncMap() map[string]any {
	return map[string]any{
		"goify":      goacodegen.Goify,
		"trimPrefix": strings.TrimPrefix,
		"ToLower":    strings.ToLower,
		// simpleField reports whether the named field on the given attribute
		// resolves to a simple assignable type between packages: primitives
		// (string, bool, numbers) or arrays/maps composed of primitives. It
		// returns false for user types and any composite containing objects.
		"simpleField": func(attr *goaexpr.AttributeExpr, name string) bool {
			if attr == nil {
				return false
			}
			// Resolve attribute object
			a := resolve(attr)
			obj, ok := a.Type.(*goaexpr.Object)
			if !ok || obj == nil {
				return false
			}
			var fa *goaexpr.AttributeExpr
			for _, nat := range *obj {
				if nat != nil && nat.Name == name {
					fa = nat.Attribute
					break
				}
			}
			if fa == nil {
				return false
			}
			return isSimpleAttr(fa)
		},
		// fieldsOf returns the JSON field names of the given attribute when it
		// represents an object (following user type indirections). The names are
		// returned in stable (lexicographic) order for deterministic generation.
		"fieldsOf": func(attr *goaexpr.AttributeExpr) []string {
			if attr == nil {
				return nil
			}
			a := resolve(attr)
			obj, ok := a.Type.(*goaexpr.Object)
			if !ok || obj == nil {
				return nil
			}
			// Copy names and sort
			names := make([]string, 0, len(*obj))
			for _, na := range *obj {
				if na == nil || na.Name == "" {
					continue
				}
				names = append(names, na.Name)
			}
			// Simple lexicographic sort for deterministic output.
			for i := 0; i < len(names); i++ {
				for j := i + 1; j < len(names); j++ {
					if names[j] < names[i] {
						names[i], names[j] = names[j], names[i]
					}
				}
			}
			return names
		},
	}
}

// resolve dereferences user types to their underlying attribute.
func resolve(a *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if a == nil {
		return nil
	}
	for {
		switch t := a.Type.(type) {
		case *goaexpr.UserTypeExpr:
			a = t.AttributeExpr
		case goaexpr.UserType:
			a = t.Attribute()
		default:
			return a
		}
	}
}

// isSimple reports whether the attribute ultimately resolves to a primitive or
// compositions (arrays/maps) of primitives. User types are considered non-simple
// even when based on primitives to avoid cross-package named type assignment.
func isSimpleAttr(a *goaexpr.AttributeExpr) bool {
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
		return false
	}
	switch t := a.Type.(type) {
	case goaexpr.Primitive:
		return true
	case *goaexpr.Array:
		return isSimpleAttr(t.ElemType)
	case *goaexpr.Map:
		return isSimpleAttr(t.KeyType) && isSimpleAttr(t.ElemType)
	case *goaexpr.Object:
		return false
	case *goaexpr.UserTypeExpr:
		return false
	case goaexpr.UserType:
		return false
	default:
		return false
	}
}
