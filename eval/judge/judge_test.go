// These tests run the judge through model validation and the runtime's existing
// corrections, preserving claim order, forced-tool requests, and failure causes.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type recordingClient struct {
	mu        sync.Mutex
	requests  []*model.Request
	responses []*model.Response
	errors    []error
}

func (c *recordingClient) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := len(c.requests)
	c.requests = append(c.requests, request)
	var response *model.Response
	if index < len(c.responses) {
		response = c.responses[index]
	}
	var err error
	if index < len(c.errors) {
		err = c.errors[index]
	}
	return response, err
}

func (c *recordingClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}

func newTestJudge(t *testing.T, provider model.Provider, opts ...Option) *Judge {
	t.Helper()
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	judge, err := New(client, 1024, opts...)
	require.NoError(t, err)
	return judge
}

func TestJudgeUsesForcedToolAndRestoresClaimIDs(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{toolResponse(`{
		"temperatures": {"label":"not_addressed","rationale":"The output says nothing about temperatures."},
		"complete": {"label":"entailed","rationale":"The output says every alarm is listed."}
	}`)}}
	claims := []aieval.Claim{
		{ID: "complete", Text: "The answer is complete."},
		{ID: "temperatures", Text: "All temperatures are normal."},
	}

	judgments, err := newTestJudge(t, client).Judge(context.Background(), "Every alarm is listed.", claims)

	require.NoError(t, err)
	assert.Equal(t, []aieval.Judgment{
		{
			ClaimID:   "complete",
			Label:     aieval.Entailed,
			Rationale: "The output says every alarm is listed.",
		},
		{
			ClaimID:   "temperatures",
			Label:     aieval.NotAddressed,
			Rationale: "The output says nothing about temperatures.",
		},
	}, judgments)
	require.Len(t, client.requests, 1)
	request := client.requests[0]
	assert.Equal(t, 1024, request.MaxTokens)
	assert.Equal(t, model.ModelClassHighReasoning, request.ModelClass)
	assert.Nil(t, request.StructuredOutput)
	require.Len(t, request.Tools, 1)
	assert.Equal(t, string(submitJudgmentsID), request.Tools[0].Name)
	require.NotNil(t, request.ToolChoice)
	assert.Equal(t, model.ToolChoiceModeTool, request.ToolChoice.Mode)
	assert.Equal(t, string(submitJudgmentsID), request.ToolChoice.Name)
	require.Len(t, request.Messages, 2)
	user := request.Messages[1].Parts[0].(model.TextPart).Text
	assert.JSONEq(t, `{
		"output":"Every alarm is listed."
	}`, user)
	assert.NotContains(t, user, "claim_id")
	var schema judgmentSchema
	require.NoError(t, json.Unmarshal(request.Tools[0].Input.Contract().Schema, &schema))
	assert.Equal(t, []string{"complete", "temperatures"}, schema.Required)
	assert.Len(t, schema.Properties, 2)
	for _, claim := range claims {
		var property judgmentSchema
		require.NoError(t, json.Unmarshal(schema.Properties[claim.ID], &property))
		assert.Equal(t, claim.Text, property.Description)
	}
}

func TestJudgeUsesRuntimeCorrectionForMalformedToolArguments(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"complete":{"label":"entailed","rationale":"The output states the claim."}}`),
	}}

	judgments, err := newTestJudge(t, client).Judge(
		context.Background(),
		"Done.",
		[]aieval.Claim{{ID: "complete", Text: "The work is complete."}},
	)

	require.NoError(t, err)
	require.Len(t, judgments, 1)
	assert.Equal(t, "complete", judgments[0].ClaimID)
	require.Len(t, client.requests, 2)
	for _, request := range client.requests {
		assert.Equal(t, 1024, request.MaxTokens)
	}
	assert.Nil(t, client.requests[1].StructuredOutput)
	assert.Contains(t, systemText(client.requests[1]), "system-reminder")
	assert.Contains(t, systemText(client.requests[1]), "judgments")
}

func TestJudgeStopsAfterRuntimeCorrectionLimit(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
	}}

	judgments, err := newTestJudge(t, client).Judge(
		context.Background(),
		"Done.",
		[]aieval.Claim{{ID: "complete", Text: "The work is complete."}},
	)

	assert.Nil(t, judgments)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `completion tool "eval.submit_judgments" did not succeed: recovery_cap`)
	assert.Contains(t, err.Error(), "judgments")
	assert.Contains(t, err.Error(), "missing property 'complete'")
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Len(t, client.requests, 4)
}

func TestRunnerRetainsRealJudgeDiagnostics(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		toolResponse(`{"calibration_entailed":{"label":"entailed","rationale":"Running."},"calibration_contradicted":{"label":"contradicted","rationale":"Not stopped."},"calibration_not_addressed":{"label":"not_addressed","rationale":"Not discussed."},"calibration_indeterminate":{"label":"indeterminate","rationale":"Conflicting readings."}}`),
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[]}`),
	}}
	runner, err := aieval.NewRunner(newTestJudge(t, client), aieval.RunnerConfig{MaxConcurrency: 1})
	require.NoError(t, err)
	report, err := runner.Run(t.Context(), aieval.Suite{
		ID: "diagnostics",
		Scenarios: []aieval.Scenario{{
			ID: "answer", Timeout: 10 * time.Second,
			Run: func(context.Context) (aieval.Result, error) {
				return aieval.Result{Output: "The work is complete.", Claims: []aieval.Claim{{ID: "complete", Text: "The work is complete."}}}, nil
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, report.Scenarios, 1)
	assert.False(t, report.Passed)
	assert.False(t, report.Scenarios[0].Passed)
	assert.Empty(t, report.Scenarios[0].Judgments)
	assert.Contains(t, report.Scenarios[0].Error, "recovery_cap")
	assert.Contains(t, report.Scenarios[0].Error, "missing property 'complete'")
	assert.Contains(t, report.Scenarios[0].Error, "judgments")
	assert.Len(t, client.requests, 5)
}

func TestJudgeReturnsValidContradictionWithoutError(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		toolResponse(`{"running":{"label":"contradicted","rationale":"The output says the opposite."}}`),
	}}
	judgments, err := newTestJudge(t, client).Judge(t.Context(), "The pump is stopped.", []aieval.Claim{{ID: "running", Text: "The pump is running."}})
	require.NoError(t, err)
	require.Len(t, judgments, 1)
	assert.Equal(t, aieval.Contradicted, judgments[0].Label)
	assert.Len(t, client.requests, 1)
}

func TestJudgeRejectsInvalidClaimsBeforeInference(t *testing.T) {
	client := &recordingClient{}

	_, err := newTestJudge(t, client).Judge(
		context.Background(),
		"Done.",
		[]aieval.Claim{{ID: "duplicate", Text: "One."}, {ID: "duplicate", Text: "Two."}},
	)

	require.ErrorContains(t, err, `duplicate claim "duplicate"`)
	assert.Empty(t, client.requests)
}

func TestJudgeDoesNotRetryProviderErrors(t *testing.T) {
	want := errors.New("provider unavailable")
	client := &recordingClient{errors: []error{want}}

	_, err := newTestJudge(t, client).Judge(
		context.Background(),
		"Done.",
		[]aieval.Claim{{ID: "complete", Text: "Complete."}},
	)

	require.ErrorIs(t, err, want)
	assert.Len(t, client.requests, 1)
}

func TestWithModelClassOverridesRequestClass(t *testing.T) {
	client := &recordingClient{errors: []error{errors.New("stop")}}
	judge := newTestJudge(t, client, WithModelClass(model.ModelClassSmall))

	_, err := judge.Judge(
		context.Background(),
		"Output.",
		[]aieval.Claim{{ID: "claim", Text: "Claim."}},
	)

	require.ErrorContains(t, err, "stop")
	require.Len(t, client.requests, 1)
	assert.Equal(t, model.ModelClassSmall, client.requests[0].ModelClass)
}

func toolResponse(payload string) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "submit-call",
				Name:  string(submitJudgmentsID),
				Input: rawjson.Message(payload),
			}},
		}},
		StopReason: "tool_use",
	}
}

func systemText(request *model.Request) string {
	var text string
	for _, message := range request.Messages {
		if message.Role != model.ConversationRoleSystem {
			continue
		}
		for _, part := range message.Parts {
			if value, ok := part.(model.TextPart); ok {
				text += value.Text
			}
		}
	}
	return text
}
