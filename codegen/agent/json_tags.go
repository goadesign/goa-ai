package codegen

import (
	goaexpr "goa.design/goa/v3/expr"
)

// cloneWithJSONTags returns a deep copy of the provided attribute where any
// top-level object fields are guaranteed to carry json struct tags that match
// their original field names.
//
// The function is intentionally conservative:
//   - It only decorates the immediate object fields for the given attribute.
//   - Existing struct:tag:json metadata is preserved so DSL authors can
//     override tags explicitly using Meta or design-specific helpers.
//   - Non-object attributes are returned as-is.
//
// This helper is used when materializing tool payload/result alias types so
// that the generated structs serialize according to the tool schema even when
// the underlying design types were authored without explicit JSON tag metadata.
func cloneWithJSONTags(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}

	// Work on a deep copy so that we never mutate the shared Goa design
	// expressions used by other generators.
	cloned := goaexpr.DupAtt(att)

	obj := goaexpr.AsObject(cloned.Type)
	if obj == nil {
		return cloned
	}

	for _, nat := range *obj {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		if nat.Attribute.Meta == nil {
			nat.Attribute.Meta = make(goaexpr.MetaExpr)
		}
		// Only set the json tag when no explicit tag was provided in the DSL.
		if _, ok := nat.Attribute.Meta["struct:tag:json"]; !ok {
			nat.Attribute.Meta["struct:tag:json"] = []string{nat.Name}
		}
	}

	return cloned
}


