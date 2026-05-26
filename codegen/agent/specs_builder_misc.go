package codegen

import (
	"encoding/json"
	"fmt"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
	"goa.design/goa/v3/http/codegen/openapi"
)

const jsonSchemaTypeInteger = "integer"

type (
	// exampleData keeps one canonical JSON-native example in both generated
	// byte form and schema annotation form so tool specs do not derive the same
	// contract twice.
	exampleData struct {
		JSON  []byte
		Value any
	}
)

// buildFieldDescriptions collects dotted field-path descriptions from the provided
// attribute. It follows objects, arrays, maps and user types, trimming any leading
// root qualifiers at error construction time (newValidationError does this for "body.").
func buildFieldDescriptions(att *goaexpr.AttributeExpr) map[string]string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	out := make(map[string]string)
	seen := make(map[string]struct{})
	var walk func(prefix string, a *goaexpr.AttributeExpr)
	walk = func(prefix string, a *goaexpr.AttributeExpr) {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			// Avoid infinite recursion on recursive user types.
			id := dt.ID()
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			defer delete(seen, id)
			walk(prefix, dt.Attribute())
		case *goaexpr.Object:
			for _, nat := range *dt {
				name := nat.Name
				path := name
				if prefix != "" {
					path = prefix + "." + name
				}
				if nat.Attribute != nil && nat.Attribute.Description != "" {
					out[path] = nat.Attribute.Description
				}
				walk(path, nat.Attribute)
			}
		case *goaexpr.Array:
			walk(prefix, dt.ElemType)
		case *goaexpr.Map:
			walk(prefix, dt.ElemType)
		case *goaexpr.Union:
			// Union branch descriptions depend on the discriminator, so this generic
			// field map records only the union field itself.
		}
	}
	walk("", att)
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildFieldJSONTypes collects the generated JSON type expected at each dotted field path.
func buildFieldJSONTypes(att *goaexpr.AttributeExpr) map[string]string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	out := make(map[string]string)
	seen := make(map[string]struct{})
	var walk func(prefix string, a *goaexpr.AttributeExpr)
	walk = func(prefix string, a *goaexpr.AttributeExpr) {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return
		}
		field := prefix
		if field == "" {
			field = "$payload"
		}
		if field != "" {
			jsonType := generatedJSONType(a.Type)
			if jsonType != "" {
				if _, exists := out[field]; exists {
					jsonType = ""
				}
			}
			if jsonType != "" {
				out[field] = jsonType
			}
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			id := dt.ID()
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			defer delete(seen, id)
			walk(prefix, dt.Attribute())
		case *goaexpr.Object:
			for _, nat := range *dt {
				name := nat.Name
				path := name
				if prefix != "" {
					path = prefix + "." + name
				}
				walk(path, nat.Attribute)
			}
		case *goaexpr.Array:
			walk(prefix, dt.ElemType)
		case *goaexpr.Map:
			walk(prefix, dt.ElemType)
		case *goaexpr.Union:
			// Union branch payload types are discriminator-specific. The unqualified
			// {type,value} envelope path is intentionally not used as contract
			// metadata for branch values.
		}
	}
	walk("", att)
	if len(out) == 0 {
		return nil
	}
	return out
}

// generatedJSONType maps Goa types to the JSON type emitted by the generated schema.
func generatedJSONType(dt goaexpr.DataType) string {
	switch actual := dt.(type) {
	case goaexpr.UserType:
		return generatedJSONType(actual.Attribute().Type)
	case *goaexpr.Object, *goaexpr.Map, *goaexpr.Union:
		return jsonSchemaTypeObject
	case *goaexpr.Array:
		return "array"
	case goaexpr.Primitive:
		switch actual.Kind() {
		case goaexpr.BooleanKind:
			return "boolean"
		case goaexpr.StringKind, goaexpr.BytesKind:
			return "string"
		case goaexpr.IntKind,
			goaexpr.Int32Kind,
			goaexpr.Int64Kind,
			goaexpr.UIntKind,
			goaexpr.UInt32Kind,
			goaexpr.UInt64Kind:
			return jsonSchemaTypeInteger
		case goaexpr.Float32Kind,
			goaexpr.Float64Kind:
			return "number"
		case goaexpr.AnyKind:
			return "JSON value"
		case goaexpr.ArrayKind,
			goaexpr.ObjectKind,
			goaexpr.MapKind,
			goaexpr.UnionKind,
			goaexpr.UserTypeKind,
			goaexpr.ResultTypeKind:
			return ""
		}
	}
	return ""
}

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

// serviceName returns the declaring service name for a tool.
//
// Tool specs are provider-owned: they should identify the service that
// declares/implements the toolset, not the consuming agent service that happens
// to reference it.
func serviceName(tool *ToolData) string {
	ts := tool.Toolset
	if ts.SourceServiceName != "" {
		return ts.SourceServiceName
	}
	if ts.ServiceName != "" {
		return ts.ServiceName
	}
	return ""
}

// toolsetName returns the name of the toolset that contains the tool.
func toolsetName(tool *ToolData) string {
	return tool.Toolset.QualifiedName
}

// servicePkgAlias returns the import alias for the service package using the
// last path segment if available, falling back to the service PkgName.
func servicePkgAlias(svc *service.Data) string {
	// Always use the service package name so it matches the alias
	// used by Goa's NameScope when computing full type references.
	// Deriving the alias from the filesystem path (path.Base(PathName))
	// can diverge from the actual package identifier (e.g., underscores
	// vs. sanitized names), leading to mismatched qualifiers like
	// "atlasdataagent" vs "atlas_data_agent" in generated code.
	return svc.PkgName
}

// schemaVariantsForAttribute generates the annotated and plain OpenAPI JSON
// schema views for att from one Goa schema graph. The annotated view receives
// the root example supplied by the caller; the plain view clears only that root
// example so provider adapters can carry examples outside the schema without
// reprocessing JSON at runtime.
func schemaVariantsForAttribute(att *goaexpr.AttributeExpr, example any) ([]byte, []byte, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil, nil, nil
	}
	prev := openapi.Definitions
	openapi.Definitions = make(map[string]*openapi.Schema)
	defer func() { openapi.Definitions = prev }()
	schema := openapi.AttributeTypeSchema(goaexpr.Root.API, att)
	if schema == nil {
		return nil, nil, nil
	}
	if len(openapi.Definitions) > 0 {
		schema.Defs = openapi.Definitions
	}
	// Prefer a concrete root schema: for user types, inline the referenced
	// definition as the root so that the top-level contains "type":"object".
	// Retain definitions to allow nested $ref resolution.
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		// Compute type name
		tname := ""
		switch u := ut.(type) {
		case *goaexpr.UserTypeExpr:
			tname = u.TypeName
		case *goaexpr.ResultTypeExpr:
			tname = u.TypeName
		}
		if tname != "" {
			if def, ok := openapi.Definitions[tname]; ok && def != nil {
				// Build a new definitions map excluding the root to avoid
				// self-referential cycles during JSON marshaling.
				if len(openapi.Definitions) > 0 {
					defs := make(map[string]*openapi.Schema, len(openapi.Definitions))
					for k, v := range openapi.Definitions {
						if k == tname {
							continue
						}
						defs[k] = v
					}
					if len(defs) > 0 {
						def.Defs = defs
					}
				}
				return schemaVariantBytes(def, att, example)
			}
		}
	}
	return schemaVariantBytes(schema, att, example)
}

func schemaVariantBytes(schema *openapi.Schema, att *goaexpr.AttributeExpr, example any) ([]byte, []byte, error) {
	prevExample := schema.Example
	schema.Example = example
	annotated, err := schema.JSON()
	if err != nil {
		schema.Example = prevExample
		return annotated, nil, err
	}
	annotated, err = specializeUnionSchemas(annotated, att)
	if err != nil {
		schema.Example = prevExample
		return annotated, nil, err
	}
	schema.Example = nil
	plain, err := schema.JSON()
	schema.Example = prevExample
	if err != nil {
		return annotated, plain, err
	}
	plain, err = specializeUnionSchemas(plain, att)
	return annotated, plain, err
}

// specializeUnionSchemas rewrites Goa's generic OneOf schema projection into
// the discriminated JSON envelope generated codecs require. The owning contract
// is the generated union codec: callers must send {type:<variant>,
// value:<variant-payload>}. The schema keeps that envelope as an object so model
// providers see the payload field as JSON, while nested union values are still
// recursively specialized.
func specializeUnionSchemas(schemaBytes []byte, att *goaexpr.AttributeExpr) ([]byte, error) {
	if len(schemaBytes) == 0 || att == nil || !containsUnion(att) {
		return schemaBytes, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(schemaBytes, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal schema for union specialization: %w", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	if err := specializeUnionSchemaNode(att, doc, defs, map[string]struct{}{}); err != nil {
		return nil, err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal schema for union specialization: %w", err)
	}
	return out, nil
}

func containsUnion(att *goaexpr.AttributeExpr) bool {
	if att == nil || att.Type == nil {
		return false
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return containsUnion(dt.Attribute())
	case *goaexpr.Object:
		for _, nat := range *dt {
			if containsUnion(nat.Attribute) {
				return true
			}
		}
	case *goaexpr.Array:
		return containsUnion(dt.ElemType)
	case *goaexpr.Map:
		return containsUnion(dt.ElemType)
	case *goaexpr.Union:
		return true
	}
	return false
}

func specializeUnionSchemaNode(att *goaexpr.AttributeExpr, schema map[string]any, defs map[string]any, seen map[string]struct{}) error {
	if att == nil || att.Type == nil || len(schema) == 0 {
		return nil
	}
	if refName := schemaRefName(schema); refName != "" {
		if _, ok := seen[refName]; ok {
			return nil
		}
		defSchema, ok := defs[refName].(map[string]any)
		if !ok {
			return fmt.Errorf("schema ref %q for union specialization is missing from $defs", refName)
		}
		seen[refName] = struct{}{}
		defer delete(seen, refName)
		return specializeUnionSchemaNode(att, defSchema, defs, seen)
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return specializeUnionSchemaNode(dt.Attribute(), schema, defs, seen)
	case *goaexpr.Object:
		properties, _ := schema["properties"].(map[string]any)
		for _, nat := range *dt {
			if !containsUnion(nat.Attribute) {
				continue
			}
			childSchema, _ := properties[nat.Name].(map[string]any)
			if len(childSchema) == 0 {
				return fmt.Errorf("schema for union-bearing field %q is missing", nat.Name)
			}
			if err := specializeUnionSchemaNode(nat.Attribute, childSchema, defs, seen); err != nil {
				return err
			}
		}
	case *goaexpr.Array:
		if !containsUnion(dt.ElemType) {
			return nil
		}
		items, _ := schema["items"].(map[string]any)
		if len(items) == 0 {
			return fmt.Errorf("array schema for union-bearing elements is missing items")
		}
		return specializeUnionSchemaNode(dt.ElemType, items, defs, seen)
	case *goaexpr.Map:
		if !containsUnion(dt.ElemType) {
			return nil
		}
		values, _ := schema["additionalProperties"].(map[string]any)
		if len(values) == 0 {
			return fmt.Errorf("map schema for union-bearing values is missing additionalProperties")
		}
		return specializeUnionSchemaNode(dt.ElemType, values, defs, seen)
	case *goaexpr.Union:
		return rewriteUnionSchema(dt, schema, defs, seen)
	}
	return nil
}

func rewriteUnionSchema(union *goaexpr.Union, schema map[string]any, defs map[string]any, seen map[string]struct{}) error {
	typeKey := union.GetTypeKey()
	if typeKey == "" {
		typeKey = "type"
	}
	valueKey := union.GetValueKey()
	if valueKey == "" {
		valueKey = "value"
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return validateCanonicalUnionSchema(union, schema, defs, seen, typeKey, valueKey)
	}
	typeSchema, _ := properties[typeKey].(map[string]any)
	valueSchema, _ := properties[valueKey].(map[string]any)
	variants, _ := typeSchema["enum"].([]any)
	values, _ := valueSchema["anyOf"].([]any)
	if len(variants) != len(union.Values) || len(values) != len(union.Values) {
		return fmt.Errorf("union schema for %q has %d type variants and %d value variants, want %d", union.TypeName, len(variants), len(values), len(union.Values))
	}

	for i, nat := range union.Values {
		if nat == nil {
			return fmt.Errorf("union %q has nil variant %d", union.TypeName, i)
		}
		name, _ := variants[i].(string)
		if name != nat.Name {
			return fmt.Errorf("union schema variant %d for %q is %q, want %q", i, union.TypeName, name, nat.Name)
		}
		value, _ := values[i].(map[string]any)
		if len(value) == 0 {
			return fmt.Errorf("union schema variant %d for %q is missing value schema", i, union.TypeName)
		}
		if err := specializeUnionSchemaNode(nat.Attribute, value, defs, seen); err != nil {
			return err
		}
	}
	delete(schema, "example")
	schema["type"] = jsonSchemaTypeObject
	schema["properties"] = properties
	schema["required"] = []any{typeKey, valueKey}
	return nil
}

// validateCanonicalUnionSchema accepts a shared definition that was already
// specialized while walking another reference to the same Goa union.
func validateCanonicalUnionSchema(union *goaexpr.Union, schema map[string]any, defs map[string]any, seen map[string]struct{}, typeKey, valueKey string) error {
	oneOf, _ := schema["oneOf"].([]any)
	if len(oneOf) != len(union.Values) {
		return fmt.Errorf("union schema for %q is missing canonical properties and has %d canonical variants, want %d", union.TypeName, len(oneOf), len(union.Values))
	}
	for i, nat := range union.Values {
		branch, _ := oneOf[i].(map[string]any)
		properties, _ := branch["properties"].(map[string]any)
		typeSchema, _ := properties[typeKey].(map[string]any)
		valueSchema, ok := properties[valueKey].(map[string]any)
		variants, _ := typeSchema["enum"].([]any)
		if len(variants) != 1 || variants[0] != nat.Name || !ok {
			return fmt.Errorf("union schema canonical variant %d for %q does not match %q", i, union.TypeName, nat.Name)
		}
		if err := specializeUnionSchemaNode(nat.Attribute, valueSchema, defs, seen); err != nil {
			return err
		}
	}
	return nil
}

func schemaRefName(schema map[string]any) string {
	ref, _ := schema["$ref"].(string)
	if ref == "" || !strings.HasPrefix(ref, "#/$defs/") {
		return ""
	}
	return strings.TrimPrefix(ref, "#/$defs/")
}

// authoredExampleForAttribute returns the last explicit Example(...) declared
// on the source attribute, normalized to the canonical JSON contract of target.
func authoredExampleForAttribute(source, target *goaexpr.AttributeExpr) *exampleData {
	if source == nil {
		return nil
	}
	examples := source.ExtractUserExamples()
	if len(examples) == 0 {
		return nil
	}
	return normalizeExampleValue(target, examples[len(examples)-1].Value)
}

// normalizeExampleValue canonicalizes one example value into JSON-native shapes
// and rewrites union nodes to the canonical {type,value} encoding.
func normalizeExampleValue(att *goaexpr.AttributeExpr, v any) *exampleData {
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
	normalized = canonicalizeUnionExamples(att, normalized)
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

// canonicalizeUnionExamples rewrites Goa's "flattened" union examples into the
// canonical JSON shape required by Goa-generated codecs: {type,value}.
//
// Goa's Union.Example returns only the selected branch value, which is useful
// for documentation but misleading for tool specs where the runtime decoder
// expects explicit discriminators. This helper preserves the structure produced
// by the standard example generator and wraps only union nodes.
func canonicalizeUnionExamples(att *goaexpr.AttributeExpr, example any) any {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return example
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return canonicalizeUnionExamples(dt.Attribute(), example)
	case *goaexpr.Object:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		for k, v := range m {
			child := att.Find(k)
			m[k] = canonicalizeUnionExamples(child, v)
		}
		return m
	case *goaexpr.Array:
		s, ok := example.([]any)
		if !ok {
			return example
		}
		for i, v := range s {
			s[i] = canonicalizeUnionExamples(dt.ElemType, v)
		}
		return s
	case *goaexpr.Map:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		for k, v := range m {
			m[k] = canonicalizeUnionExamples(dt.ElemType, v)
		}
		return m
	case *goaexpr.Union:
		if example == nil || len(dt.Values) == 0 {
			return example
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
		if canonical, ok := canonicalUnionExample(dt, example, typeKey, valueKey); ok {
			return canonical
		}
		chosen = pickUnionVariantForExample(dt, example)
		if chosen == nil {
			panic(fmt.Sprintf("agent/specs_builder: union example does not match any variant (type=%q)", dt.TypeName))
		}

		return map[string]any{
			typeKey:  chosen.Name,
			valueKey: canonicalizeUnionExamples(chosen.Attribute, example),
		}
	default:
		return example
	}
}

// canonicalUnionExample returns an already-tagged union example unchanged except
// for normalizing nested unions inside the selected variant value.
func canonicalUnionExample(u *goaexpr.Union, example any, typeKey, valueKey string) (any, bool) {
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
	return map[string]any{
		typeKey:  typeName,
		valueKey: canonicalizeUnionExamples(chosen.Attribute, value),
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
