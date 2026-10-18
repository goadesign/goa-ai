// These tests send synthetic structured requests through the official SDK and
// signer with in-memory HTTP. They verify unary schema encoding and validation,
// while streaming rejects the unsupported request before HTTP.
package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestBedrockResponsesStructuredOutput(t *testing.T) {
	for _, test := range []struct {
		name        string
		newProvider func(context.Context, string, aws.CredentialsProvider, Options, ...option.RequestOption) (model.Provider, error)
	}{
		{name: "complete tool schemas", newProvider: NewBedrockProvider},
		{name: "strict tool schemas", newProvider: NewBedrockStrictProvider},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			provider, err := test.newProvider(t.Context(), "us-west-2",
				credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"),
				Options{DefaultModel: bedrockTestModel},
				option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: bedrockRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls.Add(1)
					assert.Equal(t, "https://bedrock-runtime.us-west-2.amazonaws.com/openai/v1/responses", request.URL.String())
					assert.Contains(t, request.Header.Get("Authorization"), "/us-west-2/bedrock/aws4_request")
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					var params responses.ResponseNewParams
					require.NoError(t, params.UnmarshalJSON(body))
					format := params.Text.Format.OfJSONSchema
					require.NotNil(t, format)
					assert.Equal(t, "summary", format.Name)
					assert.True(t, format.Strict.Value)
					compiler := jsonschema.NewCompiler()
					require.NoError(t, compiler.AddResource("https://example.invalid/summary", format.Schema))
					schema, err := compiler.Compile("https://example.invalid/summary")
					require.NoError(t, err)
					assert.NoError(t, schema.Validate(map[string]any{"result": "ok", "note": nil}))
					assert.NoError(t, schema.Validate(map[string]any{"result": "ok", "note": "detail"}))
					require.Error(t, schema.Validate(map[string]any{"result": "ok"}))
					require.Error(t, schema.Validate(map[string]any{"result": "ok", "note": 7}))
					require.Error(t, schema.Validate(map[string]any{"result": "ok", "note": nil, "extra": true}))
					assert.False(t, params.Store.Value)
					assert.False(t, params.Background.Value)
					return bedrockJSONResponse(`{"id":"resp_1","status":"completed","output":[` + bedrockReasoning + `,{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"result\":\"ok\",\"note\":null}"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}`), nil
				})}))
			require.NoError(t, err)
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			request := bedrockStructuredOutputRequest()
			count, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.False(t, count.Exact)
			plainCount, err := client.CountTokens(t.Context(), &model.Request{Messages: request.Messages})
			require.NoError(t, err)
			assert.Greater(t, count.InputTokens, plainCount.InputTokens)
			assert.Zero(t, calls.Load())

			response, err := client.Complete(t.Context(), request)
			require.NoError(t, err)
			require.Len(t, response.Content, 1)
			assert.JSONEq(t, `{"result":"ok"}`, response.Content[0].Text())
			assert.Equal(t, []string{bedrockReasoning}, response.Content[0].Meta[openAIReasoningItemsMetaKey])
			assert.Equal(t, "stop", response.StopReason)
			assert.False(t, response.OutputLimited)
			assert.Equal(t, 1, int(calls.Load()))
		})
	}
}

func TestBedrockResponsesStructuredOutputRejectsInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name            string
		response        string
		providerFailure bool
	}{
		{
			name:     "invalid JSON",
			response: `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"not JSON"}]}]}`,
		},
		{
			name:     "wrong result type",
			response: `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"{\"result\":7,\"note\":null}"}]}]}`,
		},
		{
			name:     "refusal",
			response: `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot provide this result."}]}]}`,
		},
		{
			name:     "incomplete JSON",
			response: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"{\"result\":"}]}]}`,
		},
		{
			name:            "provider failure",
			response:        `{"status":"failed","error":{"code":"server_error","message":"Synthetic provider failure."},"output":[]}`,
			providerFailure: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return bedrockJSONResponse(test.response), nil
			}))
			response, err := client.Complete(t.Context(), bedrockStructuredOutputRequest())
			require.Error(t, err)
			assert.Nil(t, response)
			require.NotErrorIs(t, err, model.ErrStructuredOutputUnsupported)
			_, providerFailure := model.AsProviderError(err)
			assert.Equal(t, test.providerFailure, providerFailure)
			assert.Equal(t, 1, int(calls.Load()))
		})
	}
}

func TestBedrockResponsesStructuredStreamRejectsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("HTTP must not be called")
	}))
	for _, streamHint := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream hint %t", streamHint), func(t *testing.T) {
			request := bedrockStructuredOutputRequest()
			request.Stream = streamHint
			stream, err := client.Stream(t.Context(), request)
			require.ErrorIs(t, err, model.ErrStructuredOutputUnsupported)
			assert.Nil(t, stream)
		})
	}
	request := bedrockStructuredOutputRequest()
	request.StructuredOutput.Name = ""
	_, err := client.Stream(t.Context(), request)
	require.EqualError(t, err, "model request structured output name is required")
	assert.Zero(t, calls.Load())
}

// bedrockStructuredOutputRequest supplies one closed result schema to unary,
// counting, and streaming tests so each method receives the same contract.
func bedrockStructuredOutputRequest() *model.Request {
	return &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Summarize the synthetic record."}}}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "summary",
			Schema: rawjson.Message(`{"type":"object","properties":{"result":{"type":"string"},"note":{"type":"string"}},"required":["result"],"additionalProperties":false}`),
		},
	}
}
