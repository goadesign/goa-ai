package vertex

import (
	"encoding/base64"
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
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{"title": "picnic"}}},
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
	require.Len(t, out.ToolCalls(), 1)
	assert.Equal(t, "feed/find_duplicates", string(out.ToolCalls()[0].Name))
	assert.JSONEq(t, `{"title":"picnic"}`, string(out.ToolCalls()[0].Payload))
	assert.Equal(t, "call-1", out.ToolCalls()[0].ID)
	assert.Equal(t, string(genai.FinishReasonStop), out.StopReason)
	assert.Equal(t, 100, out.Usage.InputTokens)
	assert.Equal(t, 25, out.Usage.OutputTokens)
	assert.Equal(t, 125, out.Usage.TotalTokens)
	assert.Equal(t, "gemini-2.5-pro", out.Usage.Model)
	assert.Equal(t, model.ModelClassDefault, out.Usage.ModelClass)
}

func TestTranslateResponseFunctionCallThoughtSignature(t *testing.T) {
	sig := []byte("gemini-3-tool-call-signature")
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{
					FunctionCall:     &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{"title": "picnic"}},
					ThoughtSignature: sig,
				},
			}},
		}},
	}
	provToCanon := map[string]string{"feed_find_duplicates": "feed/find_duplicates"}
	out, err := translateResponse(resp, "gemini-3-pro", model.ModelClassDefault, provToCanon)
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), out.ToolCalls()[0].ThoughtSignature)
}

func TestTranslateResponseFunctionCallWithoutThoughtSignature(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	assert.Empty(t, out.ToolCalls()[0].ThoughtSignature)
}

func TestTranslateResponseRejectsMissingToolCallID(t *testing.T) {
	response := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{Name: "lookup", Args: map[string]any{}},
			}}},
		}},
	}

	_, err := translateResponse(response, "gemini-2.5-pro", model.ModelClassDefault, nil)
	require.EqualError(t, err, `vertex: response function call "lookup" is missing its ID`)
}

func TestTranslateResponseUnknownToolPassesThrough(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "never_advertised", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassDefault, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	// Unadvertised names surface as-is so the runtime produces an
	// unknown-tool result instead of the adapter erroring.
	assert.Equal(t, "never_advertised", string(out.ToolCalls()[0].Name))
}

func TestTranslateResponseProviderToolCallIDWins(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-abc", Name: "feed_find_duplicates", Args: map[string]any{}}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassSmall, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	assert.Equal(t, "call-abc", out.ToolCalls()[0].ID)
	// Model attribution is stamped even without usage metadata.
	assert.Equal(t, model.ModelClassSmall, out.Usage.ModelClass)
}

func TestTranslateResponseNilArgsPayloadIsEmptyObject(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: nil}},
			}},
		}},
	}
	out, err := translateResponse(resp, "m", model.ModelClassDefault, map[string]string{})
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	// marshalArgs normalizes nil args to an empty JSON object; a plain
	// json.Marshal of a nil map would produce JSON null, which violates the
	// ToolCall.Payload contract (valid JSON object arguments).
	assert.Equal(t, `{}`, string(out.ToolCalls()[0].Payload))
}

func TestMarshalArgsRejectsUnsafeSDKInteger(t *testing.T) {
	_, err := marshalArgs(map[string]any{"reading": float64(9007199254740992)})
	require.ErrorContains(t, err, "integer outside the exact SDK range")
}

func TestTranslateResponsePreservesGroundingCitations(t *testing.T) {
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		FinishReason: genai.FinishReasonStop,
		Content: &genai.Content{Parts: []*genai.Part{
			{Text: "grounded answer"},
		}},
		GroundingMetadata: &genai.GroundingMetadata{
			GroundingChunks: []*genai.GroundingChunk{{
				Web: &genai.GroundingChunkWeb{
					Title: "Source",
					URI:   "https://example.com/source",
				},
			}},
			GroundingSupports: []*genai.GroundingSupport{{
				Segment:               &genai.Segment{PartIndex: 0},
				GroundingChunkIndices: []int32{0},
			}},
		},
	}}}

	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, nil)
	require.NoError(t, err)
	part, ok := out.Content[0].Parts[0].(model.CitationsPart)
	require.True(t, ok)
	assert.Equal(t, "grounded answer", part.Text)
	require.Equal(t, []model.Citation{{
		Title:  "Source",
		Source: "https://example.com/source",
	}}, part.Citations)
}

func TestTranslateResponseNoCandidates(t *testing.T) {
	_, err := translateResponse(&genai.GenerateContentResponse{}, "m", model.ModelClassDefault, nil)
	assert.Error(t, err)
}
