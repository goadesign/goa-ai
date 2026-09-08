// Package openai handles provider-visible OpenAI Responses API tool and
// structured-output configuration. Canonical tool IDs stay inside goa-ai; only
// sanitized names cross the provider boundary. Direct OpenAI projects schemas
// into strict form (see strict_schema.go); Bedrock preserves the exact schema
// with strict:false and relies on the validated client to reject invalid output.
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// toolCodec carries the reversible per-request tool-name projection used to
// reject any provider tool name that was not advertised by this request.
type toolCodec struct {
	canonicalToProvider map[string]string
	providerToCanonical map[string]string
	projections         map[string]*strictSchemaProjection
	// exact preserves the original provider arguments without strict-mode
	// null removal. The validated model client still checks the full schema.
	exact bool
}

func encodeTools(defs []*model.ToolDefinition, modelID string, exact bool) ([]responses.ToolUnionParam, *toolCodec, error) {
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
		projections:         make(map[string]*strictSchemaProjection, len(defs)),
		exact:               exact,
	}
	for _, def := range defs {
		if def.Description == "" {
			return nil, nil, fmt.Errorf("openai: tool %q is missing description", def.Name)
		}
		schema := def.Input.Contract().Schema
		providerName := canonToProv[def.Name]
		if !exact {
			projection, err := compileStrictSchemaForModel(schema, modelID)
			if err != nil {
				return nil, nil, fmt.Errorf("openai: tool %q schema: %w", def.Name, err)
			}
			codec.projections[providerName] = projection
			schema, err = json.Marshal(projection.schema)
			if err != nil {
				return nil, nil, fmt.Errorf("openai: tool %q projected schema: %w", def.Name, err)
			}
		}
		parameters, err := sdkSchema(schema)
		if err != nil {
			return nil, nil, fmt.Errorf("openai: tool %q schema: %w", def.Name, err)
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        providerName,
				Description: param.NewOpt(def.Description),
				Parameters:  parameters,
				Strict:      param.NewOpt(!exact),
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

func encodeStructuredOutput(
	output *model.StructuredOutput,
	modelID string,
) (responses.ResponseTextConfigParam, *strictSchemaProjection, bool, error) {
	if output == nil {
		return responses.ResponseTextConfigParam{}, nil, false, nil
	}
	schema := bytes.TrimSpace(output.Schema)
	if len(schema) == 0 {
		return responses.ResponseTextConfigParam{}, nil, false, errors.New(
			"openai: structured output schema is required",
		)
	}
	name := toolname.Sanitize(output.Name)
	projection, err := compileStrictSchemaForModel(rawjson.Message(schema), modelID)
	if err != nil {
		return responses.ResponseTextConfigParam{}, nil, false, fmt.Errorf(
			"openai: structured output %q schema: %w",
			name,
			err,
		)
	}
	encoded, err := json.Marshal(projection.schema)
	if err != nil {
		return responses.ResponseTextConfigParam{}, nil, false, err
	}
	parameters, err := sdkSchema(encoded)
	if err != nil {
		return responses.ResponseTextConfigParam{}, nil, false, err
	}
	return responses.ResponseTextConfigParam{
		Format: responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
				Name:   name,
				Schema: parameters,
				Strict: param.NewOpt(true),
			},
		},
	}, projection, true, nil
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

// canonicalPayload preserves Bedrock arguments and removes only transport-only
// nulls introduced by the direct OpenAI strict projection.
func (c *toolCodec) canonicalPayload(providerName string, payload []byte) (rawjson.Message, error) {
	if c == nil {
		return nil, errors.New("openai: tool codec is required")
	}
	if c.exact {
		return bytes.Clone(payload), nil
	}
	projection := c.projections[providerName]
	if projection == nil {
		return nil, fmt.Errorf("openai: tool %q has no strict schema projection", providerName)
	}
	return projection.canonicalize(payload)
}

// streamsCanonicalDeltas reports whether provider argument fragments already
// concatenate to the canonical payload accepted by the generated tool codec.
func (c *toolCodec) streamsCanonicalDeltas(providerName string) bool {
	if c == nil {
		return false
	}
	if c.exact {
		return true
	}
	projection := c.projections[providerName]
	return projection != nil && !projection.canonicalizes
}

// providerNames returns the canonical-to-provider name mapping used when
// encoding request messages and tool choices. Nil when no tools are declared.
func (c *toolCodec) providerNames() map[string]string {
	if c == nil {
		return nil
	}
	return c.canonicalToProvider
}

// sdkSchema preserves raw JSON field values at the SDK document boundary.
// The SDK encodes json.Number as a string; raw fields retain exact integer
// constraints, examples and nested schema objects without float conversion.
func sdkSchema(data []byte) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("schema must be a JSON object")
	}
	result := make(map[string]any, len(fields))
	for name, value := range fields {
		result[name] = value
	}
	return result, nil
}
