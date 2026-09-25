// Package codec decides whether an entire original value has a closed generated
// JSON representation. This query does not claim packages or reserve names.
package codec

import (
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// SupportsStandalone reports whether every value reachable from attribute has
// a supported generated JSON representation. Unsupported values omit a codec;
// they do not invalidate an otherwise valid Goa design.
func SupportsStandalone(attribute *expr.AttributeExpr) bool {
	return supportsStandalone(attribute, make(map[expr.UserType]bool))
}

func supportsStandalone(attribute *expr.AttributeExpr, seen map[expr.UserType]bool) bool {
	if attribute == nil || attribute.Type == nil {
		return false
	}
	if custom, _ := codegen.GetMetaType(attribute); custom != "" {
		return false
	}
	switch actual := attribute.Type.(type) {
	case expr.Primitive:
		return actual != expr.Any
	case expr.UserType:
		if actual == expr.Empty {
			return true
		}
		if expr.IsErrorResult(actual) {
			return false
		}
		if seen[actual.Origin()] {
			return true
		}
		seen[actual.Origin()] = true
		return supportsStandalone(actual.Attribute(), seen)
	case *expr.Object:
		for _, field := range *actual {
			if !supportsStandalone(field.Attribute, seen) {
				return false
			}
		}
		return true
	case *expr.Array:
		return supportsStandalone(actual.ElemType, seen)
	case *expr.Map:
		key, ok := jsonshape.PrimitiveType(actual.KeyType)
		return ok && key == expr.String &&
			supportsStandalone(actual.KeyType, seen) &&
			supportsStandalone(actual.ElemType, seen)
	case *expr.Union:
		for _, branch := range actual.Values {
			if !supportsStandalone(branch.Attribute, seen) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
