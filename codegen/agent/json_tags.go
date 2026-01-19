package codegen

import (
	"strings"

	goaexpr "goa.design/goa/v3/expr"
)

// cloneWithJSONTags returns a deep copy of the provided attribute where:
//   - all object fields (including nested objects) carry json struct tags whose
//     names match the DSL field names, and
//   - any `struct:pkg:*` locator metadata is removed so synthesized transport
//     types are treated as local to the specs package.
//
// Transport types are model-facing: they must encode/decode using the JSON
// schema property names and must preserve missing-vs-zero semantics via pointer
// primitives (controlled by NameScope.GoTypeDef's ptr flag, not by this helper).
//
// Existing struct:tag:json metadata is preserved so DSL authors can override
// tags explicitly using Meta or design-specific helpers.
func cloneWithJSONTags(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}

	// Work on a deep copy so that we never mutate the shared Goa design
	// expressions used by other generators.
	cloned := goaexpr.DupAtt(att)

	normalizeTransportAttrRecursive(cloned)

	return cloned
}

// normalizeTransportAttrRecursive:
//   - removes struct:pkg:* locators so nested user types can be materialized
//     locally, and
//   - ensures object fields carry json:name tags matching the DSL field names.
func normalizeTransportAttrRecursive(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}

	if len(att.Meta) > 0 {
		for k := range att.Meta {
			if strings.HasPrefix(k, "struct:pkg:") {
				delete(att.Meta, k)
			}
		}
		if len(att.Meta) == 0 {
			att.Meta = nil
		}
	}

	obj := goaexpr.AsObject(att.Type)
	if obj == nil {
		switch dt := att.Type.(type) {
		case goaexpr.UserType:
			normalizeTransportAttrRecursive(dt.Attribute())
		case *goaexpr.Array:
			normalizeTransportAttrRecursive(dt.ElemType)
		case *goaexpr.Map:
			normalizeTransportAttrRecursive(dt.KeyType)
			normalizeTransportAttrRecursive(dt.ElemType)
		case *goaexpr.Union:
			for _, nat := range dt.Values {
				if nat == nil {
					continue
				}
				normalizeTransportAttrRecursive(nat.Attribute)
			}
		}
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
		// parent object.
		if _, ok := nat.Attribute.Meta["struct:tag:json"]; !ok {
			if _, ok := nat.Attribute.Meta["struct:tag:json:name"]; !ok {
				nat.Attribute.Meta["struct:tag:json:name"] = []string{nat.Name}
			}
		}
		normalizeTransportAttrRecursive(nat.Attribute)
	}
}
