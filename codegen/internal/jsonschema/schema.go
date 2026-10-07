// Package jsonschema builds the JSON contracts used by MCP catalogs and agent
// tool specifications. Goa owns types and validation; this package preserves
// local definitions and aligns object names and union branches with the generated
// JSON codecs. It never fetches remote schemas or invents model examples.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	goaexpr "goa.design/goa/v3/expr"
	openapiv2 "goa.design/goa/v3/http/codegen/openapi/v2"
)

const (
	jsonSchemaTypeObject = "object"
	unionTypeKeyDefault  = "type"
	unionValueKeyDefault = "value"
)

// Build returns a JSON Schema 2020-12 contract for one attribute graph. Named
// recursion remains in local $defs; synthesized OpenAPI examples are removed.
func Build(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, identity goaexpr.ExampleIdentity) ([]byte, error) {
	return build(api, att, identity, true)
}

// build uses the same authored fields and constraints for tool and form
// contracts. Only tool objects receive the generated unknown-field rejection.
func build(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, identity goaexpr.ExampleIdentity, closeObjects bool) ([]byte, error) {
	if _, err := jsonshape.Build(att); err != nil {
		return nil, err
	}
	generator := goaexpr.NewExampleGenerator(api.RandomizerFactory).At(identity)
	schema := openapiv2.BuildAttributeSchema(api, att, generator)
	encoded, err := schema.JSON()
	if err != nil {
		return nil, fmt.Errorf("encode Goa schema: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode Goa schema: %w", err)
	}
	// A named root has its shape in $defs and its caller-authored constraints
	// beside $ref. Copy the shape first, then retain those outer constraints.
	if name := schemaRefName(document); name != "" {
		defs, _ := document["$defs"].(map[string]any)
		definition, ok := defs[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema root %q is missing from $defs", name)
		}
		root := make(map[string]any, len(definition)+len(document))
		for key, value := range definition {
			root[key] = value
		}
		for key, value := range document {
			if key != "$ref" {
				root[key] = value
			}
		}
		document = root
	}
	removeExamples(document)
	defs, _ := document["$defs"].(map[string]any)
	if err := alignSchemaNodeWithGeneratedDecoder(att, document, defs, make(map[string]struct{}), closeObjects); err != nil {
		return nil, err
	}
	if !closeObjects {
		// MCP forms have no root title or description. Remove Goa's object
		// documentation while keeping each field's title and description.
		delete(document, "description")
		delete(document, "title")
	}
	document["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	encoded, err = json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode JSON contract: %w", err)
	}
	return encoded, nil
}

// alignSchemaNodeWithGeneratedDecoder updates one schema node using its Goa type.
// Named types are followed through $defs while seen prevents recursive types
// from visiting the same definition forever.
func alignSchemaNodeWithGeneratedDecoder(att *goaexpr.AttributeExpr, schema map[string]any, defs map[string]any, seen map[string]struct{}, closeObjects bool) error {
	if att == nil || att.Type == nil || len(schema) == 0 {
		return nil
	}
	if refName := schemaRefName(schema); refName != "" {
		if _, ok := seen[refName]; ok {
			return nil
		}
		defSchema, ok := defs[refName].(map[string]any)
		if !ok {
			return fmt.Errorf("schema ref %q for generated decoder is missing from $defs", refName)
		}
		seen[refName] = struct{}{}
		defer delete(seen, refName)
		return alignSchemaNodeWithGeneratedDecoder(att, defSchema, defs, seen, closeObjects)
	}
	if att.Description != "" {
		schema["description"] = att.Description
	}
	if header, ok := att.Meta["mcp:header"]; ok {
		if len(header) != 1 {
			return fmt.Errorf("mcp:header requires exactly one HTTP field-name token")
		}
		schema["x-mcp-header"] = header[0]
	}
	switch dt := att.Type.(type) {
	case goaexpr.Primitive:
		if !closeObjects && (schema["type"] == "integer" || schema["type"] == "number") {
			// OpenAPI adds numeric representation hints that MCP forms do not
			// accept. Authored formats remain visible and must pass the contract.
			validation := goaexpr.EffectiveValidation(att)
			if validation == nil || validation.Format == "" {
				delete(schema, "format")
			}
		}
	case goaexpr.UserType:
		// Named attributes may add required fields at the use site. Merge those
		// constraints into a private copy; the original Goa graph stays intact.
		effective := *dt.Attribute()
		if att.Validation != nil {
			if effective.Validation == nil {
				effective.Validation = att.Validation.Dup()
			} else {
				effective.Validation = effective.Validation.Dup()
				effective.Validation.Merge(att.Validation)
			}
		}
		return alignSchemaNodeWithGeneratedDecoder(&effective, schema, defs, seen, closeObjects)
	case *goaexpr.Object:
		if closeObjects {
			schema["additionalProperties"] = false
		} else if len(*dt) == 0 {
			// MCP requires properties even when this authored form has no fields.
			// The empty object lets the host return an accepted empty form.
			schema["properties"] = map[string]any{}
		}
		properties, _ := schema["properties"].(map[string]any)
		wireProperties := make(map[string]any, len(properties))
		required := make([]string, 0, len(att.AllRequired()))
		for _, nat := range *dt {
			name, visible := jsonshape.FieldName(nat)
			childSchema, ok := properties[nat.Name].(map[string]any)
			if !ok {
				return fmt.Errorf("schema for field %q is missing", nat.Name)
			}
			if !visible {
				continue
			}
			wireProperties[name] = childSchema
			if att.IsRequired(nat.Name) {
				required = append(required, name)
			}
			if err := alignSchemaNodeWithGeneratedDecoder(nat.Attribute, childSchema, defs, seen, closeObjects); err != nil {
				return err
			}
		}
		schema["properties"] = wireProperties
		if len(required) > 0 {
			schema["required"] = required
		} else {
			delete(schema, "required")
		}
	case *goaexpr.Array:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("array schema is missing items")
		}
		return alignSchemaNodeWithGeneratedDecoder(dt.ElemType, items, defs, seen, closeObjects)
	case *goaexpr.Map:
		values, ok := schema["additionalProperties"].(map[string]any)
		if !ok {
			primitive, primitiveOK := jsonshape.PrimitiveType(dt.ElemType)
			if primitiveOK && primitive.Kind() == goaexpr.AnyKind {
				return nil
			}
			return fmt.Errorf("map schema is missing additionalProperties")
		}
		return alignSchemaNodeWithGeneratedDecoder(dt.ElemType, values, defs, seen, closeObjects)
	case *goaexpr.Union:
		return rewriteUnionSchema(dt, schema, defs, seen, closeObjects)
	}
	return nil
}

func rewriteUnionSchema(union *goaexpr.Union, schema map[string]any, defs map[string]any, seen map[string]struct{}, closeObjects bool) error {
	typeKey := union.GetTypeKey()
	if typeKey == "" {
		typeKey = unionTypeKeyDefault
	}
	valueKey := union.GetValueKey()
	if valueKey == "" {
		valueKey = unionValueKeyDefault
	}
	branches, _ := schema["oneOf"].([]any)
	if len(branches) == 0 {
		branches, _ = schema["anyOf"].([]any)
	}
	if len(branches) != len(union.Values) {
		return fmt.Errorf("union schema for %q has %d correlated variants, want %d", union.TypeName, len(branches), len(union.Values))
	}

	for i, nat := range union.Values {
		if nat == nil {
			return fmt.Errorf("union %q has nil variant %d", union.TypeName, i)
		}
		branch, _ := branches[i].(map[string]any)
		properties, _ := branch["properties"].(map[string]any)
		typeSchema, _ := properties[typeKey].(map[string]any)
		valueSchema, ok := properties[valueKey].(map[string]any)
		variants, _ := typeSchema["enum"].([]any)
		if len(variants) != 1 || variants[0] != nat.Name || (!union.Flatten && !ok) {
			return fmt.Errorf("union schema variant %d for %q does not match %q", i, union.TypeName, nat.Name)
		}
		if nat.Attribute.Description != "" {
			branch["description"] = nat.Attribute.Description
		}
		if closeObjects {
			branch["additionalProperties"] = false
		}
		if union.Flatten {
			if err := alignSchemaNodeWithGeneratedDecoder(nat.Attribute, branch, defs, seen, closeObjects); err != nil {
				return err
			}
			required, _ := branch["required"].([]string)
			branch["required"] = append(required, typeKey)
			continue
		}
		if err := alignSchemaNodeWithGeneratedDecoder(nat.Attribute, valueSchema, defs, seen, closeObjects); err != nil {
			return err
		}
	}
	delete(schema, "example")
	delete(schema, "anyOf")
	delete(schema, "properties")
	delete(schema, "required")
	schema["type"] = jsonSchemaTypeObject
	schema["oneOf"] = branches
	return nil
}

func schemaRefName(schema map[string]any) string {
	ref, _ := schema["$ref"].(string)
	if ref == "" || !strings.HasPrefix(ref, "#/$defs/") {
		return ""
	}
	return strings.TrimPrefix(ref, "#/$defs/")
}

// removeExamples strips values created by Goa's example generator; callers
// restore only examples explicitly authored for the corresponding contract.
func removeExamples(node any) {
	switch value := node.(type) {
	case map[string]any:
		delete(value, "example")
		for _, child := range value {
			removeExamples(child)
		}
	case []any:
		for _, child := range value {
			removeExamples(child)
		}
	}
}
