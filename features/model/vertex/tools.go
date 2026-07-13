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
// FunctionDeclaration per definition. Schemas are passed through as raw
// JSON schema (ParametersJsonSchema) after normalization. Definitions
// without a description are rejected, matching the Bedrock provider's
// convention rather than silently sending empty descriptions to Gemini.
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
		schema, err := normalizeSchema(def.Input.JSONSchema())
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
	if len(raw) == 0 {
		return map[string]any{"type": "object"}, nil
	}
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
