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
	request   *model.Request
	requests  []*model.Request
	response  *model.Response
	responses []*model.Response
	err       error
}

func (c *recordingClient) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	c.request = request
	c.requests = append(c.requests, request)
	if len(c.responses) >= len(c.requests) {
		return c.responses[len(c.requests)-1], nil
	}
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
	assert.Empty(t, client.request.StructuredOutput.SchemaWithoutRootExample)
	assert.Empty(t, client.request.StructuredOutput.ExampleJSON)
	require.Len(t, client.request.Messages, 2)
	user := client.request.Messages[1].Parts[0].(model.TextPart).Text
	assert.JSONEq(t, `{"assertions":[{"claim_id":"complete","output":"Every alarm is listed.","claim":"The answer is complete."}]}`, user)
}

func TestJudgeCorrectsSchemaInvalidResponse(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		modelResponse(`{"judgments":"not an array"}`),
		modelResponse(`{"judgments":[
			{"claim_id":"complete","label":"entailed","rationale":"The output directly establishes the claim."}
		]}`),
	}}

	judgments, err := New(client).Judge(context.Background(), []aieval.Assertion{{
		ClaimID: "complete",
		Output:  "Every alarm is listed.",
		Claim:   "The answer is complete.",
	}})

	require.NoError(t, err)
	require.Len(t, judgments, 1)
	assert.Equal(t, aieval.Entailed, judgments[0].Label)
	require.Len(t, client.requests, 2)
	require.Len(t, client.requests[1].Messages, 4)
	rejected := client.requests[1].Messages[2].Parts[0].(model.TextPart).Text
	assert.JSONEq(t, `{"judgments":"not an array"}`, rejected)
	correction := client.requests[1].Messages[3].Parts[0].(model.TextPart).Text
	assert.Contains(t, correction, "cannot unmarshal string")
	assert.NotContains(t, correction, "JSON shape example")
}

func TestJudgeCorrectsClaimContractFailure(t *testing.T) {
	client := &recordingClient{responses: []*model.Response{
		modelResponse(`{"judgments":[
			{"claim_id":"other","label":"entailed","rationale":"The output establishes another claim."}
		]}`),
		modelResponse(`{"judgments":[
			{"claim_id":"complete","label":"entailed","rationale":"The output directly establishes the claim."}
		]}`),
	}}

	judgments, err := New(client).Judge(context.Background(), []aieval.Assertion{{
		ClaimID: "complete",
		Output:  "Every alarm is listed.",
		Claim:   "The answer is complete.",
	}})

	require.NoError(t, err)
	assert.Equal(t, []aieval.Judgment{{
		ClaimID:   "complete",
		Label:     aieval.Entailed,
		Rationale: "The output directly establishes the claim.",
	}}, judgments)
	require.Len(t, client.requests, 2)
	correction := client.requests[1].Messages[3].Parts[0].(model.TextPart).Text
	assert.Contains(t, correction, `judgment references unknown claim "other"`)
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
			require.ErrorContains(t, err, test.wantErr)
			assert.Len(t, client.requests, 2)
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
