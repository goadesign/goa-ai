package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestTranslateResponseTextAndToolCall(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{
				{Text: "found two"},
				{FunctionCall: &genai.FunctionCall{Name: "feed_find_duplicates", Args: map[string]any{"title": "picnic"}}},
			}},
		}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     100,
			CandidatesTokenCount: 20,
			ThoughtsTokenCount:   5,
			TotalTokenCount:      125,
		},
	}
	provToCanon := map[string]string{"feed_find_duplicates": "feed/find_duplicates"}
	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, provToCanon)
	require.NoError(t, err)
	require.Len(t, out.Content, 1)
	assert.Equal(t, model.ConversationRoleAssistant, out.Content[0].Role)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, "feed/find_duplicates", string(out.ToolCalls[0].Name))
	assert.JSONEq(t, `{"title":"picnic"}`, string(out.ToolCalls[0].Payload))
	// No provider-issued ID on the FunctionCall, so the adapter synthesizes one.
	assert.Equal(t, "call-1-feed_find_duplicates", out.ToolCalls[0].ID)
	assert.Equal(t, string(genai.FinishReasonStop), out.StopReason)
	assert.Equal(t, 100, out.Usage.InputTokens)
	assert.Equal(t, 25, out.Usage.OutputTokens)
	assert.Equal(t, 125, out.Usage.TotalTokens)
	assert.Equal(t, "gemini-2.5-pro", out.Usage.Model)
	assert.Equal(t, model.ModelClassDefault, out.Usage.ModelClass)
}

func TestTranslateResponseUnknownToolPassesThrough(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "never_advertised", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassDefault, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls, 1)
	// Unadvertised names surface as-is so the runtime produces an
	// unknown-tool result instead of the adapter erroring.
	assert.Equal(t, "never_advertised", string(out.ToolCalls[0].Name))
}

func TestTranslateResponseProviderToolCallIDWins(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-abc", Name: "feed_find_duplicates", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassSmall, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls, 1)
	// A provider-issued FunctionCall.ID is preferred over the synthesized
	// call-N-name fallback.
	assert.Equal(t, "call-abc", out.ToolCalls[0].ID)
	// Model attribution is stamped even without usage metadata.
	assert.Equal(t, model.ModelClassSmall, out.Usage.ModelClass)
}

func TestTranslateResponseNoCandidates(t *testing.T) {
	_, err := translateResponse(&genai.GenerateContentResponse{}, "m", model.ModelClassDefault, nil)
	assert.Error(t, err)
}
