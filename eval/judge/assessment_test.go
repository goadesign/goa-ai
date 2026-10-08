package judge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestReasoningRecordsUsageFromFailedCorrectionsAndExplicitAbstention(t *testing.T) {
	rejected := toolResponse(`{"wrong":{"label":"entailed","rationale":"Invalid identity."}}`)
	rejected.Usage = model.TokenUsage{Model: "fixture-model", InputTokens: 11, OutputTokens: 3, TotalTokens: 14}
	accepted := toolResponse(`{"claim_1":{"label":"unresolved","rationale":"The supplied evidence is insufficient."}}`)
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
		toolResponse(`{"claim_1":{"label":"not_addressed","rationale":"The answer describes a plan, not completed work."}}`),
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

func TestAssessmentKeepsCallerIDsSeparateFromProviderNames(t *testing.T) {
	claims := []eval.Claim{
		{ID: "suite/scenario/component/requirement", Text: "The answer gives the temperature."},
		{ID: "温度", Text: "The answer preserves the unit."},
		{ID: strings.Repeat("long", 30), Text: "The answer includes the date."},
	}
	provider := &recordingClient{responses: []*model.Response{toolResponse(`{
		"claim_3":{"label":"unresolved","rationale":"The date is unknown."},
		"claim_1":{"label":"entailed","rationale":"The temperature is present."},
		"claim_2":{"label":"not_addressed","rationale":"The unit is missing."}
	}`)}}
	result, err := newTestJudge(t, provider).Reason(t.Context(), "It was 12.", claims, "The reading is 12 °C.")
	require.NoError(t, err)
	assert.Equal(t, []eval.Judgment{
		{ClaimID: claims[0].ID, Label: eval.Entailed, Rationale: "The temperature is present."},
		{ClaimID: claims[1].ID, Label: eval.NotAddressed, Rationale: "The unit is missing."},
	}, result.Judgments)
	assert.Equal(t, []eval.Abstention{{ClaimID: claims[2].ID, Reason: "The date is unknown."}}, result.Abstentions)
	require.Len(t, provider.requests, 1)
	var schema judgmentSchema
	require.NoError(t, json.Unmarshal(provider.requests[0].Tools[0].Input.Contract().Schema, &schema))
	assert.Equal(t, []string{"claim_1", "claim_2", "claim_3"}, schema.Required)
	require.Len(t, schema.Properties, 3)
	for index, claim := range claims {
		var property judgmentSchema
		require.NoError(t, json.Unmarshal(schema.Properties[assessmentClaimKey(index)], &property))
		assert.Equal(t, claim.Text, property.Description)
		assert.NotContains(t, schema.Properties, claim.ID)
	}
}

func TestAdjudicationCorrelatesReorderedConflictsWithoutChangingInputs(t *testing.T) {
	claims := []eval.Claim{
		{ID: "suite/first", Text: "The first reading is present."},
		{ID: "suite/second", Text: "The second reading is present."},
	}
	conflicts := []eval.Disagreement{
		{Prediction: eval.Prediction{ClaimID: claims[1].ID, Probability: .98},
			Judgment:        eval.Judgment{ClaimID: claims[1].ID, Label: eval.NotAddressed, Rationale: "Second is absent."},
			QualificationID: "second-proof"},
		{Prediction: eval.Prediction{ClaimID: claims[0].ID, Probability: .97},
			Judgment:        eval.Judgment{ClaimID: claims[0].ID, Label: eval.NotAddressed, Rationale: "First is absent."},
			QualificationID: "first-proof"},
	}
	original, err := json.Marshal(conflicts)
	require.NoError(t, err)
	provider := &recordingClient{responses: []*model.Response{toolResponse(`{
		"claim_2":{"label":"not_addressed","rationale":"Second remains absent."},
		"claim_1":{"label":"entailed","rationale":"First is supplied."}
	}`)}}
	result, err := newTestJudge(t, provider).Adjudicate(t.Context(), "First: 12.", claims, "Two readings.", conflicts)
	require.NoError(t, err)
	require.Len(t, result.Judgments, 2)
	assert.Equal(t, claims[0].ID, result.Judgments[0].ClaimID)
	assert.Equal(t, claims[1].ID, result.Judgments[1].ClaimID)
	var payload struct {
		Disagreements map[string]eval.Disagreement `json:"disagreements"`
	}
	require.NoError(t, json.Unmarshal([]byte(provider.requests[0].Messages[1].Parts[0].(model.TextPart).Text), &payload))
	require.Len(t, payload.Disagreements, 2)
	for index, claim := range claims {
		key := assessmentClaimKey(index)
		conflict := payload.Disagreements[key]
		assert.Equal(t, key, conflict.Judgment.ClaimID)
		assert.Equal(t, key, conflict.Prediction.ClaimID)
		assert.NotContains(t, payload.Disagreements, claim.ID)
	}
	assert.Equal(t, "first-proof", payload.Disagreements["claim_1"].QualificationID)
	assert.Equal(t, "second-proof", payload.Disagreements["claim_2"].QualificationID)
	unchanged, err := json.Marshal(conflicts)
	require.NoError(t, err)
	assert.JSONEq(t, string(original), string(unchanged))
}
