package judge

import (
	"context"
	"errors"
	"sync"
	"testing"

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
	return New(client, opts...)
}

func TestJudgeUsesForcedToolAndRestoresClaimIDs(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{toolResponse(`{
		"judgments": [
			{"label":"entailed","rationale":"The output says every alarm is listed."},
			{"label":"not_addressed","rationale":"The output says nothing about temperatures."}
		]
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
		"output":"Every alarm is listed.",
		"claims":["The answer is complete.","All temperatures are normal."]
	}`, user)
	assert.NotContains(t, user, "claim_id")
	assert.Contains(t, string(request.Tools[0].Input.Contract().Schema), `"minItems": 2`)
	assert.Contains(t, string(request.Tools[0].Input.Contract().Schema), `"maxItems": 2`)
}

func TestJudgeUsesRuntimeCorrectionForMalformedToolArguments(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		toolResponse(`{"judgments":[]}`),
		toolResponse(`{"judgments":[{"label":"entailed","rationale":"The output states the claim."}]}`),
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
	assert.Len(t, client.requests, 4)
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

	_, _ = judge.Judge(
		context.Background(),
		"Output.",
		[]aieval.Claim{{ID: "claim", Text: "Claim."}},
	)

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
