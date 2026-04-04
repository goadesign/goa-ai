// Package openai handles provider-visible OpenAI Responses API tool and
// structured-output configuration. Canonical tool IDs stay inside goa-ai; only
// sanitized names cross the provider boundary.
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"

	"goa.design/goa-ai/runtime/agent/model"
)

const structuredOutputDefaultName = "structured_output"

func encodeTools(defs []*model.ToolDefinition) ([]responses.ToolUnionParam, map[string]string, map[string]string, error) {
	if len(defs) == 0 {
		return nil, nil, nil, nil
	}
	tools := make([]responses.ToolUnionParam, 0, len(defs))
	canonicalToProvider := make(map[string]string, len(defs))
	providerToCanonical := make(map[string]string, len(defs))
	for i, def := range defs {
		if def == nil {
			return nil, nil, nil, fmt.Errorf("openai: tool[%d] is nil", i)
		}
		if def.Name == "" {
			return nil, nil, nil, fmt.Errorf("openai: tool[%d] is missing name", i)
		}
		if def.Description == "" {
			return nil, nil, nil, fmt.Errorf("openai: tool %q is missing description", def.Name)
		}
		providerName := SanitizeToolName(def.Name)
		if previous, ok := providerToCanonical[providerName]; ok && previous != def.Name {
			return nil, nil, nil, fmt.Errorf(
				"openai: tool %q sanitizes to %q which collides with %q",
				def.Name,
				providerName,
				previous,
			)
		}
		parameters, err := toolInputSchema(def.InputSchema)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("openai: tool %q schema: %w", def.Name, err)
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        providerName,
				Description: param.NewOpt(def.Description),
				Parameters:  parameters,
				Strict:      param.NewOpt(true),
			},
		})
		canonicalToProvider[def.Name] = providerName
		providerToCanonical[providerName] = def.Name
	}
	if len(tools) == 0 {
		return nil, nil, nil, nil
	}
	return tools, canonicalToProvider, providerToCanonical, nil
}

func encodeToolChoice(
	choice *model.ToolChoice,
	canonicalToProvider map[string]string,
) (responses.ResponseNewParamsToolChoiceUnion, bool, error) {
	if choice == nil {
		return responses.ResponseNewParamsToolChoiceUnion{}, false, nil
	}
	switch choice.Mode {
	case "", model.ToolChoiceModeAuto:
		return responses.ResponseNewParamsToolChoiceUnion{}, false, nil
	case model.ToolChoiceModeNone:
		return responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsNone),
		}, true, nil
	case model.ToolChoiceModeAny:
		if len(canonicalToProvider) == 0 {
			return responses.ResponseNewParamsToolChoiceUnion{}, false, errors.New(
				"openai: tool choice mode \"any\" requires tool definitions",
			)
		}
		return responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired),
		}, true, nil
	case model.ToolChoiceModeTool:
		if choice.Name == "" {
			return responses.ResponseNewParamsToolChoiceUnion{}, false, errors.New(
				"openai: tool choice mode \"tool\" requires a tool name",
			)
		}
		providerName, ok := canonicalToProvider[choice.Name]
		if !ok {
			return responses.ResponseNewParamsToolChoiceUnion{}, false, fmt.Errorf(
				"openai: tool choice name %q does not match any tool",
				choice.Name,
			)
		}
		return responses.ResponseNewParamsToolChoiceUnion{
			OfFunctionTool: &responses.ToolChoiceFunctionParam{
				Name: providerName,
			},
		}, true, nil
	default:
		return responses.ResponseNewParamsToolChoiceUnion{}, false, fmt.Errorf(
			"openai: unsupported tool choice mode %q",
			choice.Mode,
		)
	}
}

func encodeStructuredOutput(output *model.StructuredOutput) (responses.ResponseTextConfigParam, bool, error) {
	if output == nil {
		return responses.ResponseTextConfigParam{}, false, nil
	}
	schema := bytes.TrimSpace(output.Schema)
	if len(schema) == 0 {
		return responses.ResponseTextConfigParam{}, false, errors.New(
			"openai: structured output schema is required",
		)
	}
	if !json.Valid(schema) {
		return responses.ResponseTextConfigParam{}, false, errors.New(
			"openai: structured output schema is not valid JSON",
		)
	}
	parameters, err := toolInputSchema(schema)
	if err != nil {
		return responses.ResponseTextConfigParam{}, false, fmt.Errorf(
			"openai: structured output schema: %w",
			err,
		)
	}
	name := structuredOutputDefaultName
	if output.Name != "" {
		name = output.Name
	}
	name = SanitizeToolName(name)
	return responses.ResponseTextConfigParam{
		Format: responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
				Name:   name,
				Schema: parameters,
				Strict: param.NewOpt(true),
			},
		},
	}, true, nil
}

func toolInputSchema(schema any) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}
	var data []byte
	switch actual := schema.(type) {
	case json.RawMessage:
		data = actual
	case []byte:
		data = actual
	default:
		encoded, err := json.Marshal(actual)
		if err != nil {
			return nil, err
		}
		data = encoded
	}
	if len(data) == 0 {
		return nil, nil
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON schema")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
