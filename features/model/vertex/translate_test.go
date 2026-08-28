package vertex

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestVertexOutputLimited(t *testing.T) {
	tests := []struct {
		name   string
		reason genai.FinishReason
		want   bool
	}{
		{name: "maximum output tokens", reason: genai.FinishReasonMaxTokens, want: true},
		{name: "natural end", reason: genai.FinishReasonStop},
		{name: "safety", reason: genai.FinishReasonSafety},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := translateResponse(
				&genai.GenerateContentResponse{
					Candidates: []*genai.Candidate{{
						FinishReason: test.reason,
						Content: &genai.Content{
							Role:  "model",
							Parts: []*genai.Part{{Text: "answer"}},
						},
					}},
				},
				"gemini-test",
				model.ModelClassDefault,
				nil,
				nil,
			)

			require.NoError(t, err)
			require.Equal(t, test.want, response.OutputLimited)
		})
	}
}

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
	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, provToCanon, nil)
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
	out, err := translateResponse(resp, "gemini-3-pro", model.ModelClassDefault, provToCanon, nil)
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
	out, err := translateResponse(
		resp,
		"gemini-2.5-pro",
		model.ModelClassDefault,
		map[string]string{"feed_find_duplicates": "feed/find_duplicates"},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, out.ToolCalls(), 1)
	assert.Empty(t, out.ToolCalls()[0].ThoughtSignature)
}

func TestTranslateResponseAssignsMissingToolCallID(t *testing.T) {
	response := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{Name: "lookup", Args: map[string]any{}},
			}}},
		}},
	}

	translated, err := translateResponse(
		response,
		"gemini-2.5-pro",
		model.ModelClassDefault,
		map[string]string{"lookup": "lookup"},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, translated.ToolCalls(), 1)
	assert.Equal(t, "vertex-call-0", translated.ToolCalls()[0].ID)

	history := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.ToolUsePart{
			Name: "lookup",
			ID:   "vertex-call-0",
		}},
	}}
	translated, err = translateResponse(
		response,
		"gemini-2.5-pro",
		model.ModelClassDefault,
		map[string]string{"lookup": "lookup"},
		newVertexToolCallIDAllocator(history),
	)
	require.NoError(t, err)
	assert.Equal(t, "vertex-call-1", translated.ToolCalls()[0].ID)
}

func TestTranslateResponseAssignsDenseThinkingIndexes(t *testing.T) {
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		FinishReason: genai.FinishReasonStop,
		Content: &genai.Content{Parts: []*genai.Part{
			{Text: "before"},
			{Thought: true, Text: "first", ThoughtSignature: []byte("sig-1")},
			{Text: "between"},
			{Thought: true, Text: "second", ThoughtSignature: []byte("sig-2")},
		}},
	}}}

	out, err := translateResponse(resp, "gemini-test", model.ModelClassDefault, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Content[0].Parts[1].(model.ThinkingPart).Index)
	assert.Equal(t, 1, out.Content[0].Parts[3].(model.ThinkingPart).Index)
}

func TestTranslateResponseRejectsUnknownTool(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "never_advertised", Args: map[string]any{}}},
			}},
		}},
	}
	_, err := translateResponse(resp, "m", model.ModelClassDefault, map[string]string{}, nil)
	name, ok := model.UnadvertisedToolName(err)
	require.True(t, ok)
	require.Equal(t, "never_advertised", name)
	require.NotContains(t, err.Error(), name)
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
	out, err := translateResponse(
		resp,
		"m",
		model.ModelClassSmall,
		map[string]string{"feed_find_duplicates": "feed/find_duplicates"},
		nil,
	)
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
	out, err := translateResponse(
		resp,
		"m",
		model.ModelClassDefault,
		map[string]string{"feed_find_duplicates": "feed/find_duplicates"},
		nil,
	)
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

func TestMarshalArgsMeasuresEscapedJSONBytes(t *testing.T) {
	payload, err := marshalArgs(map[string]any{"value": "\x00"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"\u0000"}`, string(payload))

	rawUnderLimit := strings.Repeat("\x00", maxVertexToolArgsBytes/6+1)
	require.Less(t, len(rawUnderLimit), maxVertexToolArgsBytes)
	_, err = marshalArgs(map[string]any{"value": rawUnderLimit})
	require.ErrorIs(t, err, errVertexToolArgsTooLarge)
}

func TestMarshalArgsEnforcesExactEncodedSizeLimit(t *testing.T) {
	const envelopeBytes = len(`{"value":""}`)
	exactValue := strings.Repeat("a", maxVertexToolArgsBytes-envelopeBytes)

	payload, err := marshalArgs(map[string]any{"value": exactValue})
	require.NoError(t, err)
	assert.Len(t, payload, maxVertexToolArgsBytes)

	_, err = marshalArgs(map[string]any{"value": exactValue + "a"})
	require.ErrorIs(t, err, errVertexToolArgsTooLarge)
}

func TestMarshalArgsMatchesEncodingJSON(t *testing.T) {
	tests := []map[string]any{
		{"text": "<>&\u2028\u2029"},
		{"text": string([]byte{0xff})},
		{"numbers": []any{1e-7, -1e-7, math.Copysign(0, -1), 123.5}},
		{"nested": map[string]any{"enabled": true, "missing": nil}},
	}
	for _, args := range tests {
		expected, err := json.Marshal(args)
		require.NoError(t, err)

		actual, err := marshalArgs(args)
		require.NoError(t, err)
		assert.Equal(t, string(expected), string(actual))
	}
}

func TestMeasureSDKJSONValueRejectsContainerBeforeTraversal(t *testing.T) {
	tests := []any{
		map[string]any{"first": nil, "second": nil},
		[]any{nil, nil},
	}
	for _, value := range tests {
		values := 99_998
		encodedSize := 0
		err := measureSDKJSONValue(value, 0, &values, &encodedSize)
		require.EqualError(t, err, "vertex: tool args exceed 100000 values")
		assert.Equal(t, 99_999, values)
		assert.Zero(t, encodedSize)
	}
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

	out, err := translateResponse(resp, "gemini-2.5-pro", model.ModelClassDefault, nil, nil)
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
	_, err := translateResponse(&genai.GenerateContentResponse{}, "m", model.ModelClassDefault, nil, nil)
	assert.Error(t, err)
}
