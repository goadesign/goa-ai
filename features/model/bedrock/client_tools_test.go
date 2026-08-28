package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// mustBedrockToolInput compiles a static test schema.
func mustBedrockToolInput(t *testing.T, schema rawjson.Message) model.ToolInput {
	t.Helper()
	input, err := model.AdvertisedToolInputFromSchema(schema)
	require.NoError(t, err)
	return input
}

func TestEncodeTools_NoChoice(t *testing.T) {
	cfg, fields, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Nil(t, fields)
	require.Len(t, cfg.Tools, 1)
	require.Nil(t, cfg.ToolChoice)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
}

// TestPrepareRequestClassifiesEmptyToolInputs verifies that stream decoding
// receives both the stronger code-owned argument fact and the generated
// contract's acceptance of an empty object under canonical tool names.
func TestPrepareRequestClassifiesEmptyToolInputs(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-sonnet-5"}
	parts, err := client.prepareRequest(&model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "continue"}},
		}},
		Tools: []*model.ToolDefinition{
			{
				Name:        "catalog.continue_results",
				Description: "Continue the result listing.",
				Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
				NoArguments: true,
			},
			{
				Name:        "catalog.discover.list_sources",
				Description: "List configured sources.",
				Input: mustBedrockToolInput(
					t,
					rawjson.Message(`{"type":"object","properties":{"cursor":{"type":"string"}}}`),
				),
			},
			{
				Name:        "catalog.read.get_record",
				Description: "Read one required record.",
				Input: mustBedrockToolInput(
					t,
					rawjson.Message(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`),
				),
			},
		},
	})

	require.NoError(t, err)
	require.Contains(t, parts.noArgumentTools, "catalog.continue_results")
	require.Contains(t, parts.emptyObjectTools, "catalog.continue_results")
	require.Contains(t, parts.emptyObjectTools, "catalog.discover.list_sources")
	require.NotContains(t, parts.emptyObjectTools, "catalog.read.get_record")
}

func TestEncodeTools_ModeAny(t *testing.T) {
	cfg, _, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
	choice, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberAny)
	require.True(t, ok, "expected ToolChoiceMemberAny")
	require.NotNil(t, choice)
}

func TestEncodeTools_ModeTool(t *testing.T) {
	cfg, _, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, &model.ToolChoice{
		Mode: model.ToolChoiceModeTool,
		Name: "lookup",
	}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
	member, ok := cfg.ToolChoice.(*brtypes.ToolChoiceMemberTool)
	require.True(t, ok, "expected ToolChoiceMemberTool")
	require.NotNil(t, member)
	require.NotNil(t, member.Value.Name)
	require.Equal(t, "lookup", sanToCanon[*member.Value.Name])
}

func TestEncodeTools_ModeNoneRejectsDefinedTools(t *testing.T) {
	_, _, _, _, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeNone}, false)
	require.ErrorContains(t, err, `tool choice mode "none" is unsupported when tools are defined`)
}

func TestEncodeTools_ChoiceWithoutToolsErrors(t *testing.T) {
	_, _, _, _, err := encodeTools("amazon.nova-pro-v1:0", nil, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.Error(t, err)
}

func TestEncodeTools_ModeNoneWithoutTools(t *testing.T) {
	cfg, fields, canonToSan, sanToCanon, err := encodeTools(
		"amazon.nova-pro-v1:0",
		nil,
		&model.ToolChoice{Mode: model.ToolChoiceModeNone},
		false,
	)
	require.NoError(t, err)
	require.Nil(t, cfg)
	require.Nil(t, fields)
	require.Nil(t, canonToSan)
	require.Nil(t, sanToCanon)
}

func TestEncodeTools_AppendsCacheCheckpoint(t *testing.T) {
	cfg, _, _, _, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, nil, true)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 2, "expected tool spec + cache checkpoint")
	_, ok := cfg.Tools[1].(*brtypes.ToolMemberCachePoint)
	require.True(t, ok, "expected second tool entry to be cache checkpoint")
}

func TestEncodeTools_AnthropicModelAddsToolExamples(t *testing.T) {
	cfg, fields, _, _, err := encodeTools("us.anthropic.claude-opus-4-7", []*model.ToolDefinition{
		{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       toolInputFromSpec(t, toolInputExampleSpec()),
		},
	}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, []string{"tool-examples-2025-10-29"}, fields["anthropic_beta"])
	tools, ok := fields["tools"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	require.Equal(t, "reports_complete", tools[0]["name"])
	require.Equal(t, []map[string]any{{"summary": "Done"}}, tools[0]["input_examples"])
	require.Equal(t, map[string]any{"type": "object"}, tools[0]["input_schema"])
}

func TestEncodeTools_AnthropicModelKeepsAllToolsWhenOneHasExample(t *testing.T) {
	cfg, fields, _, _, err := encodeTools("us.anthropic.claude-opus-4-7", []*model.ToolDefinition{
		{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       toolInputFromSpec(t, toolInputExampleSpec()),
		},
		{
			Name:        "reports.lookup",
			Description: "Look up a report",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	tools, ok := fields["tools"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tools, 2)
	require.Equal(t, "reports_complete", tools[0]["name"])
	require.Equal(t, []map[string]any{{"summary": "Done"}}, tools[0]["input_examples"])
	require.Equal(t, "reports_lookup", tools[1]["name"])
	require.NotContains(t, tools[1], "input_examples")
	require.Equal(t, map[string]any{"type": "object"}, tools[1]["input_schema"])
}

func TestBuildConverseStreamInputAnthropicToolExamplesUseNativeToolsOnly(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		maxTok:       32,
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "finish the report"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       toolInputFromSpec(t, toolInputExampleSpec()),
		}},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	require.NotNil(t, parts.toolConfig)

	input, err := client.buildConverseStreamInput(parts, req)
	require.NoError(t, err)
	require.Nil(t, input.ToolConfig)
	require.NotNil(t, input.AdditionalModelRequestFields)

	raw, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.Equal(t, []any{"tool-examples-2025-10-29"}, fields["anthropic_beta"])
	tools, ok := fields["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
}

func TestBuildConverseStreamInputWithToolResultsUsesBedrockToolConfig(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		maxTok:       32,
	}
	req := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "finish the report"},
				},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ToolUsePart{
						ID:    "toolu_1",
						Name:  "reports.lookup",
						Input: rawjson.Message(`{"query":"status"}`),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.ToolResultPart{
						ToolUseID: "toolu_1",
						Content:   map[string]any{"status": "ready"},
					},
				},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "reports.complete",
				Description: "Complete a report",
				Input:       toolInputFromSpec(t, toolInputExampleSpec()),
			},
			{
				Name:        "reports.lookup",
				Description: "Look up a report",
				Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
			},
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)

	input, err := client.buildConverseStreamInput(parts, req)
	require.NoError(t, err)
	require.NotNil(t, input.ToolConfig)
	require.Nil(t, input.AdditionalModelRequestFields)
}

func TestEncodeTools_AnthropicToolChoiceUsesNativeFieldWithExamples(t *testing.T) {
	_, fields, _, _, err := encodeTools("us.anthropic.claude-opus-4-7", []*model.ToolDefinition{
		{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       toolInputFromSpec(t, toolInputExampleSpec()),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "reports.complete"}, false)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"type": "tool", "name": "reports_complete"}, fields["tool_choice"])
}

func TestEncodeTools_MythosPreviewRejectsForcedToolChoice(t *testing.T) {
	_, _, _, _, err := encodeTools("us.anthropic.claude-mythos-preview-v1:0", []*model.ToolDefinition{
		{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.EqualError(
		t,
		err,
		`bedrock: model "us.anthropic.claude-mythos-preview-v1:0" does not support forced tool choice mode "any"`,
	)
}

func TestToDocumentPreservesCanonicalRawJSONNumbers(t *testing.T) {
	doc, err := toDocument(rawjson.Message(`{"large":9007199254740993}`))
	require.NoError(t, err)

	got, err := decodeDocument(doc)
	require.NoError(t, err)
	require.JSONEq(t, `{"large":9007199254740993}`, string(got))
}

func TestToDocumentPreservesDecodedJSONNumbers(t *testing.T) {
	doc, err := toDocument(map[string]any{"large": json.Number("9007199254740993")})
	require.NoError(t, err)

	got, err := decodeDocument(doc)
	require.NoError(t, err)
	require.JSONEq(t, `{"large":9007199254740993}`, string(got))
}

func TestSchemaDocumentPreservesNumericKeywords(t *testing.T) {
	doc, err := schemaDocument(rawjson.Message(
		`{"type":"integer","default":50,"minimum":1,"maximum":200}`,
	))
	require.NoError(t, err)

	got, err := decodeDocument(doc)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"integer","default":50,"minimum":1,"maximum":200}`, string(got))
}

func TestIsNovaModel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: false},
		{name: "claude", in: "anthropic.claude-3-sonnet-20241022-v1:0", want: false},
		{name: "nova", in: "amazon.nova-pro-v1:0", want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := isNovaModel(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func toolInputExampleSpec() tools.TypeSpec {
	return tools.TypeSpec{
		Name:                     "ReportsCompletePayload",
		Schema:                   tools.RawJSON(`{"type":"object","example":{"summary":"Done"}}`),
		SchemaWithoutRootExample: tools.RawJSON(`{"type":"object"}`),
		ExampleJSON:              tools.RawJSON(`{"summary":"Done"}`),
	}
}

func toolInputFromSpec(t *testing.T, spec tools.TypeSpec) model.ToolInput {
	t.Helper()
	input, err := model.ToolInputFromContract(spec.Name, model.ToolInputContract{
		Schema:                   spec.Schema,
		SchemaWithoutRootExample: spec.SchemaWithoutRootExample,
		ExampleJSON:              spec.ExampleJSON,
	})
	require.NoError(t, err)
	return input
}
