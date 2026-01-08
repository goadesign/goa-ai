package codegen

import (
	goaexpr "goa.design/goa/v3/expr"
)

// cloneWithJSONTags returns a deep copy of the provided attribute where all
// object fields (including nested objects) are guaranteed to carry json struct
// tags that match their original field names.
//
// The function recursively decorates:
//   - Immediate object fields for the given attribute.
//   - Nested object fields within inline struct definitions.
//   - Existing struct:tag:json metadata is preserved so DSL authors can
//     override tags explicitly using Meta or design-specific helpers.
//   - Non-object attributes are returned as-is.
//
// This helper is used when materializing tool payload/result alias types so
// that the generated structs serialize according to the tool schema even when
// the underlying design types were authored without explicit JSON tag metadata.
// Recursive handling ensures that synthesized nested types (such as the bounds
// helper struct on bounded tool results) also serialize with lowercase keys
// matching the JSON schema.
func cloneWithJSONTags(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}

	// Work on a deep copy so that we never mutate the shared Goa design
	// expressions used by other generators.
	cloned := goaexpr.DupAtt(att)

	addJSONTagsRecursive(cloned)

	return cloned
}

// addJSONTagsRecursive walks the attribute tree and ensures all object fields
// carry json struct tags matching their original field names.
func addJSONTagsRecursive(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil {
		return
	}

	obj := goaexpr.AsObject(att.Type)
	if obj == nil {
		return
	}

	for _, nat := range *obj {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		if nat.Attribute.Meta == nil {
			nat.Attribute.Meta = make(goaexpr.MetaExpr)
		}
		// Only set the json name when no explicit json tag metadata was provided
		// in the DSL.
		//
		// Use "struct:tag:json:name" (not "struct:tag:json") so Goa can append
		// ",omitempty" automatically for fields that are not required by their
		// parent object. This prevents optional pointer/slice/map fields from
		// being marshaled as `"field": null`, which violates non-nullable JSON
		// Schema contracts enforced by the registry gateway.
		if _, ok := nat.Attribute.Meta["struct:tag:json"]; !ok {
			if _, ok := nat.Attribute.Meta["struct:tag:json:name"]; !ok {
				nat.Attribute.Meta["struct:tag:json:name"] = []string{nat.Name}
			}
		}
		// Recurse into nested objects to handle inline struct fields.
		addJSONTagsRecursive(nat.Attribute)
	}
}
