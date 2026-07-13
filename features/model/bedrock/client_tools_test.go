package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestEncodeTools_NoChoice(t *testing.T) {
	cfg, fields, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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

func TestEncodeTools_ModeAny(t *testing.T) {
	cfg, _, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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

func TestEncodeTools_ModeNonePreservesConfig(t *testing.T) {
	cfg, _, canonToSan, sanToCanon, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeNone}, false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Tools, 1)
	require.Nil(t, cfg.ToolChoice)
	require.Len(t, canonToSan, 1)
	require.Len(t, sanToCanon, 1)
}

func TestEncodeTools_ChoiceWithoutToolsErrors(t *testing.T) {
	_, _, _, _, err := encodeTools("amazon.nova-pro-v1:0", nil, &model.ToolChoice{Mode: model.ToolChoiceModeAny}, false)
	require.Error(t, err)
}

func TestEncodeTools_AppendsCacheCheckpoint(t *testing.T) {
	cfg, _, _, _, err := encodeTools("amazon.nova-pro-v1:0", []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Search",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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
			Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
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
			Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
		},
		{
			Name:        "reports.lookup",
			Description: "Look up a report",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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
	client := &Client{
		defaultModel: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		maxTok:       32,
		think:        defaultThinkingBudget,
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "finish the report"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
		}},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	require.NotNil(t, parts.toolConfig)

	input := client.buildConverseStreamInput(parts, req, thinkingConfig{})
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
	client := &Client{
		defaultModel: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		maxTok:       32,
		think:        defaultThinkingBudget,
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
				Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
			},
			{
				Name:        "reports.lookup",
				Description: "Look up a report",
				Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
			},
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)

	input := client.buildConverseStreamInput(parts, req, thinkingConfig{})
	require.NotNil(t, input.ToolConfig)
	require.Nil(t, input.AdditionalModelRequestFields)
}

func TestEncodeTools_AnthropicToolChoiceUsesNativeFieldWithExamples(t *testing.T) {
	_, fields, _, _, err := encodeTools("us.anthropic.claude-opus-4-7", []*model.ToolDefinition{
		{
			Name:        "reports.complete",
			Description: "Complete a report",
			Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
		},
	}, &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "reports.complete"}, false)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"type": "tool", "name": "reports_complete"}, fields["tool_choice"])
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

func TestSanitizeToolName_StripsNamespaces(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ada toolset preserves namespace",
			in:   "ada.get_application_status",
			want: "ada_get_application_status",
		},
		{
			name: "ada time series preserves namespace",
			in:   "ada.get_time_series",
			want: "ada_get_time_series",
		},
		{
			name: "chat atlas read subset preserves full canonical id",
			in:   "atlas.read.chat.chat_get_user_details",
			want: "atlas_read_chat_chat_get_user_details",
		},
		{
			name: "chat emit toolset preserves namespace",
			in:   "chat.emit.ask_clarifying_question",
			want: "chat_emit_ask_clarifying_question",
		},
		{
			name: "todos toolset preserves namespace",
			in:   "todos.todos.update_todos",
			want: "todos_todos_update_todos",
		},
		{
			name: "plain name passthrough",
			in:   "lookup",
			want: "lookup",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolName(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeToolName_NoCollisionsAcrossToolsets(t *testing.T) {
	a := SanitizeToolName("atlas.read.explain_control_logic")
	b := SanitizeToolName("ada.explain_control_logic")

	require.NotEmpty(t, a)
	require.NotEmpty(t, b)
	require.NotEqual(t, a, b)
}

func TestSanitizeToolName_TruncatesWithStableHashSuffix(t *testing.T) {
	in := "atlas.read.chat." + strings.Repeat("very_long_segment_", 10) + "tool"
	got := SanitizeToolName(in)

	require.NotEmpty(t, got)
	require.LessOrEqual(t, len(got), 64)
	require.Regexp(t, `_[0-9a-f]{8}$`, got)
	require.Equal(t, got, SanitizeToolName(in))
}
