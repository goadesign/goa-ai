package codegen

import (
	"encoding/json"

	"goa.design/goa/v3/expr"
)

// toJSONSchema returns a compact JSON Schema (draft-agnostic) for the given
// Goa attribute. It purposefully covers the most common Goa types used in
// payloads (objects, arrays, primitives) and recurses into user types.
// The goal is to provide MCP clients with useful shape hints rather than a
// full validation-capable schema.
func toJSONSchema(attr *expr.AttributeExpr) string {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return `{"type":"object","additionalProperties":false}`
	}
	m := make(map[string]any)
	buildSchema(attr, m, make(map[string]struct{}))
	b, _ := json.Marshal(m)
	return string(b)
}

// buildSchema fills dst with a JSON-Schema-like map for the provided attribute.
// visited guards against infinite recursion on cyclic user types.
func buildSchema(attr *expr.AttributeExpr, dst map[string]any, visited map[string]struct{}) {
	switch t := attr.Type.(type) {
	case expr.UserType:
		// Recurse into the underlying attribute. Avoid infinite recursion.
		ut := t.(*expr.UserTypeExpr)
		if _, ok := visited[ut.TypeName]; ok {
			dst["type"] = "object"
			return
		}
		visited[ut.TypeName] = struct{}{}
		buildSchema(ut.Attribute(), dst, visited)
		return
	case *expr.Object:
		dst["type"] = schemaTypeObject
		props := make(map[string]any)
		var required []string
		// Map object fields
		for _, nat := range *t {
			pm := make(map[string]any)
			buildSchema(nat.Attribute, pm, visited)
			props[nat.Name] = pm
		}
		if attr.Validation != nil && len(attr.Validation.Required) > 0 {
			required = append(required, attr.Validation.Required...)
		}
		if len(required) > 0 {
			dst["required"] = required
		}
		dst["properties"] = props
		// Being conservative to avoid surprising payload decoding
		dst["additionalProperties"] = false
		return
	case *expr.Array:
		dst["type"] = "array"
		if t.ElemType != nil {
			itm := make(map[string]any)
			buildSchema(t.ElemType, itm, visited)
			dst["items"] = itm
		}
		if v := attr.Validation; v != nil {
			if v.MinLength != nil { // Goa uses MinLength/MaxLength for collection sizes
				dst["minItems"] = *v.MinLength
			}
			if v.MaxLength != nil {
				dst["maxItems"] = *v.MaxLength
			}
		}
		return
	case *expr.Map:
		// Represent maps as object with free-form additionalProperties
		dst["type"] = schemaTypeObject
		if t.ElemType != nil {
			ap := make(map[string]any)
			buildSchema(t.ElemType, ap, visited)
			dst["additionalProperties"] = ap
		} else {
			dst["additionalProperties"] = true
		}
		return
	case expr.Primitive:
		//nolint:exhaustive // we intentionally handle only primitive kinds relevant for schema generation
		switch t.Kind() {
		case expr.StringKind:
			dst["type"] = schemaTypeString
			if v := attr.Validation; v != nil {
				if v.Pattern != "" {
					dst["pattern"] = v.Pattern
				}
				if v.MinLength != nil {
					dst["minLength"] = *v.MinLength
				}
				if v.MaxLength != nil {
					dst["maxLength"] = *v.MaxLength
				}
			}
		case expr.IntKind, expr.Int32Kind, expr.Int64Kind, expr.UIntKind, expr.UInt32Kind, expr.UInt64Kind:
			dst["type"] = "integer"
			if v := attr.Validation; v != nil {
				if v.Minimum != nil {
					dst["minimum"] = *v.Minimum
				}
				if v.Maximum != nil {
					dst["maximum"] = *v.Maximum
				}
			}
		case expr.Float32Kind, expr.Float64Kind:
			dst["type"] = "number"
			if v := attr.Validation; v != nil {
				if v.Minimum != nil {
					dst["minimum"] = *v.Minimum
				}
				if v.Maximum != nil {
					dst["maximum"] = *v.Maximum
				}
			}
		case expr.BooleanKind:
			dst["type"] = "boolean"
		case expr.BytesKind:
			// Encode bytes as string with format
			dst["type"] = schemaTypeString
			dst["contentEncoding"] = contentEncodingBase64
		default:
			dst["type"] = schemaTypeString
		}
		// Enum support (string/number primarily)
		if attr.Validation != nil && len(attr.Validation.Values) > 0 {
			var vals []any
			vals = append(vals, attr.Validation.Values...)
			dst["enum"] = vals
		}
		return
	default:
		// Fallback
		dst["type"] = schemaTypeObject
		dst["additionalProperties"] = true
	}
}

const (
	schemaTypeObject      = "object"
	schemaTypeString      = "string"
	contentEncodingBase64 = "base64"
)
