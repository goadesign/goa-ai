// Package codegen assigns JSON field names on copied tool attributes. Marked
// native-image evidence keeps declared names; other tool contracts use model
// names. Shared design expressions are never changed.
package codegen

import (
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// specJSONContract selects the JSON names for one generated type occurrence.
// The native-image marker selects declared names only for that evidence graph.
type specJSONContract uint8

const (
	specJSONModel specJSONContract = iota
	specJSONNativeImage
)

// transportAttribute copies the type and assigns each field the JSON name that
// its generated decoder accepts. Goa still owns pointer and presence rules.
func (contract specJSONContract) transportAttribute(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	contract.normalizeTransportAttribute(cloned, make(map[goaexpr.UserType]struct{}))
	return cloned
}

// schemaAttribute copies the transport graph for schemas and raw-field checks.
// Native evidence already has declared names; model contracts also rename object
// keys and remove injected fields from properties and required lists.
func (contract specJSONContract) schemaAttribute(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	if contract == specJSONModel {
		normalizeModelSchemaAttrRecursive(cloned)
	}
	return cloned
}

// normalizeTransportAttribute removes package locators from a copied graph and
// writes its JSON tags. Each named type is visited once to preserve recursion.
func (contract specJSONContract) normalizeTransportAttribute(att *goaexpr.AttributeExpr, seen map[goaexpr.UserType]struct{}) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	stripStructPkgMetaKeys(att)
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		origin := dt.Origin()
		if _, ok := seen[origin]; ok {
			return
		}
		seen[origin] = struct{}{}
		contract.normalizeTransportAttribute(dt.Attribute(), seen)
	case *goaexpr.Object:
		for _, nat := range *dt {
			if nat == nil || nat.Attribute == nil {
				continue
			}
			if nat.Attribute.Meta == nil {
				nat.Attribute.Meta = make(goaexpr.MetaExpr)
			}
			name, visible := contract.transportFieldName(nat)
			delete(nat.Attribute.Meta, "struct:tag:json")
			delete(nat.Attribute.Meta, "struct:tag:json:name")
			if visible {
				// Goa appends omitempty according to the containing field's rules.
				nat.Attribute.Meta["struct:tag:json:name"] = []string{name}
			} else {
				nat.Attribute.Meta["struct:tag:json"] = []string{"-"}
			}
			contract.normalizeTransportAttribute(nat.Attribute, seen)
		}
	case *goaexpr.Array:
		contract.normalizeTransportAttribute(dt.ElemType, seen)
	case *goaexpr.Map:
		contract.normalizeTransportAttribute(dt.KeyType, seen)
		contract.normalizeTransportAttribute(dt.ElemType, seen)
	case *goaexpr.Union:
		if contract == specJSONNativeImage {
			// Union helper names are exact in Goa. Derive a separate name from
			// the authored declaration each time a transport copy is visited.
			source := att.AuthoredAttribute().Type.(*goaexpr.Union)
			dt.TypeName = source.TypeName + "NativeImageTransport"
		}
		for _, nat := range dt.Values {
			if nat != nil {
				contract.normalizeTransportAttribute(nat.Attribute, seen)
			}
		}
	}
}

// normalizeModelSchemaAttrRecursive rewrites a cloned attribute graph so Goa's
// OpenAPI schema generator naturally emits the model JSON contract.
func normalizeModelSchemaAttrRecursive(att *goaexpr.AttributeExpr) {
	normalizeModelSchemaAttr(att, make(map[goaexpr.UserType]struct{}))
}

// normalizeModelSchemaAttr visits every distinct named type once so recursive
// schemas keep their cycle while every reachable field is normalized.
func normalizeModelSchemaAttr(att *goaexpr.AttributeExpr, seen map[goaexpr.UserType]struct{}) {
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
		origin := dt.Origin()
		if _, ok := seen[origin]; ok {
			return
		}
		seen[origin] = struct{}{}
		normalizeModelSchemaAttr(dt.Attribute(), seen)
	case *goaexpr.Object:
		normalizeModelSchemaObject(att, dt, seen)
	case *goaexpr.Array:
		normalizeModelSchemaAttr(dt.ElemType, seen)
	case *goaexpr.Map:
		normalizeModelSchemaAttr(dt.KeyType, seen)
		normalizeModelSchemaAttr(dt.ElemType, seen)
	case *goaexpr.Union:
		for _, nat := range dt.Values {
			if nat == nil {
				continue
			}
			normalizeModelSchemaAttr(nat.Attribute, seen)
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
		normalized := specJSONModel.normalizeExampleValue(att, example.Value)
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
func normalizeModelSchemaObject(att *goaexpr.AttributeExpr, obj *goaexpr.Object, typeSeen map[goaexpr.UserType]struct{}) {
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
		// Validation reads this copied schema but accesses the transport's Go
		// fields. Preserve the original naming input when JSON renaming changes
		// that selector; explicit field-name metadata remains authoritative.
		fieldName := goacodegen.GoifyAtt(nat.Attribute, originalName, true)
		if fieldName != goacodegen.GoifyAtt(nat.Attribute, name, true) {
			nat.Attribute.AddMeta("struct:field:name", originalName)
		}
		normalizeModelSchemaAttr(nat.Attribute, typeSeen)
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

// transportFieldName returns the JSON name for this type occurrence. Complete
// native evidence uses the declared field even when another transport hides it;
// ordinary tool contracts retain their model naming and injected-field rules.
func (contract specJSONContract) transportFieldName(nat *goaexpr.NamedAttributeExpr) (string, bool) {
	if nat == nil || nat.Attribute == nil {
		return "", false
	}
	if contract == specJSONNativeImage {
		return nat.Name, true
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
