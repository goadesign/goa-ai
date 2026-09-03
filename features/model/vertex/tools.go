package vertex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// encodeTools declares the request's tools as one genai.Tool with a
// FunctionDeclaration per definition. Each complete schema is prepared for
// Gemini's smaller function-schema vocabulary; the validated model client
// still applies every original rule Gemini cannot express.
// Definitions without a description are rejected, matching the Bedrock
// provider's convention rather than silently sending empty descriptions to
// Gemini.
func encodeTools(defs []*model.ToolDefinition, canonToProv map[string]string) ([]*genai.Tool, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		prov, ok := canonToProv[def.Name]
		if !ok {
			return nil, fmt.Errorf("vertex: tool %q has no provider name", def.Name)
		}
		if def.Description == "" {
			return nil, fmt.Errorf("vertex: tool %q requires a description", def.Name)
		}
		schema, err := normalizeToolSchema(def.Input.Contract().Schema)
		if err != nil {
			return nil, fmt.Errorf("vertex: tool %q schema: %w", def.Name, err)
		}
		decls = append(decls, &genai.FunctionDeclaration{
			Name:                 prov,
			Description:          def.Description,
			ParametersJsonSchema: schema,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}

// normalizeToolSchema prepares a complete JSON Schema for Gemini function
// arguments. It removes only rules and labels the validated model client checks
// after Gemini returns a call; an unknown keyword fails instead of being
// reinterpreted.
func normalizeToolSchema(raw []byte) (any, error) {
	schema, err := normalizeSchema(raw)
	if err != nil {
		return nil, err
	}
	if err := prepareGeminiToolSchema(schema); err != nil {
		return nil, err
	}
	return schema, nil
}

// encodeToolConfig maps the goa-ai tool choice through the request's bijective
// name map. A specific tool must be declared in the same request.
func encodeToolConfig(choice *model.ToolChoice, canonToProv map[string]string) (*genai.ToolConfig, error) {
	fcc := &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}
	if choice != nil {
		switch choice.Mode {
		case model.ToolChoiceModeAuto:
			// Already the default; nothing to change.
		case model.ToolChoiceModeNone:
			fcc.Mode = genai.FunctionCallingConfigModeNone
		case model.ToolChoiceModeAny:
			fcc.Mode = genai.FunctionCallingConfigModeAny
		case model.ToolChoiceModeTool:
			fcc.Mode = genai.FunctionCallingConfigModeAny
			prov, ok := canonToProv[choice.Name]
			if !ok {
				return nil, fmt.Errorf("vertex: tool choice %q is not declared in the request", choice.Name)
			}
			fcc.AllowedFunctionNames = []string{prov}
		}
	}
	return &genai.ToolConfig{FunctionCallingConfig: fcc}, nil
}

// normalizeSchema prepares a goa-ai JSON schema for Gemini: it parses the
// raw bytes and drops metadata keywords Gemini rejects ($schema, $id) plus
// root-level examples, which goa-ai conveys separately.
func normalizeSchema(raw []byte) (any, error) {
	if !json.Valid(raw) {
		return nil, errors.New("invalid JSON schema")
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		return nil, err
	}
	if schema == nil {
		return nil, errors.New("JSON schema must be an object")
	}
	delete(schema, "$schema")
	delete(schema, "$id")
	delete(schema, "example")
	delete(schema, "examples")
	return schema, nil
}

// prepareGeminiToolSchema mutates one parsed schema after normalizeSchema has
// established that its root is an object. Properties and definitions contain
// arbitrary names, so only their child values are interpreted as schemas.
func prepareGeminiToolSchema(schema any) error {
	object, ok := schema.(map[string]any)
	if !ok {
		return errors.New("gemini tool schema must be an object")
	}
	if oneOf, hasOneOf := object["oneOf"]; hasOneOf {
		if _, hasAnyOf := object["anyOf"]; hasAnyOf {
			return errors.New("gemini tool schema cannot contain both \"oneOf\" and \"anyOf\"")
		}
		object["anyOf"] = oneOf
		delete(object, "oneOf")
	}
	for keyword, value := range object {
		switch keyword {
		case "$defs", "properties":
			children, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("gemini tool schema %q must be an object", keyword)
			}
			for _, child := range children {
				if err := prepareGeminiToolSchema(child); err != nil {
					return err
				}
			}
		case "items":
			if err := prepareGeminiToolSchema(value); err != nil {
				return err
			}
		case "additionalProperties":
			if _, ok := value.(bool); ok {
				continue
			}
			if err := prepareGeminiToolSchema(value); err != nil {
				return err
			}
		case "anyOf":
			choices, ok := value.([]any)
			if !ok {
				return errors.New("gemini tool schema \"anyOf\" must be an array")
			}
			for _, choice := range choices {
				if err := prepareGeminiToolSchema(choice); err != nil {
					return err
				}
			}
		case "$ref", "type", "nullable", "required", "format", "description", "enum", "propertyOrdering":
			// Gemini accepts these keywords unchanged.
		case "$schema", "$id", "title", "example", "examples",
			"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
			"minLength", "maxLength", "pattern",
			"minItems", "maxItems", "uniqueItems",
			"minProperties", "maxProperties":
			delete(object, keyword)
		default:
			return fmt.Errorf("gemini tool schema keyword %q is unsupported", keyword)
		}
	}
	return nil
}
