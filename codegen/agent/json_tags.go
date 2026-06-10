package codegen

import (
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// cloneWithModelJSONTags returns the transport attribute graph used by generated
// codecs. It preserves Goa attribute names so Goa transforms can map transport
// values back to public tool types, but every visible object field carries the
// model-facing JSON tag used on the wire.
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
	normalizeModelJSONTransportAttrRecursive(cloned)

	return cloned
}

// cloneModelSchemaAttribute returns the schema attribute graph used by generated
// tool specs. Object keys are the model-facing JSON property names and fields
// hidden from model JSON are removed entirely from both properties and required
// lists.
func cloneModelSchemaAttribute(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}

	cloned := goaexpr.DupAtt(att)
	normalizeModelSchemaAttrRecursive(cloned)
	return cloned
}

// normalizeModelJSONTransportAttrRecursive:
//   - removes struct:pkg:* locators so nested user types can be materialized
//     locally, and
//   - ensures object fields carry json:name tags matching the model-facing
//     field names.
func normalizeModelJSONTransportAttrRecursive(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}

	stripStructPkgMetaKeys(att)

	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		normalizeModelJSONTransportAttrRecursive(dt.Attribute())
	case *goaexpr.Object:
		for _, nat := range *dt {
			if nat == nil || nat.Attribute == nil {
				continue
			}
			if nat.Attribute.Meta == nil {
				nat.Attribute.Meta = make(goaexpr.MetaExpr)
			}
			// Use "struct:tag:json:name" (not "struct:tag:json") so Goa can
			// append ",omitempty" automatically for fields that are not required
			// by their parent object.
			hidden := hiddenJSONTag(nat.Attribute)
			delete(nat.Attribute.Meta, "struct:tag:json")
			delete(nat.Attribute.Meta, "struct:tag:json:name")
			if hidden {
				nat.Attribute.Meta["struct:tag:json"] = []string{"-"}
			} else {
				nat.Attribute.Meta["struct:tag:json:name"] = []string{modelJSONName(nat.Name)}
			}
			normalizeModelJSONTransportAttrRecursive(nat.Attribute)
		}
	case *goaexpr.Array:
		normalizeModelJSONTransportAttrRecursive(dt.ElemType)
	case *goaexpr.Map:
		normalizeModelJSONTransportAttrRecursive(dt.KeyType)
		normalizeModelJSONTransportAttrRecursive(dt.ElemType)
	case *goaexpr.Union:
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			normalizeModelJSONTransportAttrRecursive(nat.Attribute)
		}
	}
}

// normalizeModelSchemaAttrRecursive rewrites a cloned attribute graph so Goa's
// OpenAPI schema generator naturally emits the model JSON contract.
func normalizeModelSchemaAttrRecursive(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}

	normalizeModelSchemaUserExamples(att)
	stripStructPkgMetaKeys(att)
	delete(att.Meta, "struct:tag:json")
	delete(att.Meta, "struct:tag:json:name")
	if len(att.Meta) == 0 {
		att.Meta = nil
	}

	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		normalizeModelSchemaAttrRecursive(dt.Attribute())
	case *goaexpr.Object:
		normalizeModelSchemaObject(att, dt)
	case *goaexpr.Array:
		normalizeModelSchemaAttrRecursive(dt.ElemType)
	case *goaexpr.Map:
		normalizeModelSchemaAttrRecursive(dt.KeyType)
		normalizeModelSchemaAttrRecursive(dt.ElemType)
	case *goaexpr.Union:
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			normalizeModelSchemaAttrRecursive(nat.Attribute)
		}
	}
}

// normalizeModelSchemaUserExamples projects copied DSL examples into the same
// model JSON names as the schema graph before object keys are rewritten.
func normalizeModelSchemaUserExamples(att *goaexpr.AttributeExpr) {
	if len(att.UserExamples) == 0 {
		return
	}
	projected := make([]*goaexpr.ExampleExpr, 0, len(att.UserExamples))
	for _, example := range att.UserExamples {
		if example == nil {
			continue
		}
		normalized := normalizeExampleValue(att, example.Value)
		if normalized == nil {
			continue
		}
		projected = append(projected, &goaexpr.ExampleExpr{
			Summary:     example.Summary,
			Description: example.Description,
			Value:       normalized.Value,
		})
	}
	att.UserExamples = projected
}

// normalizeModelSchemaObject projects object field names to model JSON names
// and removes fields that are hidden from the model contract.
func normalizeModelSchemaObject(att *goaexpr.AttributeExpr, obj *goaexpr.Object) {
	var requiredList []string
	if att.Validation != nil {
		requiredList = att.Validation.Required
	}
	required := make(map[string]string, len(requiredList))
	projected := make(goaexpr.Object, 0, len(*obj))
	seen := make(map[string]string, len(*obj))
	for _, nat := range *obj {
		if nat == nil || nat.Attribute == nil {
			continue
		}
		originalName := nat.Name
		if hiddenJSONTag(nat.Attribute) {
			continue
		}
		name := modelJSONName(originalName)
		if previous, ok := seen[name]; ok {
			panic("agent/codegen: model JSON field " + name + " collides between " + previous + " and " + originalName)
		}
		seen[name] = originalName
		normalizeModelSchemaAttrRecursive(nat.Attribute)
		nat.Name = name
		projected = append(projected, nat)
		required[originalName] = name
	}
	*obj = projected
	if att.Validation != nil {
		projectedRequired := make([]string, 0, len(requiredList))
		for _, name := range requiredList {
			if projected, ok := required[name]; ok {
				projectedRequired = append(projectedRequired, projected)
			}
		}
		att.Validation.Required = projectedRequired
	}
}

// stripStructPkgMetaKeys removes generated package locators from cloned
// transport/schema graphs so synthesized types are local to the specs package.
func stripStructPkgMetaKeys(att *goaexpr.AttributeExpr) {
	if len(att.Meta) == 0 {
		return
	}
	for k := range att.Meta {
		if strings.HasPrefix(k, "struct:pkg:") {
			delete(att.Meta, k)
		}
	}
	if len(att.Meta) == 0 {
		att.Meta = nil
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
