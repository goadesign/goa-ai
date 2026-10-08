package judge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestReasoningRecordsUsageFromFailedCorrectionsAndExplicitAbstention(t *testing.T) {
	rejected := toolResponse(`{"wrong":{"label":"entailed","rationale":"Invalid identity."}}`)
	rejected.Usage = model.TokenUsage{Model: "fixture-model", InputTokens: 11, OutputTokens: 3, TotalTokens: 14}
	accepted := toolResponse(`{"complete":{"label":"unresolved","rationale":"The supplied evidence is insufficient."}}`)
	accepted.Usage = model.TokenUsage{Model: "fixture-model", InputTokens: 17, OutputTokens: 4, TotalTokens: 21}
	provider := &recordingClient{responses: []*model.Response{rejected, accepted}}
	judge := newTestJudge(t, provider)
	result, err := judge.Reason(t.Context(), "Captured answer.", []eval.Claim{{ID: "complete", Text: "The task was completed."}}, "Captured facts.")
	require.NoError(t, err)
	assert.Empty(t, result.Judgments)
	require.Len(t, result.Abstentions, 1)
	assert.Equal(t, "complete", result.Abstentions[0].ClaimID)
	require.Len(t, result.Calls, 2)
	assert.Equal(t, "reasoning", result.Calls[0].Stage)
	assert.NotEmpty(t, result.Calls[0].Error)
	assert.Empty(t, result.Calls[1].Error)
	require.NotNil(t, result.Calls[0].Usage)
	assert.Equal(t, 14, result.Calls[0].Usage.TotalTokens)
	assert.Equal(t, 21, result.Calls[1].Usage.TotalTokens)
	assert.Len(t, provider.requests, 2)

	var request requestBody
	content := provider.requests[0].Messages[1].Parts[0].(model.TextPart).Text
	require.NoError(t, json.Unmarshal([]byte(content), &request))
	assert.Equal(t, "Captured answer.", request.Output)
	assert.Equal(t, "Captured facts.", request.Reference)
	assert.NotContains(t, content, "probabilities")
	assert.Contains(t, judge.Config().Instructions, "unresolved")
}

func TestAdjudicationReceivesOriginalEvidenceAndBothDecisions(t *testing.T) {
	provider := &recordingClient{responses: []*model.Response{
		toolResponse(`{"complete":{"label":"not_addressed","rationale":"The answer describes a plan, not completed work."}}`),
	}}
	claims := []eval.Claim{{ID: "complete", Text: "The task was completed."}}
	disagreements := []eval.Disagreement{{
		Prediction:      eval.Prediction{ClaimID: "complete", Probability: .97},
		Judgment:        eval.Judgment{ClaimID: "complete", Label: eval.NotAddressed, Rationale: "Only planned."},
		QualificationID: "reviewed-fixture",
	}}
	result, err := newTestJudge(t, provider).Adjudicate(t.Context(), "I will do it tomorrow.", claims, "The task was requested.", disagreements)
	require.NoError(t, err)
	assert.Equal(t, eval.NotAddressed, result.Judgments[0].Label)
	assert.Equal(t, "adjudication", result.Calls[0].Stage)
	assert.Len(t, provider.requests, 1)
	body := provider.requests[0].Messages[1].Parts[0].(model.TextPart).Text
	assert.Contains(t, body, "I will do it tomorrow.")
	assert.Contains(t, body, "The task was requested.")
	assert.Contains(t, body, `"probability":0.97`)
	assert.Contains(t, body, "Only planned.")
	assert.Contains(t, body, "reviewed-fixture")
}
