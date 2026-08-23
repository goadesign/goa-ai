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

	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// toolCodec carries the reversible per-request tool-name projection used to
// reject any provider tool name that was not advertised by this request.
type toolCodec struct {
	canonicalToProvider map[string]string
	providerToCanonical map[string]string
}

func encodeTools(defs []*model.ToolDefinition) ([]responses.ToolUnionParam, *toolCodec, error) {
	if len(defs) == 0 {
		return nil, nil, nil
	}
	canonToProv, provToCanon, err := toolname.BuildMaps(defs)
	if err != nil {
		return nil, nil, fmt.Errorf("openai: %w", err)
	}
	tools := make([]responses.ToolUnionParam, 0, len(defs))
	codec := &toolCodec{
		canonicalToProvider: canonToProv,
		providerToCanonical: provToCanon,
	}
	for _, def := range defs {
		if def.Description == "" {
			return nil, nil, fmt.Errorf("openai: tool %q is missing description", def.Name)
		}
		schema := def.Input.Contract().Schema
		parameters, err := projectStrictSchema(schema)
		if err != nil {
			return nil, nil, fmt.Errorf("openai: tool %q schema: %w", def.Name, err)
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        canonToProv[def.Name],
				Description: param.NewOpt(def.Description),
				Parameters:  parameters,
				Strict:      param.NewOpt(true),
			},
		})
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
	name := toolname.Sanitize(output.Name)
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

// canonicalName maps an advertised provider-visible tool name back to its
// canonical goa-ai identifier.
func (c *toolCodec) canonicalName(providerName string) (string, bool) {
	if c == nil {
		return "", false
	}
	canonical, ok := c.providerToCanonical[providerName]
	return canonical, ok
}

// providerNames returns the canonical-to-provider name mapping used when
// encoding request messages and tool choices. Nil when no tools are declared.
func (c *toolCodec) providerNames() map[string]string {
	if c == nil {
		return nil
	}
	return c.canonicalToProvider
}
