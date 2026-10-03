// These tests use the real SDK with an in-memory HTTP transport. They compare
// forced and automatic grading requests and retain validation failures without
// contacting a model, a credential service or any other network endpoint.
package judge_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/judge"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tooloutput"
)

func TestJudgeAutomaticAnthropicHTTPPreservesContract(t *testing.T) {
	for _, transportCase := range []struct {
		budget int
		method string
	}{
		{1024, "invoke"},
		{32768, "invoke-with-response-stream"},
	} {
		t.Run(transportCase.method, func(t *testing.T) {
			var forcedBody map[string]json.RawMessage
			for _, constructor := range []struct {
				name string
				new  func(model.Client, int, ...judge.Option) (*judge.Judge, error)
			}{
				{"forced", judge.New},
				{"automatic", judge.NewAutomatic},
			} {
				t.Run(constructor.name, func(t *testing.T) {
					const accepted = `{"complete":{"label":"contradicted","rationale":"The candidate says the opposite."}}`
					transport := &judgmentHTTPTransport{
						t: t, stopReason: "tool_use", outputTokens: 64,
						argumentsByRequest: []string{`{"complete":{"label":"unsupported","rationale":"Invalid label."}}`, accepted},
					}
					client, recorder := newJudgmentHTTPClient(t, transport)
					evaluator, err := constructor.new(client, transportCase.budget)
					require.NoError(t, err)
					claims := []aieval.Claim{{ID: "complete", Text: "The candidate says the reading increased."}}

					judgments, err := evaluator.Judge(t.Context(), "The reading decreased.", claims, "The observed reading increased.")

					require.NoError(t, err)
					assert.Equal(t, []aieval.Judgment{{ClaimID: "complete", Label: aieval.Contradicted, Rationale: "The candidate says the opposite."}}, judgments)
					require.Len(t, transport.requests, 2)
					require.Len(t, recorder.responses, 2)
					for index, request := range transport.requests {
						assert.Equal(t, "/model/"+judgmentTransportModel+"/"+transportCase.method, request.path)
						assert.Equal(t, transportCase.budget, request.body.MaxTokens)
						require.Len(t, request.body.Tools, 1)
						var body map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(request.raw, &body))
						assert.NotContains(t, body, "output_config")
						if constructor.name == "automatic" {
							assert.NotContains(t, body, "tool_choice", "the adapter encodes automatic selection using the provider default")
						} else {
							assert.Equal(t, "tool", request.body.ToolChoice.Type)
							assert.Equal(t, request.body.Tools[0].Name, request.body.ToolChoice.Name)
							delete(body, "tool_choice")
						}
						if index == 0 {
							if constructor.name == "forced" {
								forcedBody = body
							} else {
								require.Len(t, body, len(forcedBody))
								for field, expected := range forcedBody {
									assert.JSONEq(t, string(expected), string(body[field]), "only tool choice may differ in field %s", field)
								}
							}
						}
						assert.JSONEq(t, string(transport.requests[0].body.Tools[0].InputSchema), string(request.body.Tools[0].InputSchema))
						response := recorder.responses[index]
						require.NotNil(t, response)
						assert.Equal(t, "tool_use", response.StopReason)
						assert.False(t, response.OutputLimited)
						assert.Equal(t, model.TokenUsage{
							Model: judgmentTransportModel, ModelClass: model.ModelClassHighReasoning,
							InputTokens: 123, OutputTokens: 64, TotalTokens: 187,
						}, response.Usage)
						calls := response.ToolCalls()
						require.Len(t, calls, 1)
						assert.Equal(t, "eval.submit_judgments", string(calls[0].Name))
						assert.Equal(t, transport.argumentsByRequest[index], string(calls[0].Payload))
					}
				})
			}
		})
	}
}

func TestJudgeAutomaticAnthropicHTTPRejectsInvalidAndLimitedOutput(t *testing.T) {
	for _, budget := range []int{1024, 32768} {
		for _, test := range []struct {
			name      string
			arguments string
			stop      string
			calls     int
		}{
			{"invalid claims", `{"other":{"label":"entailed","rationale":"Not the required claim."}}`, "tool_use", 4},
			{"limited valid claims", `{"complete":{"label":"entailed","rationale":"Syntactically valid."}}`, "max_tokens", 1},
		} {
			t.Run(fmt.Sprintf("%d/%s", budget, test.name), func(t *testing.T) {
				transport := &judgmentHTTPTransport{
					t: t, arguments: test.arguments, stopReason: test.stop, outputTokens: 64,
				}
				client, recorder := newJudgmentHTTPClient(t, transport)
				evaluator, err := judge.NewAutomatic(client, budget)
				require.NoError(t, err)

				judgments, err := evaluator.Judge(t.Context(), "Candidate.", []aieval.Claim{{ID: "complete", Text: "The candidate reports the reading."}}, "")

				require.Error(t, err)
				assert.Nil(t, judgments)
				var failure *tooloutput.RunError
				require.ErrorAs(t, err, &failure)
				if test.stop == "tool_use" {
					require.ErrorContains(t, failure.TerminalError(), "recovery_cap")
					var rejected *model.OutputValidationError
					require.ErrorAs(t, err, &rejected)
					assert.Equal(t, &model.TokenUsage{
						Model: judgmentTransportModel, ModelClass: model.ModelClassHighReasoning,
						InputTokens: 123, OutputTokens: 64, TotalTokens: 187,
					}, rejected.Usage())
				} else {
					require.ErrorContains(t, failure.TerminalError(), "generated-output limit")
				}
				assert.Len(t, transport.requests, test.calls)
				assert.Len(t, recorder.responses, test.calls)
			})
		}
	}
}

func TestJudgeAutomaticAnthropicHTTPPreservesPartialStreamFailure(t *testing.T) {
	transport := &judgmentHTTPTransport{
		t:            t,
		arguments:    `{"complete":{"label":"entailed","rationale":"unfinished`,
		stopReason:   "max_tokens",
		outputTokens: 32768,
	}
	client, recorder := newJudgmentHTTPClient(t, transport)
	evaluator, err := judge.NewAutomatic(client, 32768)
	require.NoError(t, err)

	judgments, err := evaluator.Judge(t.Context(), "Candidate.", []aieval.Claim{{ID: "complete", Text: "The candidate reports the reading."}}, "")

	require.Error(t, err)
	assert.Nil(t, judgments)
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, model.OutputValidationResponseShape, rejected.Kind())
	require.ErrorContains(t, err, "tool payload is not valid JSON")
	require.ErrorContains(t, err, "stop_reason=unknown output_limited=unknown")
	assert.Equal(t, &model.TokenUsage{
		Model: judgmentTransportModel, ModelClass: model.ModelClassHighReasoning,
		InputTokens: 123, TotalTokens: 123,
	}, rejected.Usage())
	assert.Len(t, transport.requests, 1)
	require.Len(t, recorder.responses, 1)
	assert.Nil(t, recorder.responses[0])
}
