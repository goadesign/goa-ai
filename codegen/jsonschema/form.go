// Package jsonschema emits MCP's flat form schemas through the shared Goa schema builder.
// Primitive aliases are written inline because forms do not support references;
// rules outside the protocol's subset are rejected rather than discarded.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	contract "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// BuildForm preserves authored form fields, JSON names, defaults and constraints.
// The returned schema follows MCP's restricted form contract; unsupported Goa
// rules fail generation instead of producing a weaker host-visible schema.
func BuildForm(api *expr.APIExpr, attribute *expr.AttributeExpr, identity expr.ExampleIdentity) ([]byte, error) {
	if err := validateFormFieldTags(attribute); err != nil {
		return nil, err
	}
	encoded, err := build(api, attribute, identity, false)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode generated form schema: %w", err)
	}
	definitions, _ := document["$defs"].(map[string]any)
	delete(document, "$defs")
	if err := inlineFormReferences(document, definitions, make(map[string]bool)); err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode generated form schema: %w", err)
	}
	if err := contract.ValidateForm(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// validateFormFieldTags rejects hidden fields and encoding options that change
// the primitive JSON values advertised to a form host. Omission options do not
// change decoding, and authored field names remain governed by Goa.
func validateFormFieldTags(attribute *expr.AttributeExpr) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("unsupported form schema: content must be an object")
	}
	for _, field := range *object {
		if _, visible := jsonshape.FieldName(field); !visible {
			return fmt.Errorf("unsupported form schema: field %q is hidden from JSON", field.Name)
		}
		tags := strings.Trim(codegen.AttributeTagsWithName(nil, "", field.Attribute), " `")
		tag := reflect.StructTag(tags).Get("json")
		_, options, _ := strings.Cut(tag, ",")
		for _, option := range strings.Split(options, ",") {
			switch option {
			case "", "omitempty", "omitzero":
			default:
				return fmt.Errorf("unsupported form schema: field %q uses JSON encoding option %q", field.Name, option)
			}
		}
	}
	return nil
}

// inlineFormReferences copies a named field's schema into its use site while
// retaining that site's authored constraints. Cycles cannot form a flat form.
func inlineFormReferences(node any, definitions map[string]any, active map[string]bool) error {
	switch value := node.(type) {
	case map[string]any:
		if name := schemaRefName(value); name != "" {
			if active[name] {
				return fmt.Errorf("MCP form cannot contain recursive type %q", name)
			}
			definition, ok := definitions[name].(map[string]any)
			if !ok {
				return fmt.Errorf("form schema definition %q is missing", name)
			}
			encoded, err := json.Marshal(definition)
			if err != nil {
				return fmt.Errorf("copy form schema definition %q: %w", name, err)
			}
			var copied map[string]any
			if err := json.Unmarshal(encoded, &copied); err != nil {
				return fmt.Errorf("copy form schema definition %q: %w", name, err)
			}
			for key, constraint := range value {
				if key != "$ref" {
					copied[key] = constraint
				}
			}
			clear(value)
			for key, constraint := range copied {
				value[key] = constraint
			}
			active[name] = true
			defer delete(active, name)
		}
		for _, child := range value {
			if err := inlineFormReferences(child, definitions, active); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := inlineFormReferences(child, definitions, active); err != nil {
				return err
			}
		}
	}
	return nil
}
