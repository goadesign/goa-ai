// Package codegen builds JSON schemas, examples, and field details used by generated
// tool descriptions and JSON errors.
package codegen

import (
	"encoding/json"
	"fmt"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonschema"
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

const (
	jsonSchemaTypeInteger = "integer"
	unionTypeKeyDefault   = "type"
	unionValueKeyDefault  = "value"
)

type (
	// exampleData keeps one JSON example in the two forms needed by generated
	// source and JSON Schema.
	exampleData struct {
		JSON  []byte
		Value any
	}

	// fieldPathSegmentData is one fixed property or dynamic collection segment
	// written into generated field metadata.
	fieldPathSegmentData struct {
		Name    string
		Dynamic bool
	}

	// unionBranchData identifies the branch that contains one generated field.
	unionBranchData struct {
		Discriminator []fieldPathSegmentData
		Value         string
	}

	// fieldMetadataData contains all static schema facts emitted for one field.
	fieldMetadataData struct {
		Path                []fieldPathSegmentData
		JSONType            string
		Description         string
		Branches            []unionBranchData
		DiscriminatorValues []string
	}
)

// buildFieldMetadata records each generated field without flattening fixed
// property names, array indexes, map keys, or union branches into strings.
func buildFieldMetadata(att *goaexpr.AttributeExpr) []*fieldMetadataData {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	var out []*fieldMetadataData
	byKey := make(map[string]*fieldMetadataData)
	seen := make(map[string]struct{})
	add := func(field *fieldMetadataData) {
		if len(field.Path) == 0 && field.JSONType == "" && field.Description == "" &&
			len(field.Branches) == 0 && len(field.DiscriminatorValues) == 0 {
			return
		}
		key := generatedFieldMetadataKey(field.Path, field.Branches)
		if existing, ok := byKey[key]; ok {
			if existing.JSONType == "" {
				existing.JSONType = field.JSONType
			}
			if existing.Description == "" {
				existing.Description = field.Description
			}
			if len(existing.DiscriminatorValues) == 0 {
				existing.DiscriminatorValues = field.DiscriminatorValues
			}
			return
		}
		byKey[key] = field
		out = append(out, field)
	}
	var walk func([]fieldPathSegmentData, *goaexpr.AttributeExpr, []unionBranchData, string)
	walk = func(path []fieldPathSegmentData, a *goaexpr.AttributeExpr, branches []unionBranchData, inheritedDescription string) {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return
		}
		description := a.Description
		if description == "" {
			description = inheritedDescription
		}
		if dt, ok := a.Type.(goaexpr.UserType); ok {
			add(&fieldMetadataData{
				Path:        cloneFieldPath(path),
				JSONType:    generatedJSONType(a.Type),
				Description: description,
				Branches:    cloneUnionBranches(branches),
			})
			id := dt.ID()
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			defer delete(seen, id)
			walk(path, dt.Attribute(), branches, description)
			return
		}
		add(&fieldMetadataData{
			Path:        cloneFieldPath(path),
			JSONType:    generatedJSONType(a.Type),
			Description: description,
			Branches:    cloneUnionBranches(branches),
		})
		switch dt := a.Type.(type) {
		case *goaexpr.Object:
			for _, nat := range *dt {
				walk(appendFixedField(path, nat.Name), nat.Attribute, branches, "")
			}
		case *goaexpr.Array:
			walk(appendDynamicField(path), dt.ElemType, branches, description)
		case *goaexpr.Map:
			walk(appendDynamicField(path), dt.ElemType, branches, description)
		case *goaexpr.Union:
			typeKey := dt.GetTypeKey()
			if typeKey == "" {
				typeKey = unionTypeKeyDefault
			}
			valueKey := dt.GetValueKey()
			if valueKey == "" {
				valueKey = unionValueKeyDefault
			}
			discriminator := appendFixedField(path, typeKey)
			values := make([]string, len(dt.Values))
			for index, nat := range dt.Values {
				values[index] = nat.Name
			}
			add(&fieldMetadataData{
				Path:                discriminator,
				JSONType:            "string",
				Branches:            cloneUnionBranches(branches),
				DiscriminatorValues: values,
			})
			for _, nat := range dt.Values {
				branch := unionBranchData{
					Discriminator: cloneFieldPath(discriminator),
					Value:         nat.Name,
				}
				walk(
					appendFixedField(path, valueKey),
					nat.Attribute,
					append(cloneUnionBranches(branches), branch),
					"",
				)
			}
		}
	}
	walk(nil, att, nil, "")
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendFixedField(path []fieldPathSegmentData, name string) []fieldPathSegmentData {
	return append(cloneFieldPath(path), fieldPathSegmentData{Name: name})
}

func appendDynamicField(path []fieldPathSegmentData) []fieldPathSegmentData {
	return append(cloneFieldPath(path), fieldPathSegmentData{Dynamic: true})
}

func cloneFieldPath(path []fieldPathSegmentData) []fieldPathSegmentData {
	return append([]fieldPathSegmentData(nil), path...)
}

func cloneUnionBranches(branches []unionBranchData) []unionBranchData {
	cloned := make([]unionBranchData, len(branches))
	for index, branch := range branches {
		cloned[index] = branch
		cloned[index].Discriminator = cloneFieldPath(branch.Discriminator)
	}
	return cloned
}

func generatedFieldMetadataKey(path []fieldPathSegmentData, branches []unionBranchData) string {
	var key strings.Builder
	writePath := func(value []fieldPathSegmentData) {
		for _, segment := range value {
			if segment.Dynamic {
				key.WriteString("d;")
				continue
			}
			fmt.Fprintf(&key, "f%d:%s;", len(segment.Name), segment.Name)
		}
	}
	writePath(path)
	for _, branch := range branches {
		key.WriteByte('|')
		writePath(branch.Discriminator)
		fmt.Fprintf(&key, "=%d:%s", len(branch.Value), branch.Value)
	}
	return key.String()
}

// generatedJSONType maps Goa types to exact JSON categories. It returns an
// empty string when the design accepts more than one category.
func generatedJSONType(dt goaexpr.DataType) string { return jsonshape.Category(dt) }

// isEmptyStruct reports whether the attribute resolves to an empty object.
// It follows user types so callers can treat alias user types over empty
// objects the same as literal empty structs.
func isEmptyStruct(att *goaexpr.AttributeExpr) bool {
	if att == nil || att.Type == nil {
		return true
	}
	if att.Type == goaexpr.Empty {
		return true
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return isEmptyStruct(dt.Attribute())
	case *goaexpr.Object:
		return len(*dt) == 0
	default:
		return false
	}
}

// schemaVariantsForAttribute starts from the common generated JSON contract,
// then attaches this tool occurrence's explicitly authored examples.
func (contract specJSONContract) schemaVariantsForAttribute(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, example any, identity goaexpr.ExampleIdentity) ([]byte, []byte, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil, nil, nil
	}
	encoded, err := jsonschema.Build(api, att, identity)
	if err != nil {
		return nil, nil, err
	}
	annotated, err := contract.projectAuthoredSchemaExamples(encoded, att, example)
	if err != nil {
		return nil, nil, err
	}
	plain, err := contract.projectAuthoredSchemaExamples(encoded, att, nil)
	return annotated, plain, err
}

// projectAuthoredSchemaExamples removes examples synthesized while Goa builds
// the OpenAPI schema, then restores examples explicitly authored in the Goa
// contract. Generated placeholder values are not model guidance.
func (contract specJSONContract) projectAuthoredSchemaExamples(schemaBytes []byte, att *goaexpr.AttributeExpr, rootExample any) ([]byte, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("decode generated tool schema: %w", err)
	}
	removeSchemaExamples(schema)
	if rootExample != nil {
		schema["example"] = rootExample
	}
	defs, _ := schema["$defs"].(map[string]any)
	contract.restoreNestedSchemaExamples(att, schema, defs, make(map[string]struct{}), true)
	projected, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode generated tool schema: %w", err)
	}
	return projected, nil
}

// restoreNestedSchemaExamples projects explicitly authored examples onto the
// matching schema nodes after synthesized examples have been removed.
func (contract specJSONContract) restoreNestedSchemaExamples(att *goaexpr.AttributeExpr, schema, defs map[string]any, seen map[string]struct{}, root bool) {
	if att == nil || len(schema) == 0 {
		return
	}
	if refName := schemaRefName(schema); refName != "" {
		if _, ok := seen[refName]; ok {
			return
		}
		definition, ok := defs[refName].(map[string]any)
		if !ok {
			return
		}
		seen[refName] = struct{}{}
		contract.restoreNestedSchemaExamples(att, definition, defs, seen, root)
		delete(seen, refName)
		return
	}
	if !root {
		if example := contract.authoredExampleForAttribute(att); example != nil {
			schema["example"] = example.Value
		}
	}
	contract.restoreSchemaChildExamples(att, schema, defs, seen)
}

// restoreSchemaChildExamples follows the attribute and specialized JSON Schema
// together so each nested authored example lands on its matching JSON node.
func (contract specJSONContract) restoreSchemaChildExamples(att *goaexpr.AttributeExpr, schema, defs map[string]any, seen map[string]struct{}) {
	switch actual := att.Type.(type) {
	case goaexpr.UserType:
		contract.restoreSchemaChildExamples(actual.Attribute(), schema, defs, seen)
	case *goaexpr.Object:
		properties, _ := schema["properties"].(map[string]any)
		for _, nat := range *actual {
			name, ok := contract.transportFieldName(nat)
			if !ok {
				continue
			}
			child, _ := properties[name].(map[string]any)
			contract.restoreNestedSchemaExamples(nat.Attribute, child, defs, seen, false)
		}
	case *goaexpr.Array:
		items, _ := schema["items"].(map[string]any)
		contract.restoreNestedSchemaExamples(actual.ElemType, items, defs, seen, false)
	case *goaexpr.Map:
		values, _ := schema["additionalProperties"].(map[string]any)
		contract.restoreNestedSchemaExamples(actual.ElemType, values, defs, seen, false)
	case *goaexpr.Union:
		valueKey := actual.GetValueKey()
		if valueKey == "" {
			valueKey = unionValueKeyDefault
		}
		branches, _ := schema["oneOf"].([]any)
		for i, nat := range actual.Values {
			if i >= len(branches) {
				return
			}
			branch, _ := branches[i].(map[string]any)
			properties, _ := branch["properties"].(map[string]any)
			value, _ := properties[valueKey].(map[string]any)
			contract.restoreNestedSchemaExamples(nat.Attribute, value, defs, seen, false)
		}
	}
}

// removeSchemaExamples removes every example value from a generated schema,
// including named definitions and union branches.
func removeSchemaExamples(node any) {
	switch actual := node.(type) {
	case map[string]any:
		delete(actual, "example")
		for _, child := range actual {
			removeSchemaExamples(child)
		}
	case []any:
		for _, child := range actual {
			removeSchemaExamples(child)
		}
	}
}

func schemaRefName(schema map[string]any) string {
	ref, _ := schema["$ref"].(string)
	if !strings.HasPrefix(ref, "#/$defs/") {
		return ""
	}
	return strings.TrimPrefix(ref, "#/$defs/")
}

// authoredExampleForAttribute returns the last Example(...) written for source
// in the same JSON shape accepted by the generated decoder.
func (contract specJSONContract) authoredExampleForAttribute(source *goaexpr.AttributeExpr) *exampleData {
	if source == nil {
		return nil
	}
	examples := source.ExtractUserExamples()
	if len(examples) == 0 {
		return nil
	}
	return contract.normalizeExampleValue(source, examples[len(examples)-1].Value)
}

// normalizeExampleValue changes one example to ordinary JSON values and writes
// each union as the {type,value} object accepted by the generated decoder.
func (contract specJSONContract) normalizeExampleValue(att *goaexpr.AttributeExpr, v any) *exampleData {
	// Normalize to JSON-native shapes (map[string]any, []any, float64, string, bool)
	// so downstream rewriting logic doesn't have to handle typed maps/slices that
	// Goa's example generator may produce for single-field objects.
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil
	}
	normalized = contract.canonicalizeUnionExamples(att, normalized)
	normalized = contract.projectExampleFieldNames(att, normalized)
	data, err := json.Marshal(normalized)
	if err != nil || len(data) == 0 {
		return nil
	}
	// Treat "{}" as a non-informative example and omit it.
	if string(data) == "{}" {
		return nil
	}
	return &exampleData{JSON: data, Value: normalized}
}

// projectExampleFieldNames rewrites authored example object keys from Goa
// attribute names to the JSON names selected for this occurrence.
func (contract specJSONContract) projectExampleFieldNames(att *goaexpr.AttributeExpr, example any) any {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return example
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return contract.projectExampleFieldNames(dt.Attribute(), example)
	case *goaexpr.Object:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		projected := make(map[string]any, len(m))
		for _, nat := range *dt {
			name, ok := contract.transportFieldName(nat)
			if !ok {
				continue
			}
			value, exists := m[nat.Name]
			if !exists {
				value, exists = m[name]
			}
			if !exists {
				continue
			}
			projected[name] = contract.projectExampleFieldNames(nat.Attribute, value)
		}
		return projected
	case *goaexpr.Array:
		s, ok := example.([]any)
		if !ok {
			return example
		}
		for i, v := range s {
			s[i] = contract.projectExampleFieldNames(dt.ElemType, v)
		}
		return s
	case *goaexpr.Map:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		for k, v := range m {
			m[k] = contract.projectExampleFieldNames(dt.ElemType, v)
		}
		return m
	case *goaexpr.Union:
		return contract.projectUnionExampleFieldNames(dt, example)
	default:
		return example
	}
}

// projectUnionExampleFieldNames keeps a union's {type,value} object and changes
// field names inside the selected value to their JSON names.
func (contract specJSONContract) projectUnionExampleFieldNames(u *goaexpr.Union, example any) any {
	m, ok := example.(map[string]any)
	if !ok {
		return example
	}
	typeKey := u.GetTypeKey()
	if typeKey == "" {
		typeKey = "type"
	}
	valueKey := u.GetValueKey()
	if valueKey == "" {
		valueKey = "value"
	}
	rawType, ok := m[typeKey].(string)
	if !ok {
		return example
	}
	for _, nat := range u.Values {
		if nat == nil || nat.Name != rawType {
			continue
		}
		if value, exists := m[valueKey]; exists {
			m[valueKey] = contract.projectExampleFieldNames(nat.Attribute, value)
		}
		return m
	}
	return example
}

func exampleJSON(example *exampleData) []byte {
	if example == nil {
		return nil
	}
	return example.JSON
}

func exampleValue(example *exampleData) any {
	if example == nil {
		return nil
	}
	return example.Value
}

// canonicalizeUnionExamples changes Goa's branch-only union examples to the
// {type,value} JSON object accepted by generated decoders.
//
// Goa returns only the selected branch value. This function keeps all other
// example values unchanged and wraps each union value with its branch name.
func (contract specJSONContract) canonicalizeUnionExamples(att *goaexpr.AttributeExpr, example any) any {
	normalized, _ := contract.canonicalizeUnionExampleValue(att, example)
	return normalized
}

func (contract specJSONContract) canonicalizeUnionExampleValue(att *goaexpr.AttributeExpr, example any) (any, bool) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return example, true
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return contract.canonicalizeUnionExampleValue(dt.Attribute(), example)
	case *goaexpr.Object:
		m, ok := example.(map[string]any)
		if !ok {
			return example, true
		}
		for k, v := range m {
			child := contract.exampleChildAttribute(att, k)
			normalized, ok := contract.canonicalizeUnionExampleValue(child, v)
			if !ok {
				return nil, false
			}
			m[k] = normalized
		}
		return m, true
	case *goaexpr.Array:
		s, ok := example.([]any)
		if !ok {
			return example, true
		}
		for i, v := range s {
			normalized, ok := contract.canonicalizeUnionExampleValue(dt.ElemType, v)
			if !ok {
				return nil, false
			}
			s[i] = normalized
		}
		return s, true
	case *goaexpr.Map:
		m, ok := example.(map[string]any)
		if !ok {
			return example, true
		}
		for k, v := range m {
			normalized, ok := contract.canonicalizeUnionExampleValue(dt.ElemType, v)
			if !ok {
				return nil, false
			}
			m[k] = normalized
		}
		return m, true
	case *goaexpr.Union:
		if example == nil || len(dt.Values) == 0 {
			return example, true
		}

		typeKey := dt.GetTypeKey()
		if typeKey == "" {
			typeKey = "type"
		}
		valueKey := dt.GetValueKey()
		if valueKey == "" {
			valueKey = "value"
		}

		var chosen *goaexpr.NamedAttributeExpr
		if canonical, ok := contract.canonicalUnionExample(dt, example, typeKey, valueKey); ok {
			return canonical, true
		}
		chosen = pickUnionVariantForExample(dt, example)
		if chosen == nil {
			panic(fmt.Sprintf("agent/specs_builder: union example does not match any variant (type=%q)", dt.TypeName))
		}
		value, ok := contract.canonicalizeUnionExampleValue(chosen.Attribute, example)
		if !ok {
			return nil, false
		}

		return map[string]any{
			typeKey:  chosen.Name,
			valueKey: value,
		}, true
	default:
		return example, true
	}
}

// exampleChildAttribute resolves an authored example object key against both
// the Goa design field name and the JSON name selected for this occurrence.
func (contract specJSONContract) exampleChildAttribute(att *goaexpr.AttributeExpr, key string) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil {
		return nil
	}
	if child := att.Find(key); child != nil {
		return child
	}
	obj := goaexpr.AsObject(att.Type)
	if obj == nil {
		return nil
	}
	for _, nat := range *obj {
		if name, ok := contract.transportFieldName(nat); ok && name == key {
			return nat.Attribute
		}
	}
	return nil
}

// canonicalUnionExample reads an example that already names its union branch
// and fixes any union examples inside the selected value.
func (contract specJSONContract) canonicalUnionExample(u *goaexpr.Union, example any, typeKey, valueKey string) (any, bool) {
	m, ok := example.(map[string]any)
	if !ok {
		return nil, false
	}
	typeName, ok := m[typeKey].(string)
	if !ok || typeName == "" {
		return nil, false
	}
	var chosen *goaexpr.NamedAttributeExpr
	for _, nat := range u.Values {
		if nat != nil && nat.Name == typeName {
			chosen = nat
			break
		}
	}
	if chosen == nil {
		return nil, false
	}
	value, ok := m[valueKey]
	if !ok {
		panic(fmt.Sprintf("agent/specs_builder: canonical union example for %q missing %q", u.TypeName, valueKey))
	}
	normalized, ok := contract.canonicalizeUnionExampleValue(chosen.Attribute, value)
	if !ok {
		return nil, false
	}
	return map[string]any{
		typeKey:  typeName,
		valueKey: normalized,
	}, true
}

func pickUnionVariantForExample(u *goaexpr.Union, example any) *goaexpr.NamedAttributeExpr {
	// Prefer key-based matching for object-shaped unions: Goa emits object examples
	// as map[string]any, but IsCompatible may not be able to match user type
	// variants directly (it reasons about Go types, not JSON shapes).
	if m, ok := example.(map[string]any); ok {
		for _, nat := range u.Values {
			if nat == nil || nat.Attribute == nil {
				continue
			}
			if unionVariantMatchesObjectKeys(nat.Attribute, m) {
				return nat
			}
		}
	}

	for _, nat := range u.Values {
		if nat == nil || nat.Attribute == nil || nat.Attribute.Type == nil {
			continue
		}
		attr := unwrapUserTypeAttr(nat.Attribute)
		if attr == nil || attr.Type == nil {
			continue
		}
		if attr.Type.IsCompatible(example) {
			return nat
		}
	}

	return nil
}

func unionVariantMatchesObjectKeys(att *goaexpr.AttributeExpr, example map[string]any) bool {
	attr := unwrapUserTypeAttr(att)
	if attr == nil {
		return false
	}
	obj, ok := attr.Type.(*goaexpr.Object)
	if !ok || obj == nil {
		return false
	}

	fields := make(map[string]struct{}, len(*obj))
	for _, nat := range *obj {
		fields[nat.Name] = struct{}{}
	}

	for k := range example {
		if _, ok := fields[k]; !ok {
			return false
		}
	}
	return true
}

func unwrapUserTypeAttr(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil {
		return att
	}
	for {
		ut, ok := att.Type.(goaexpr.UserType)
		if !ok {
			return att
		}
		att = ut.Attribute()
		if att == nil || att.Type == nil {
			return att
		}
	}
}

// lowerCamel converts a string to lower camelCase using Goa's Goify function.
func lowerCamel(s string) string {
	return codegen.Goify(s, false)
}
