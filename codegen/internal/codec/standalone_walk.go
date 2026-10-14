// Package codec visits each retained schema copy so package-local generated
// declarations receive their own typed checks. Repeated pointers stop recursion.
package codec

import "goa.design/goa/v3/expr"

// walkPreflightAttribute preserves distinct copies of a named schema. The
// ordinary shape walk groups origins because JSON itself has no Go packages.
func walkPreflightAttribute(
	attribute *expr.AttributeExpr,
	seen map[expr.UserType]struct{},
	visit func(*expr.AttributeExpr) error,
) error {
	if attribute == nil || attribute.Type == nil || attribute.Type == expr.Empty {
		return nil
	}
	if err := visit(attribute); err != nil {
		return err
	}
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		if _, exists := seen[actual]; exists {
			return nil
		}
		seen[actual] = struct{}{}
		return walkPreflightAttribute(actual.Attribute(), seen, visit)
	case *expr.Object:
		for _, field := range *actual {
			if err := walkPreflightAttribute(field.Attribute, seen, visit); err != nil {
				return err
			}
		}
	case *expr.Array:
		return walkPreflightAttribute(actual.ElemType, seen, visit)
	case *expr.Map:
		if err := walkPreflightAttribute(actual.KeyType, seen, visit); err != nil {
			return err
		}
		return walkPreflightAttribute(actual.ElemType, seen, visit)
	case *expr.Union:
		for _, branch := range actual.Values {
			if err := walkPreflightAttribute(branch.Attribute, seen, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
