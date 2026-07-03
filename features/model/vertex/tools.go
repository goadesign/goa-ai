package vertex

import (
	"encoding/json"
	"fmt"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// encodeTools declares the request's tools as one genai.Tool with a
// FunctionDeclaration per definition. Schemas are passed through as raw
// JSON schema (ParametersJsonSchema) after normalization.
func encodeTools(defs []*model.ToolDefinition, canonToProv map[string]string) ([]*genai.Tool, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		prov, ok := canonToProv[def.Name]
		if !ok {
			continue // shadowed by sanitization collision
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

// encodeToolConfig maps the goa-ai tool choice onto Gemini's function
// calling config. A specific tool is expressed as mode ANY restricted to
// that tool's sanitized name.
func encodeToolConfig(choice *model.ToolChoice, canonToProv map[string]string) *genai.ToolConfig {
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
			if prov, ok := canonToProv[choice.Name]; ok {
				fcc.AllowedFunctionNames = []string{prov}
			} else {
				fcc.AllowedFunctionNames = []string{sanitizeToolName(choice.Name)}
			}
		}
	}
	return &genai.ToolConfig{FunctionCallingConfig: fcc}
}

// normalizeSchema prepares a goa-ai JSON schema for Gemini: it parses the
// raw bytes and drops metadata keywords Gemini rejects ($schema, $id) plus
// root-level examples, which goa-ai conveys separately.
func normalizeSchema(raw []byte) (any, error) {
	if len(raw) == 0 {
		return map[string]any{"type": "object"}, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	delete(schema, "$schema")
	delete(schema, "$id")
	delete(schema, "example")
	delete(schema, "examples")
	return schema, nil
}
