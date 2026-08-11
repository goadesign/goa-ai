package judge

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

type recordingClient struct {
	request  *model.Request
	response *model.Response
	err      error
}

func (c *recordingClient) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	c.request = request
	return c.response, c.err
}

func (c *recordingClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}

func TestJudgeUsesStrictHighReasoningRequest(t *testing.T) {
	client := &recordingClient{response: modelResponse(`{
		"judgments": [
			{"claim_id":"complete","label":"entailed","rationale":"The output says every alarm is listed."}
		]
	}`)}
	assertions := []aieval.Assertion{{
		ClaimID: "complete", Output: "Every alarm is listed.", Claim: "The answer is complete.",
	}}

	judgments, err := New(client).Judge(context.Background(), assertions)

	require.NoError(t, err)
	assert.Equal(t, []aieval.Judgment{{
		ClaimID: "complete", Label: aieval.Entailed,
		Rationale: "The output says every alarm is listed.",
	}}, judgments)
	require.NotNil(t, client.request)
	assert.Equal(t, model.ModelClassHighReasoning, client.request.ModelClass)
	assert.Zero(t, client.request.Temperature)
	assert.Empty(t, client.request.Tools)
	assert.Nil(t, client.request.ToolChoice)
	require.NotNil(t, client.request.StructuredOutput)
	assert.Equal(t, "eval_judgments", client.request.StructuredOutput.Name)
	assert.JSONEq(t, responseSchema, string(client.request.StructuredOutput.Schema))
	require.Len(t, client.request.Messages, 2)
	user := client.request.Messages[1].Parts[0].(model.TextPart).Text
	assert.JSONEq(t, `{"assertions":[{"claim_id":"complete","output":"Every alarm is listed.","claim":"The answer is complete."}]}`, user)
}

func TestJudgeRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown field",
			body:    `{"judgments":[],"extra":true}`,
			wantErr: "unknown field",
		},
		{
			name:    "trailing value",
			body:    `{"judgments":[]} {"judgments":[]}`,
			wantErr: "trailing JSON value",
		},
		{
			name:    "missing judgment",
			body:    `{"judgments":[]}`,
			wantErr: "0 judgments for 1 claims",
		},
		{
			name: "duplicate judgment",
			body: `{"judgments":[
				{"claim_id":"complete","label":"entailed","rationale":"One."},
				{"claim_id":"complete","label":"entailed","rationale":"Two."}
			]}`,
			wantErr: "2 judgments for 1 claims",
		},
		{
			name: "unknown claim",
			body: `{"judgments":[
				{"claim_id":"other","label":"entailed","rationale":"Other."}
			]}`,
			wantErr: "unknown claim",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingClient{response: modelResponse(test.body)}
			_, err := New(client).Judge(context.Background(), []aieval.Assertion{{
				ClaimID: "complete", Output: "Done.", Claim: "Complete.",
			}})
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestJudgeDoesNotRetryModelErrors(t *testing.T) {
	want := errors.New("provider unavailable")
	client := &recordingClient{err: want}
	_, err := New(client).Judge(context.Background(), []aieval.Assertion{{
		ClaimID: "complete", Output: "Done.", Claim: "Complete.",
	}})
	assert.ErrorIs(t, err, want)
}

func modelResponse(body string) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: body}},
		}},
		StopReason: "end_turn",
	}
}

func TestWithModelClassOverridesRequestClass(t *testing.T) {
	client := &recordingClient{err: errors.New("stop")}
	j := New(client, WithModelClass(model.ModelClassSmall))
	_, _ = j.Judge(context.Background(), []aieval.Assertion{{ClaimID: "c1", Output: "o", Claim: "c"}})
	require.NotNil(t, client.request)
	assert.Equal(t, model.ModelClassSmall, client.request.ModelClass)
}
