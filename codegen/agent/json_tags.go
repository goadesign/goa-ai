package codegen

import (
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// cloneWithModelJSONTags returns a deep copy of the provided attribute where:
//   - all object fields (including nested objects) carry json struct tags whose
//     names match the model-facing JSON field names, and
//   - any `struct:pkg:*` locator metadata is removed so synthesized transport
//     types are treated as local to the specs package.
//
// Transport types are model-facing: they must encode/decode using the JSON
// schema property names and must preserve missing-vs-zero semantics via pointer
// primitives (controlled by NameScope.GoTypeDef's ptr flag, not by this helper).
//
// Existing HTTP/public transport JSON tag metadata is ignored because Goa
// generators can attach it to shared design expressions. The only preserved JSON
// tag is "-", which goa-ai uses to hide injected fields from the model contract.
func cloneWithModelJSONTags(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
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
//   - ensures object fields carry json:name tags matching the model-facing
//     field names.
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
		// Use "struct:tag:json:name" (not "struct:tag:json") so Goa can append
		// ",omitempty" automatically for fields that are not required by their
		// parent object. Preserve only json:"-" because it hides injected fields.
		hidden := hiddenJSONTag(nat.Attribute)
		delete(nat.Attribute.Meta, "struct:tag:json")
		delete(nat.Attribute.Meta, "struct:tag:json:name")
		if hidden {
			nat.Attribute.Meta["struct:tag:json"] = []string{"-"}
		} else {
			nat.Attribute.Meta["struct:tag:json:name"] = []string{modelJSONName(nat.Name)}
		}
		normalizeTransportAttrRecursive(nat.Attribute)
	}
}

// modelJSONName returns the default model-facing JSON property name for a Goa
// attribute. Tool contracts default to snake_case even when the Go/public API
// attribute name is lowerCamel.
func modelJSONName(name string) string {
	return goacodegen.SnakeCase(name)
}

// transportFieldName returns the JSON property name generated for nat in a
// model-facing transport attribute. HTTP/public transport tag metadata is
// ignored; json:"-" remains authoritative for hidden injected fields.
func transportFieldName(nat *goaexpr.NamedAttributeExpr) (string, bool) {
	if nat == nil || nat.Attribute == nil {
		return "", false
	}
	if hiddenJSONTag(nat.Attribute) {
		return "", false
	}
	return modelJSONName(nat.Name), true
}

// hiddenJSONTag reports whether att is explicitly hidden from model-facing
// JSON. goa-ai uses this for injected fields that service code supplies after
// model decoding.
func hiddenJSONTag(att *goaexpr.AttributeExpr) bool {
	name, ok := explicitJSONTagName(att)
	return ok && name == "-"
}

// explicitJSONTagName extracts a field name from Goa's JSON tag metadata.
// A "-" tag is returned to let callers intentionally skip hidden fields.
func explicitJSONTagName(att *goaexpr.AttributeExpr) (string, bool) {
	if att == nil || len(att.Meta) == 0 {
		return "", false
	}
	if tags := att.Meta["struct:tag:json"]; len(tags) > 0 {
		name := strings.Split(tags[0], ",")[0]
		return name, name != ""
	}
	if names := att.Meta["struct:tag:json:name"]; len(names) > 0 {
		return names[0], names[0] != ""
	}
	return "", false
}
