package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// These tests run the official SDK and SigV4 signer, but replace HTTP with an
// in-memory transport. They prove emitted requests and local output validation,
// not model availability, remote schema enforcement or selection quality.
type bedrockRoundTripFunc func(*http.Request) (*http.Response, error)

const (
	bedrockTestModel     = "global.openai.gpt-5.6-terra"
	bedrockTestSchema    = `{"type":"object","additionalProperties":false,"properties":{"retained":{"type":"array","items":{"$ref":"#/$defs/selection"}}},"required":["retained"],"$defs":{"selection":{"type":"object","additionalProperties":false,"properties":{"request_index":{"type":"integer","minimum":0,"maximum":9007199254740993},"source_indexes":{"type":"array","items":{"type":"integer","minimum":0}}},"required":["request_index","source_indexes"]}},"example":{"retained":[{"request_index":0,"source_indexes":[1,2]}]},"x-tool-purpose":"Select relevant candidates; preserve [] when none match."}`
	bedrockTestArguments = ` {"retained":[{"request_index":0,"source_indexes":[1,2]},{"request_index":1,"source_indexes":[]}]} `
	bedrockReasoning     = `{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"opaque+/=\\unchanged","content":[]}`
)

func TestBedrockResponsesSDKRequestAndReplay(t *testing.T) {
	t.Setenv("AWS_BEDROCK_BASE_URL", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "must-not-be-used")
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	var bodies []json.RawMessage
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "POST", req.Method)
		assert.Equal(t, "https://bedrock-runtime.us-west-2.amazonaws.com/openai/v1/responses", req.URL.String())
		assert.True(t, strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 "))
		assert.Contains(t, req.Header.Get("Authorization"), "/us-west-2/bedrock/aws4_request")
		assert.Equal(t, "test-session", req.Header.Get("X-Amz-Security-Token"))
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, body)
		return bedrockJSONResponse(bedrockToolResponse(t, bedrockReasoning, "screen_select", bedrockTestArguments)), nil
	}))
	request := bedrockTestRequest()
	response, err := client.Complete(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "screen.select", response.ToolCalls()[0].Name.String())
	assert.True(t, bytes.Equal([]byte(bedrockTestArguments), response.ToolCalls()[0].Payload), "tool arguments must remain byte-for-byte unchanged")
	assert.Equal(t, 11, response.Usage.CacheWriteTokens)
	assert.Equal(t, 7, response.Usage.CacheReadTokens)
	require.Len(t, response.Content, 2)
	assert.JSONEq(t, bedrockReasoning, response.Content[0].Meta[openAIReasoningItemsMetaKey].([]string)[0])
	for i := range response.Content {
		request.Messages = append(request.Messages, &response.Content[i])
	}
	request.Messages = append(request.Messages, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{ToolUseID: "call_1", Content: map[string]any{"accepted": true}}},
	})
	_, err = client.Complete(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, bodies, 2)
	for _, body := range bodies {
		var wire struct {
			Model              string          `json:"model"`
			Store              bool            `json:"store"`
			Background         bool            `json:"background"`
			Truncation         string          `json:"truncation"`
			MaxOutputTokens    int             `json:"max_output_tokens"`
			PromptCacheOptions json.RawMessage `json:"prompt_cache_options"`
			Reasoning          json.RawMessage `json:"reasoning"`
			Include            []string        `json:"include"`
			ToolChoice         json.RawMessage `json:"tool_choice"`
			Tools              []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Strict      *bool           `json:"strict"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"tools"`
		}
		require.NoError(t, json.Unmarshal(body, &wire))
		assert.Equal(t, bedrockTestModel, wire.Model)
		assert.False(t, wire.Store)
		assert.False(t, wire.Background)
		assert.Equal(t, "disabled", wire.Truncation)
		assert.Equal(t, 32768, wire.MaxOutputTokens)
		assert.JSONEq(t, `{"mode":"explicit"}`, string(wire.PromptCacheOptions))
		assert.JSONEq(t, `{"effort":"medium","summary":"auto"}`, string(wire.Reasoning))
		assert.Equal(t, []string{"reasoning.encrypted_content"}, wire.Include)
		assert.JSONEq(t, `{"type":"function","name":"screen_select"}`, string(wire.ToolChoice))
		require.Len(t, wire.Tools, 1)
		assert.Equal(t, "screen_select", wire.Tools[0].Name)
		assert.Equal(t, request.Tools[0].Description, wire.Tools[0].Description)
		require.NotNil(t, wire.Tools[0].Strict)
		assert.False(t, *wire.Tools[0].Strict)
		assert.JSONEq(t, bedrockTestSchema, string(wire.Tools[0].Parameters))
		assert.Contains(t, string(wire.Tools[0].Parameters), `"maximum":9007199254740993`)
		assert.NotContains(t, string(body), "input_examples")
	}
	var first, resumed struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(bodies[0], &first))
	require.NoError(t, json.Unmarshal(bodies[1], &resumed))
	require.Len(t, first.Input, 2)
	require.Len(t, resumed.Input, 5)
	var system struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(first.Input[0], &system))
	assert.Equal(t, "System \"instructions\"\nΔ", system.Content)
	var user struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(first.Input[1], &user))
	require.Len(t, user.Content, 1)
	assert.Equal(t, "Candidate list\n[] and \\ paths", user.Content[0].Text)
	for i := range first.Input {
		assert.JSONEq(t, string(first.Input[i]), string(resumed.Input[i]))
	}
	assert.JSONEq(t, bedrockReasoning, string(resumed.Input[2]))
	var toolCall struct {
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
	}
	require.NoError(t, json.Unmarshal(resumed.Input[3], &toolCall))
	assert.True(t, bytes.Equal([]byte(bedrockTestArguments), []byte(toolCall.Arguments)), "replayed arguments must remain byte-for-byte unchanged")
	assert.Equal(t, "call_1", toolCall.CallID)
	assert.JSONEq(t, `{"type":"function_call_output","id":"tool_result_4_0","call_id":"call_1","output":"{\"accepted\":true}"}`, string(resumed.Input[4]))
}

func TestBedrockResponsesConstructorContract(t *testing.T) {
	t.Setenv("AWS_BEDROCK_BASE_URL", "")
	creds := credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session")
	opts := Options{DefaultModel: bedrockTestModel}
	_, err := NewBedrockProvider(t.Context(), "", creds, opts)
	require.ErrorContains(t, err, "requires an AWS region")
	_, err = NewBedrockProvider(t.Context(), "us-west-2", nil, opts)
	require.ErrorContains(t, err, "requires an AWS credentials provider")
	_, err = NewBedrockProvider(t.Context(), "us-west-2", creds, Options{DefaultModel: bedrockTestModel, transport: &mockTransport{}})
	require.ErrorContains(t, err, "constructs its own SDK client")
	sdk := openaisdk.NewClient(option.WithAPIKey("test-key"))
	_, err = NewBedrockProvider(t.Context(), "us-west-2", creds, Options{DefaultModel: bedrockTestModel, Client: &sdk.Responses})
	require.ErrorContains(t, err, "constructs its own SDK client")
	var calls atomic.Int32
	client, err := NewBedrock(t.Context(), "us-west-2", creds, opts,
		option.WithBaseURL("https://custom.example/openai/v1"),
		option.WithHTTPClient(&http.Client{Transport: bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("HTTP must not be called")
		})}))
	require.NoError(t, err)
	request := bedrockTestRequest()
	request.Thinking = nil
	_, err = client.Complete(t.Context(), request)
	require.ErrorContains(t, err, "option.WithBaseURL")
	assert.Zero(t, calls.Load())
	sentinel := errors.New("test credential refresh cause")
	_, err = NewBedrockProvider(t.Context(), "us-west-2", aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, sentinel
	}), opts)
	assert.ErrorIs(t, err, sentinel)
}

func TestBedrockResponsesSDKEndpointOverride(t *testing.T) {
	t.Setenv("AWS_BEDROCK_BASE_URL", "https://custom.example/openai/v1")
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://custom.example/openai/v1/responses", req.URL.String())
		assert.Contains(t, req.Header.Get("Authorization"), "/us-west-2/bedrock/aws4_request")
		return bedrockJSONResponse(bedrockToolResponse(t, "", "screen_select", bedrockTestArguments)), nil
	}))
	_, err := client.Complete(t.Context(), bedrockTestRequest())
	require.NoError(t, err)
}

func TestBedrockResponsesRejectUnsupportedBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("HTTP must not be called")
	}))
	for _, test := range []struct {
		name    string
		change  func(*model.Request)
		message string
	}{
		{"after_system", func(r *model.Request) { r.Cache = &model.CacheOptions{AfterSystem: true} }, "caching is not supported"},
		{"after_tools", func(r *model.Request) { r.Cache = &model.CacheOptions{AfterTools: true} }, "caching is not supported"},
		{"inline_cache", func(r *model.Request) { r.Messages[0].Parts = append(r.Messages[0].Parts, model.CacheCheckpointPart{}) }, "cache checkpoints"},
		{"structured_output", func(r *model.Request) {
			r.Tools = nil
			r.ToolChoice = nil
			r.StructuredOutput = &model.StructuredOutput{Name: "answer", Schema: rawjson.Message(`{"type":"object"}`)}
		}, "structured output not supported"},
		{"thinking_budget", func(r *model.Request) { r.Thinking.BudgetTokens = 100 }, "thinking budgets are not supported"},
		{"thinking_interleaved", func(r *model.Request) { r.Thinking.Interleaved = true }, "interleaved thinking is not supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := bedrockTestRequest()
			test.change(request)
			_, err := client.Complete(t.Context(), request)
			require.ErrorContains(t, err, test.message)
			_, err = client.Stream(t.Context(), request)
			require.ErrorContains(t, err, test.message)
			if test.name == "structured_output" {
				assert.ErrorIs(t, err, model.ErrStructuredOutputUnsupported)
			}
		})
	}
	_, err := client.CountTokens(t.Context(), bedrockTestRequest())
	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
	assert.Zero(t, calls.Load())
}

func TestBedrockResponsesSDKInvalidArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"retained":"[{\"request_index\":0,\"source_indexes\":[1,2]}]"}`,
		`{"retained":[{"request_index":0,"source_indexes":"[1,2]"}]}`,
		`{"retained":[{"request_index":-1,"source_indexes":[]}]}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return bedrockJSONResponse(bedrockToolResponse(t, "", "screen_select", arguments)), nil
			}))
			_, err := client.Complete(t.Context(), bedrockTestRequest())
			var invalid *model.OutputValidationError
			require.ErrorAs(t, err, &invalid)
			streaming := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return bedrockToolStreamResponse(t, "screen_select", arguments), nil
			}))
			stream, err := streaming.Stream(t.Context(), bedrockTestRequest())
			require.NoError(t, err)
			defer func() { assert.NoError(t, stream.Close()) }()
			for err == nil {
				_, err = stream.Recv()
			}
			require.ErrorAs(t, err, &invalid)
			assert.Nil(t, stream.Response())
		})
	}
}

func TestBedrockResponsesSDKUnknownToolName(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", streaming), func(t *testing.T) {
			client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if streaming {
					return bedrockToolStreamResponse(t, "invented", bedrockTestArguments), nil
				}
				return bedrockJSONResponse(bedrockToolResponse(t, "", "invented", bedrockTestArguments)), nil
			}))
			var err error
			if streaming {
				stream, streamErr := client.Stream(t.Context(), bedrockTestRequest())
				require.NoError(t, streamErr)
				defer func() { assert.NoError(t, stream.Close()) }()
				for err == nil {
					_, err = stream.Recv()
				}
			} else {
				_, err = client.Complete(t.Context(), bedrockTestRequest())
			}
			name, ok := model.UnadvertisedToolName(err)
			assert.True(t, ok)
			assert.Equal(t, "invented", name)
		})
	}
}

func TestBedrockResponsesSDKStreamMultipleTools(t *testing.T) {
	response := bedrockToolResponse(t, bedrockReasoning, "screen_select", bedrockTestArguments)
	var snapshot map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response), &snapshot))
	output := make([]json.RawMessage, 0, 3)
	require.NoError(t, json.Unmarshal(snapshot["output"], &output))
	output = append(output, json.RawMessage(`{"type":"function_call","id":"fc_2","call_id":"call_2","name":"screen_select","arguments":"{\"retained\":[]}","status":"completed"}`))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	snapshot["output"] = encoded
	encoded, err = json.Marshal(snapshot)
	require.NoError(t, err)
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			return nil, readErr
		}
		assert.Contains(t, string(body), `"stream":true`)
		sse := "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":" + string(encoded) + "}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
	}))
	stream, err := client.Stream(t.Context(), bedrockTestRequest())
	require.NoError(t, err)
	defer func() { assert.NoError(t, stream.Close()) }()
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	result := stream.Response()
	require.NotNil(t, result)
	require.Len(t, result.ToolCalls(), 2)
	assert.Equal(t, "call_1", result.ToolCalls()[0].ID)
	assert.True(t, bytes.Equal([]byte(bedrockTestArguments), result.ToolCalls()[0].Payload), "streamed arguments must remain byte-for-byte unchanged")
	assert.Equal(t, "call_2", result.ToolCalls()[1].ID)
	assert.JSONEq(t, `{"retained":[]}`, string(result.ToolCalls()[1].Payload))
	assert.Equal(t, 11, result.Usage.CacheWriteTokens)
	assert.Equal(t, "tool_calls", result.StopReason)
}

func TestBedrockResponsesSDKErrorsAndCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			var calls atomic.Int32
			client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Exact provider failure: quota 12 exceeded for route test."}}`))}, nil
			}))
			var err error
			if stream {
				var result *model.ValidatedStream
				result, err = client.Stream(t.Context(), bedrockTestRequest())
				if err == nil {
					_, err = result.Recv()
					require.NoError(t, result.Close())
				}
			} else {
				_, err = client.Complete(t.Context(), bedrockTestRequest())
			}
			require.ErrorIs(t, err, model.ErrRateLimited)
			require.ErrorContains(t, err, "Exact provider failure: quota 12 exceeded for route test.")
			providerErr, ok := model.AsProviderError(err)
			require.True(t, ok)
			assert.Equal(t, "openai", providerErr.Provider())
			operation := "responses.create"
			if stream {
				operation = "responses.stream"
			}
			assert.Equal(t, operation, providerErr.Operation())
			assert.EqualValues(t, 1, calls.Load())
		})
	}
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.Complete(ctx, bedrockTestRequest())
	require.ErrorIs(t, err, context.Canceled)
	stream, err := client.Stream(ctx, bedrockTestRequest())
	if err == nil {
		_, err = stream.Recv()
		require.ErrorIs(t, stream.Close(), context.Canceled)
	}
	assert.ErrorIs(t, err, context.Canceled)
}

func TestOpenAISDKStrictSchemaKeepsNumericTypes(t *testing.T) {
	var body []byte
	sdk := openaisdk.NewClient(option.WithAPIKey("test-key"), option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return bedrockJSONResponse(`{"id":"r","model":"gpt-5","status":"completed","output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`), nil
	})}))
	client, err := New(Options{Client: &sdk.Responses, DefaultModel: "gpt-5"})
	require.NoError(t, err)
	request := bedrockTestRequest()
	request.Thinking = nil
	request.ToolChoice = nil
	request.Tools[0].Input = mustOpenAIToolInput(rawjson.Message(`{"type":"object","properties":{"count":{"type":"integer","minimum":0,"maximum":9007199254740993},"label":{"type":"string"}},"required":["count"]}`))
	_, err = client.Complete(t.Context(), request)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"strict":true`)
	assert.Contains(t, string(body), `"maximum":9007199254740993`)
	assert.Contains(t, string(body), `"minimum":0`)
	assert.Contains(t, string(body), `"anyOf":[{"type":"string"},{"type":"null"}]`)
}

func newBedrockTestClient(t *testing.T, transport http.RoundTripper) model.Client {
	t.Helper()
	client, err := NewBedrock(t.Context(), "us-west-2",
		credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"),
		Options{DefaultModel: bedrockTestModel, MaxCompletionTokens: 32768, ThinkingEffort: "medium"},
		option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: transport}))
	require.NoError(t, err)
	return client
}

func bedrockTestRequest() *model.Request {
	return &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "System \"instructions\"\nΔ"}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Candidate list\n[] and \\ paths"}}},
		},
		Tools:      []*model.ToolDefinition{{Name: "screen.select", Description: "Select candidates for each request.", Input: mustOpenAIToolInput(rawjson.Message(bedrockTestSchema))}},
		ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "screen.select"},
		Thinking:   &model.ThinkingOptions{Enable: true},
	}
}

func bedrockToolResponse(t *testing.T, reasoning, name, arguments string) string {
	t.Helper()
	call, err := json.Marshal(map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": name, "arguments": arguments, "status": "completed"})
	require.NoError(t, err)
	output := string(call)
	if reasoning != "" {
		output = reasoning + "," + output
	}
	return `{"id":"resp_1","model":"` + bedrockTestModel + `","status":"completed","output":[` + output + `],"usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,"input_tokens_details":{"cached_tokens":7,"cache_write_tokens":11},"output_tokens_details":{"reasoning_tokens":3}}}`
}

func bedrockJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// bedrockToolStreamResponse includes progressive argument bytes and a final
// snapshot, exercising both SDK streaming paths before canonical validation.
func bedrockToolStreamResponse(t *testing.T, name, arguments string) *http.Response {
	t.Helper()
	added, err := json.Marshal(map[string]any{
		"type": "response.output_item.added", "sequence_number": 1, "output_index": 0,
		"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": name, "arguments": "", "status": "in_progress"},
	})
	require.NoError(t, err)
	delta, err := json.Marshal(map[string]any{
		"type": "response.function_call_arguments.delta", "sequence_number": 2, "output_index": 0, "item_id": "fc_1", "delta": arguments,
	})
	require.NoError(t, err)
	response := bedrockToolResponse(t, "", name, arguments)
	sse := "data: " + string(added) + "\n\ndata: " + string(delta) + "\n\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":" + response + "}\n\ndata: [DONE]\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
}

func (f bedrockRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
