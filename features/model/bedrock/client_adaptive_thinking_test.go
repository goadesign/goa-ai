package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// When the configured high-reasoning model is Opus 4.7 or later, the streaming input
// must carry thinking: {type: "adaptive", display: "summarized"} — never the
// legacy type:"enabled" + budget_tokens payload that returns a 400 on 4.7+ and
// never the implicit display default that omits visible reasoning text.
func TestBuildConverseStreamInputOpus47AndLaterUsesAdaptiveThinking(t *testing.T) {
	for _, highModel := range []string{
		"us.anthropic.claude-opus-4-7",
		"us.anthropic.claude-opus-4-8",
		"us.anthropic.claude-opus-4-9",
		"us.anthropic.claude-fable-5",
	} {
		t.Run(highModel, func(t *testing.T) {
			client := &provider{
				defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				highModel:    highModel,
				maxTok:       32,
			}

			req := &model.Request{
				ModelClass: model.ModelClassHighReasoning,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "plan the refactor"}},
				}},
				Tools: []*model.ToolDefinition{{
					Name:        "search",
					Description: "search the workspace",
					Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
				}},
				Thinking: &model.ThinkingOptions{
					Enable:       true,
					Interleaved:  true,
					BudgetTokens: 8192,
				},
			}

			parts, err := client.prepareRequest(req)
			require.NoError(t, err)

			thinking := parts.thinking
			require.True(t, thinking.enable, "thinking must be enabled")
			require.True(t, thinking.adaptive, "Opus 4.7+ must use adaptive thinking")
			require.Zero(t, thinking.budget, "adaptive mode must not carry a token budget")
			require.False(t, thinking.interleaved, "adaptive mode must not set the interleaved beta header")

			input, err := client.buildConverseStreamInput(parts, req)
			require.NoError(t, err)
			require.NotNil(t, input.AdditionalModelRequestFields)

			raw, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))

			thinkingField, ok := fields["thinking"].(map[string]any)
			require.True(t, ok, "expected thinking field to be a map, got %T", fields["thinking"])
			assert.Equal(t, "adaptive", thinkingField["type"])
			assert.Equal(t, "summarized", thinkingField["display"])
			_, hasBudget := thinkingField["budget_tokens"]
			assert.False(t, hasBudget, "adaptive thinking must not include budget_tokens")
			_, hasBeta := fields["anthropic_beta"]
			assert.False(t, hasBeta, "adaptive thinking must not include the interleaved beta header")
		})
	}
}

// Claude Sonnet 5 thinks adaptively on every Bedrock request and rejects the
// legacy budgeted thinking shape. When a caller asks to receive thinking, the
// adapter must request summarized display explicitly because Bedrock otherwise
// returns an empty signed thinking block.
func TestBuildConverseStreamInputSonnet5UsesVisibleAdaptiveThinking(t *testing.T) {
	client := &provider{
		defaultModel: "global.anthropic.claude-sonnet-5",
		maxTok:       32,
	}
	req := &model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "inspect the facility"}},
		}},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 8192,
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)

	thinking := parts.thinking
	require.True(t, thinking.enable)
	require.True(t, thinking.adaptive)
	require.Zero(t, thinking.budget)
	require.False(t, thinking.interleaved)

	input, err := client.buildConverseStreamInput(parts, req)
	require.NoError(t, err)
	require.NotNil(t, input.AdditionalModelRequestFields)
	raw, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))

	assert.Equal(t, map[string]any{
		"type":    "adaptive",
		"display": "summarized",
	}, fields["thinking"])
}

func TestPrepareRequestLegacyThinkingRequiresPositiveBudget(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0"}
	for _, budget := range []int{0, -1} {
		req := &model.Request{
			Messages: []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "reason"}},
			}},
			Thinking: &model.ThinkingOptions{Enable: true, BudgetTokens: budget},
		}
		_, err := client.prepareRequest(req)
		require.ErrorContains(t, err, "budget_tokens must be positive")
	}
}

func TestBuildConverseStreamInputThinkingBudget(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		maxTok:       32,
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "reason"}},
		}},
		Thinking: &model.ThinkingOptions{Enable: true, BudgetTokens: 4096},
	}
	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	input, err := client.buildConverseStreamInput(parts, req)
	require.NoError(t, err)
	require.NotNil(t, input.AdditionalModelRequestFields)
	raw, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.Equal(t, map[string]any{
		"type":          "enabled",
		"budget_tokens": float64(4096),
	}, fields["thinking"])
}

func TestConverseAndConverseStreamShareThinkingContract(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0"}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "reason"}},
		}},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 4096,
		},
	}
	parts, err := client.prepareRequest(req)
	require.NoError(t, err)

	unary, err := client.buildConverseInput(parts, req)
	require.NoError(t, err)
	streaming, err := client.buildConverseStreamInput(parts, req)
	require.NoError(t, err)
	require.NotNil(t, unary.AdditionalModelRequestFields)
	require.NotNil(t, streaming.AdditionalModelRequestFields)
	unaryFields, err := unary.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	streamFields, err := streaming.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)

	assert.JSONEq(t, string(unaryFields), string(streamFields))
	assert.Len(t, client.requestOptions(parts.thinking), 1)
}

// Bedrock adaptive thinking is valid even without tools. The adapter must not
// silently drop thinking config just because the request is a plain message
// turn, otherwise Opus 4.7 falls back to omitted reasoning text again.
func TestResolveThinkingOpus47WithoutTools(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		maxTok:       32,
	}

	req := &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "explain the trade-offs"}},
		}},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 8192,
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	require.Nil(t, parts.toolConfig, "test requires a no-tools request")

	thinking := parts.thinking
	require.True(t, thinking.enable, "explicit adaptive thinking must survive no-tools requests")
	require.True(t, thinking.adaptive, "Opus 4.7 must stay on adaptive thinking without tools")
	require.Zero(t, thinking.budget, "adaptive mode must not carry a token budget")
	require.False(t, thinking.interleaved, "adaptive mode must not request the legacy interleaved beta header")
}

func TestResolveThinkingOpus47KeepsAdaptiveThinkingWithForcedTool(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		maxTok:       32,
	}

	req := &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "finish the task"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.complete",
			Description: "complete the task",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "tasks.progress.complete",
		},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 8192,
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	assert.True(t, parts.thinking.enable)
	assert.True(t, parts.thinking.adaptive)
}

func TestResolveThinkingOpus47KeepsAdaptiveThinkingWithAnyTool(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		maxTok:       32,
	}

	req := &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "continue through tools"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.update",
			Description: "update task progress",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeAny,
		},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 8192,
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	assert.True(t, parts.thinking.enable)
	assert.True(t, parts.thinking.adaptive)
}

func TestFableAcceptsForcedToolChoice(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice *model.ToolChoice
	}{
		{
			name:       "any",
			toolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAny},
		},
		{
			name: "specific tool",
			toolChoice: &model.ToolChoice{
				Mode: model.ToolChoiceModeTool,
				Name: "tasks.progress.complete",
			},
		},
		{
			name:       "auto",
			toolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAuto},
		},
		{
			name: "nil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &provider{
				defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				highModel:    "us.anthropic.claude-fable-5",
				maxTok:       32,
			}

			req := &model.Request{
				ModelClass: model.ModelClassHighReasoning,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "finish the task"}},
				}},
				Tools: []*model.ToolDefinition{{
					Name:        "tasks.progress.complete",
					Description: "complete the task",
					Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
				}},
				ToolChoice: tc.toolChoice,
			}

			_, err := client.prepareRequest(req)
			require.NoError(t, err)
		})
	}
}

func TestResolveThinkingLegacyModelRejectsForcedToolChoice(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		maxTok:       4096,
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "finish the task"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.complete",
			Description: "complete the task",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "tasks.progress.complete",
		},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			BudgetTokens: 2048,
		},
	}

	_, err := client.prepareRequest(req)
	require.EqualError(t, err, "bedrock: manual thinking cannot be combined with forced tool choice")
}

// Opus models allow forced tool choice when the caller did not request
// thinking.
func TestOpusAllowsForcedToolChoiceWithoutThinking(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-8",
		maxTok:       32,
	}

	req := &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "finish the task"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.complete",
			Description: "complete the task",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "tasks.progress.complete",
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	thinking := parts.thinking
	require.False(t, thinking.enable)
}

// Claude Opus 4.7 and later, as well as the Claude 5 generation (Fable), reject
// sampling parameters like temperature. The Bedrock adapter must omit
// temperature for those requests while preserving it for models that still
// support sampling controls.
func TestOpus47AndLaterOmitsTemperatureFromInferenceConfig(t *testing.T) {
	cases := []struct {
		name       string
		highModel  string
		modelClass model.ModelClass
		wantTemp   bool
	}{
		{
			name:       "default keeps temperature",
			highModel:  "us.anthropic.claude-opus-4-8",
			modelClass: model.ModelClassDefault,
			wantTemp:   true,
		},
		{
			name:       "high reasoning opus-4-8 omits temperature",
			highModel:  "us.anthropic.claude-opus-4-8",
			modelClass: model.ModelClassHighReasoning,
			wantTemp:   false,
		},
		{
			name:       "high reasoning fable-5 omits temperature",
			highModel:  "us.anthropic.claude-fable-5",
			modelClass: model.ModelClassHighReasoning,
			wantTemp:   false,
		},
		{
			name:       "small keeps temperature",
			highModel:  "us.anthropic.claude-opus-4-8",
			modelClass: model.ModelClassSmall,
			wantTemp:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &provider{
				defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				highModel:    tc.highModel,
				smallModel:   "global.anthropic.claude-haiku-4-5-20251001-v1:0",
			}

			req := &model.Request{
				ModelClass:  tc.modelClass,
				Temperature: 0.2,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			}

			parts, err := client.prepareRequest(req)
			require.NoError(t, err)

			input, err := client.buildConverseInput(parts, req)
			require.NoError(t, err)
			if tc.wantTemp {
				require.NotNil(t, input.InferenceConfig)
				require.NotNil(t, input.InferenceConfig.Temperature)
				assert.InDelta(t, 0.2, *input.InferenceConfig.Temperature, 0.001)
				return
			}

			if input.InferenceConfig != nil {
				assert.Nil(t, input.InferenceConfig.Temperature)
			}
		})
	}
}

// TestSonnet5OmitsTemperatureOnBedrock is the regression test for the
// live-verified 400 that motivated the shared capability rule: Claude
// Sonnet 5 rejects any request carrying a non-default temperature
// ("temperature is deprecated for this model"), so the Bedrock adapter must
// never emit InferenceConfig.Temperature for it — while Sonnet 4.x keeps
// receiving the caller's value.
func TestSonnet5OmitsTemperatureOnBedrock(t *testing.T) {
	cases := []struct {
		name         string
		defaultModel string
		wantTemp     bool
	}{
		{"sonnet-5 in-region omits temperature", "anthropic.claude-sonnet-5", false},
		{"sonnet-5 us geo omits temperature", "us.anthropic.claude-sonnet-5", false},
		{"sonnet-5 global suffixed omits temperature", "global.anthropic.claude-sonnet-5-v1:0", false},
		{"sonnet-4-5 keeps temperature", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &provider{defaultModel: tc.defaultModel}

			req := &model.Request{
				Temperature: 0.2,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			}

			parts, err := client.prepareRequest(req)
			require.NoError(t, err)

			input, err := client.buildConverseInput(parts, req)
			require.NoError(t, err)
			if tc.wantTemp {
				require.NotNil(t, input.InferenceConfig)
				require.NotNil(t, input.InferenceConfig.Temperature)
				assert.InDelta(t, 0.2, *input.InferenceConfig.Temperature, 0.001)
				return
			}
			if input.InferenceConfig != nil {
				assert.Nil(t, input.InferenceConfig.Temperature)
			}
		})
	}
}
