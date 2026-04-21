package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
)

// isAdaptiveThinkingModel must match every Bedrock inference profile scope
// (in-region, geo cross-region, global cross-region) for the Opus versions
// that require adaptive thinking. Misclassifying these models causes the
// adapter to send the legacy type:"enabled" + budget_tokens config, which
// produces unreliable signatures on Opus 4.6 and a 400 error on Opus 4.7+.
func TestIsAdaptiveThinkingModel(t *testing.T) {
	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"opus-4-6 in-region", "anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 us geo", "us.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 eu geo", "eu.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-6 global", "global.anthropic.claude-opus-4-6-v1", true},
		{"opus-4-7 in-region", "anthropic.claude-opus-4-7", true},
		{"opus-4-7 us geo", "us.anthropic.claude-opus-4-7", true},
		{"opus-4-7 eu geo", "eu.anthropic.claude-opus-4-7", true},
		{"opus-4-7 jp geo", "jp.anthropic.claude-opus-4-7", true},
		{"opus-4-7 global", "global.anthropic.claude-opus-4-7", true},
		{"opus-4-1", "anthropic.claude-opus-4-1", false},
		{"opus-4-5", "anthropic.claude-opus-4-5", false},
		{"sonnet-4-5", "global.anthropic.claude-sonnet-4-5-20250929-v1:0", false},
		{"haiku-4-5", "global.anthropic.claude-haiku-4-5-20251001-v1:0", false},
		{"sonnet-3-7", "anthropic.claude-3-7-sonnet", false},
		{"nova", "amazon.nova-pro-v1:0", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAdaptiveThinkingModel(tc.modelID), "isAdaptiveThinkingModel(%q)", tc.modelID)
		})
	}
}

// When the configured high-reasoning model is Opus 4.7, the streaming input
// must carry thinking: {type: "adaptive", display: "summarized"} — never the
// legacy type:"enabled" + budget_tokens payload that returns a 400 on 4.7 and
// never the implicit display default that omits visible reasoning text.
func TestBuildConverseStreamInputOpus47UsesAdaptiveThinking(t *testing.T) {
	client := &Client{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		maxTok:       32,
		think:        defaultThinkingBudget,
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
			InputSchema: map[string]any{"type": "object"},
		}},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 8192,
		},
	}

	parts, err := client.prepareRequest(context.Background(), req)
	require.NoError(t, err)

	thinking := client.resolveThinking(req, parts)
	require.True(t, thinking.enable, "thinking must be enabled")
	require.True(t, thinking.adaptive, "Opus 4.7 must use adaptive thinking")
	require.Zero(t, thinking.budget, "adaptive mode must not carry a token budget")
	require.False(t, thinking.interleaved, "adaptive mode must not set the interleaved beta header")

	input := client.buildConverseStreamInput(parts, req, thinking)
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
}

// Bedrock adaptive thinking is valid even without tools. The adapter must not
// silently drop thinking config just because the request is a plain message
// turn, otherwise Opus 4.7 falls back to omitted reasoning text again.
func TestResolveThinkingOpus47WithoutTools(t *testing.T) {
	client := &Client{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		maxTok:       32,
		think:        defaultThinkingBudget,
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

	parts, err := client.prepareRequest(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, parts.toolConfig, "test requires a no-tools request")

	thinking := client.resolveThinking(req, parts)
	require.True(t, thinking.enable, "explicit adaptive thinking must survive no-tools requests")
	require.True(t, thinking.adaptive, "Opus 4.7 must stay on adaptive thinking without tools")
	require.Zero(t, thinking.budget, "adaptive mode must not carry a token budget")
	require.False(t, thinking.interleaved, "adaptive mode must not request the legacy interleaved beta header")
}

// Claude Opus 4.7 rejects sampling parameters like temperature. The Bedrock
// adapter must omit temperature for Opus 4.7 requests while preserving it for
// models that still support sampling controls.
func TestOpus47OmitsTemperatureFromInferenceConfig(t *testing.T) {
	client := &Client{
		defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		highModel:    "us.anthropic.claude-opus-4-7",
		smallModel:   "global.anthropic.claude-haiku-4-5-20251001-v1:0",
	}

	cases := []struct {
		name       string
		modelClass model.ModelClass
		wantTemp   bool
	}{
		{
			name:       "default keeps temperature",
			modelClass: model.ModelClassDefault,
			wantTemp:   true,
		},
		{
			name:       "high reasoning omits temperature",
			modelClass: model.ModelClassHighReasoning,
			wantTemp:   false,
		},
		{
			name:       "small keeps temperature",
			modelClass: model.ModelClassSmall,
			wantTemp:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.Request{
				ModelClass:  tc.modelClass,
				Temperature: 0.2,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			}

			parts, err := client.prepareRequest(context.Background(), req)
			require.NoError(t, err)

			input := client.buildConverseInput(parts, req)
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
