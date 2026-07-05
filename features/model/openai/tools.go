// Package openai handles provider-visible OpenAI Responses API tool and
// structured-output configuration. Canonical tool IDs stay inside goa-ai; only
// sanitized names cross the provider boundary, and tool input schemas cross it
// in strict-mode projected form (see strict_schema.go).
package openai

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// toolCodec carries the reversible per-request tool projection state: the
// sanitized names exchanged with OpenAI in both directions and the canonical
// input schemas needed to canonicalize strict-mode tool arguments on the way
// back. Methods tolerate a nil receiver so translation paths need no tool
// bookkeeping when a request declares no tools.
type toolCodec struct {
	canonicalToProvider map[string]string
	providerToCanonical map[string]string
	canonicalSchemas    map[string]rawjson.Message
}

const structuredOutputDefaultName = "structured_output"

func encodeTools(defs []*model.ToolDefinition) ([]responses.ToolUnionParam, *toolCodec, error) {
	if len(defs) == 0 {
		return nil, nil, nil
	}
	tools := make([]responses.ToolUnionParam, 0, len(defs))
	codec := &toolCodec{
		canonicalToProvider: make(map[string]string, len(defs)),
		providerToCanonical: make(map[string]string, len(defs)),
		canonicalSchemas:    make(map[string]rawjson.Message, len(defs)),
	}
	for i, def := range defs {
		if def == nil {
			return nil, nil, fmt.Errorf("openai: tool[%d] is nil", i)
		}
		if def.Name == "" {
			return nil, nil, fmt.Errorf("openai: tool[%d] is missing name", i)
		}
		if def.Description == "" {
			return nil, nil, fmt.Errorf("openai: tool %q is missing description", def.Name)
		}
		providerName := SanitizeToolName(def.Name)
		if previous, ok := codec.providerToCanonical[providerName]; ok && previous != def.Name {
			return nil, nil, fmt.Errorf(
				"openai: tool %q sanitizes to %q which collides with %q",
				def.Name,
				providerName,
				previous,
			)
		}
		schema := def.Input.JSONSchema()
		parameters, err := projectStrictSchema(schema)
		if err != nil {
			return nil, nil, fmt.Errorf("openai: tool %q schema: %w", def.Name, err)
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        providerName,
				Description: param.NewOpt(def.Description),
				Parameters:  parameters,
				Strict:      param.NewOpt(true),
			},
		})
		codec.canonicalToProvider[def.Name] = providerName
		codec.providerToCanonical[providerName] = def.Name
		codec.canonicalSchemas[def.Name] = schema
	}
	return tools, codec, nil
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
	name := structuredOutputDefaultName
	if output.Name != "" {
		name = output.Name
	}
	name = SanitizeToolName(name)
	parameters, err := projectStrictSchema(rawjson.Message(schema))
	if err != nil {
		return responses.ResponseTextConfigParam{}, false, fmt.Errorf(
			"openai: structured output %q schema: %w",
			name,
			err,
		)
	}
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

// canonicalName maps a provider-visible tool name back to its canonical
// goa-ai identifier. Unknown names pass through unchanged.
func (c *toolCodec) canonicalName(providerName string) string {
	if c == nil {
		return providerName
	}
	if canonical, ok := c.providerToCanonical[providerName]; ok {
		return canonical
	}
	return providerName
}

// canonicalSchema returns the canonical input schema recorded for a canonical
// tool name, or nil when the request declared no such tool.
func (c *toolCodec) canonicalSchema(canonicalName string) rawjson.Message {
	if c == nil {
		return nil
	}
	return c.canonicalSchemas[canonicalName]
}

// providerNames returns the canonical-to-provider name mapping used when
// encoding request messages and tool choices. Nil when no tools are declared.
func (c *toolCodec) providerNames() map[string]string {
	if c == nil {
		return nil
	}
	return c.canonicalToProvider
}
