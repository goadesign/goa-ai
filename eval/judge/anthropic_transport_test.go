// These tests send synthetic grading requests through the real Anthropic SDK
// and its Bedrock HTTP adapter. An in-memory transport supplies provider events;
// no credential service or model service is contacted. The real Judge still validates
// every returned tool argument and owns its existing correction limit.
package judge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkbedrock "github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/judge"
	anthropicprovider "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// judgmentHTTPTransport records actual SDK HTTP requests and returns one
	// synthetic provider response for each request, including correction calls.
	judgmentHTTPTransport struct {
		t                  *testing.T
		arguments          string
		argumentsByRequest []string
		stopReason         string
		outputTokens       int
		requests           []judgmentHTTPRequest
	}

	judgmentHTTPRequest struct {
		path string
		raw  []byte
		body struct {
			MaxTokens int             `json:"max_tokens"`
			Thinking  json.RawMessage `json:"thinking"`
			System    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"system"`
			ToolChoice struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tool_choice"`
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"input_schema"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
	}

	// judgmentResponseRecorder observes the assembled provider result without
	// changing it; model.NewClient still performs all tool-schema validation.
	judgmentResponseRecorder struct {
		model.Provider
		responses []*model.Response
	}
)

const judgmentTransportModel = "anthropic.claude-opus-5"

func TestJudgeAnthropicHTTPOutputAllowance(t *testing.T) {
	for _, test := range []struct {
		cap    int
		method string
	}{
		{cap: 21333, method: "invoke"},
		{cap: 21334, method: "invoke-with-response-stream"},
		{cap: 32768, method: "invoke-with-response-stream"},
	} {
		t.Run(fmt.Sprint(test.cap), func(t *testing.T) {
			arguments := `{"complete":{"label":"entailed","rationale":"All supplied measurements appear exactly."}}`
			transport := &judgmentHTTPTransport{t: t, arguments: arguments, stopReason: "tool_use", outputTokens: 300}
			client, recorder := newJudgmentHTTPClient(t, transport)
			evaluator, err := judge.New(client, test.cap)
			require.NoError(t, err)
			candidate := strings.Repeat("Synthetic reading: 12.5 °C; source <sample>\n", 300)
			claim := "The report preserves every reading. Reference evidence:\n" + strings.Repeat("Reading α: 12.5 °C\n", 500)

			judgments, err := evaluator.Judge(t.Context(), candidate, []aieval.Claim{{ID: "complete", Text: claim}}, "")

			require.NoError(t, err)
			assert.Equal(t, []aieval.Judgment{{ClaimID: "complete", Label: aieval.Entailed, Rationale: "All supplied measurements appear exactly."}}, judgments)
			require.Len(t, transport.requests, 1, "streaming is selected before sending, not after a failed unary request")
			assertJudgmentHTTPRequest(t, transport.requests[0], test.cap, test.method, candidate, claim)
			require.Len(t, recorder.responses, 1)
			response := recorder.responses[0]
			require.NotNil(t, response)
			assert.Equal(t, "tool_use", response.StopReason)
			assert.False(t, response.OutputLimited)
			assert.Equal(t, model.TokenUsage{Model: judgmentTransportModel, ModelClass: model.ModelClassHighReasoning, InputTokens: 123, OutputTokens: 300, TotalTokens: 423}, response.Usage)
			calls := response.ToolCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, "eval.submit_judgments", string(calls[0].Name))
			assert.Equal(t, arguments, string(calls[0].Payload))
		})
	}
}

func TestJudgeAnthropicHTTPRejectsIncompleteJudgments(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments string
		cause     string
	}{
		{name: "missing claim", arguments: `{}`, cause: "complete"},
		{name: "missing rationale", arguments: `{"complete":{"label":"entailed"}}`, cause: "rationale"},
		{name: "unknown field", arguments: `{"complete":{"label":"entailed","rationale":"Complete.","extra":"must not disappear"}}`, cause: "extra"},
		{name: "duplicate claim", arguments: `{"complete":{"label":"contradicted","rationale":"First."},"complete":{"label":"entailed","rationale":"Second."}}`, cause: "duplicate JSON member"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &judgmentHTTPTransport{t: t, arguments: test.arguments, stopReason: "max_tokens", outputTokens: 32768}
			client, recorder := newJudgmentHTTPClient(t, transport)
			evaluator, err := judge.New(client, 32768)
			require.NoError(t, err)
			candidate, claim := "Reading: 12.5 °C.", "The report includes the reading."

			judgments, err := evaluator.Judge(t.Context(), candidate, []aieval.Claim{{ID: "complete", Text: claim}}, "")

			require.Error(t, err)
			assert.Nil(t, judgments)
			require.ErrorContains(t, err, "recovery_cap")
			require.ErrorContains(t, err, test.cause)
			require.ErrorContains(t, err, `stop_reason="max_tokens"`)
			require.ErrorContains(t, err, "output_limited=true")
			require.ErrorContains(t, err, "OutputTokens:32768")
			assert.Equal(t, 4, strings.Count(err.Error(), "observed chat high-reasoning:"), "all four provider rejections remain in the returned diagnostics")
			require.Len(t, transport.requests, 4)
			require.Len(t, recorder.responses, 4)
			for index, request := range transport.requests {
				assertJudgmentHTTPRequest(t, request, 32768, "invoke-with-response-stream", candidate, claim)
				response := recorder.responses[index]
				require.NotNil(t, response)
				calls := response.ToolCalls()
				require.Len(t, calls, 1)
				assert.Equal(t, test.arguments, string(calls[0].Payload), "invalid arguments must not be repaired")
				assert.Equal(t, "max_tokens", response.StopReason)
				assert.True(t, response.OutputLimited)
				assert.Equal(t, 32768, response.Usage.OutputTokens)
			}
		})
	}
}

func TestJudgeAnthropicHTTPRejectsPartialJSONWithoutRepair(t *testing.T) {
	transport := &judgmentHTTPTransport{
		t:            t,
		arguments:    `{"complete":{"label":"entailed","rationale":"unfinished`,
		stopReason:   "max_tokens",
		outputTokens: 32768,
	}
	client, recorder := newJudgmentHTTPClient(t, transport)
	evaluator, err := judge.New(client, 32768)
	require.NoError(t, err)
	candidate, claim := "Reading: 12.5 °C.", "The report includes the reading."

	judgments, err := evaluator.Judge(t.Context(), candidate, []aieval.Claim{{ID: "complete", Text: claim}}, "")

	require.Error(t, err)
	assert.Nil(t, judgments)
	require.ErrorContains(t, err, "anthropic: accumulate streamed response")
	require.ErrorContains(t, err, "unexpected end of JSON input")
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, model.OutputValidationResponseShape, rejected.Kind())
	// The SDK rejects the incomplete argument at content_block_stop, before
	// reading message_delta. Preserve known input usage and leave the later
	// stop/output facts unknown; this failure does not authorize a correction.
	require.ErrorContains(t, err, "stop_reason=unknown output_limited=unknown")
	assert.NotContains(t, err.Error(), "recovery_cap")
	assert.Equal(t, &model.TokenUsage{Model: judgmentTransportModel, ModelClass: model.ModelClassHighReasoning, InputTokens: 123, TotalTokens: 123}, rejected.Usage())
	require.Len(t, transport.requests, 1)
	assertJudgmentHTTPRequest(t, transport.requests[0], 32768, "invoke-with-response-stream", candidate, claim)
	require.Len(t, recorder.responses, 1)
	assert.Nil(t, recorder.responses[0], "partial JSON must never become an assembled response")
}

func (r *judgmentResponseRecorder) Complete(ctx context.Context, request *model.Request) (*model.Response, error) {
	response, err := r.Provider.Complete(ctx, request)
	r.responses = append(r.responses, response)
	return response, err
}

func (r *judgmentHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	observed := judgmentHTTPRequest{path: request.URL.Path, raw: data}
	if err := json.Unmarshal(data, &observed.body); err != nil {
		return nil, err
	}
	arguments := r.arguments
	if len(r.argumentsByRequest) > 0 {
		if len(r.requests) >= len(r.argumentsByRequest) {
			return nil, fmt.Errorf("unexpected judgment request %d", len(r.requests)+1)
		}
		arguments = r.argumentsByRequest[len(r.requests)]
	}
	r.requests = append(r.requests, observed)
	name := observed.body.ToolChoice.Name
	contentType := "application/json"
	var body []byte
	if strings.HasSuffix(request.URL.Path, "/invoke-with-response-stream") {
		contentType = "application/vnd.amazon.eventstream"
		body = judgmentEventStream(r.t, name, arguments, r.stopReason, r.outputTokens)
	} else {
		body = []byte(fmt.Sprintf(`{"id":"msg_test","type":"message","role":"assistant","model":%q,"content":[{"type":"tool_use","id":"call_test","name":%q,"input":%s}],"stop_reason":%q,"usage":{"input_tokens":123,"output_tokens":%d}}`, judgmentTransportModel, name, arguments, r.stopReason, r.outputTokens))
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

// newJudgmentHTTPClient uses static synthetic credentials only to exercise the
// SDK's real signing and Bedrock encoding before the in-memory transport runs.
func newJudgmentHTTPClient(t *testing.T, transport *judgmentHTTPTransport) (model.Client, *judgmentResponseRecorder) {
	t.Helper()
	sdkClient := sdk.NewClient(sdkbedrock.WithConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("synthetic-access", "synthetic-secret", ""),
	}), option.WithHTTPClient(&http.Client{Transport: transport}))
	provider, err := anthropicprovider.NewProvider(&sdkClient.Messages, anthropicprovider.Options{
		DefaultModel:         judgmentTransportModel,
		HighModel:            judgmentTransportModel,
		ThinkingBudget:       8192,
		ToolExamplesInSchema: true,
	})
	require.NoError(t, err)
	recorder := &judgmentResponseRecorder{Provider: provider}
	client, err := model.NewClient(recorder)
	require.NoError(t, err)
	return client, recorder
}

// assertJudgmentHTTPRequest verifies the actual wire body retains complete
// evidence on initial and correction calls. Claim text appears exactly once,
// in its named schema property, not in a separate positional input list.
func assertJudgmentHTTPRequest(t *testing.T, request judgmentHTTPRequest, cap int, method, candidate, claim string) {
	t.Helper()
	const prompt = `Classify each claim independently against the supplied output.
Use the reference, when supplied, as factual context only. Do not credit the output with information that appears only in the reference.
Return entailed when the output establishes the claim, contradicted when it establishes the claim is false, not_addressed when it does neither, and indeterminate only when ambiguity prevents classification.
Apply each claim's conditions and requirements as written. For a constraint on content the output may omit, absence of that content satisfies the constraint; use entailed if the rest of the claim is satisfied, not not_addressed. This does not satisfy a requirement to include content, supply missing evidence for content actually included, or resolve an unknown condition about the world.
Call the supplied grading tool exactly once. Each required property describes one claim; supply its label and a concise rationale in that property.`
	require.NotEmpty(t, request.body.System)
	assert.Equal(t, "text", request.body.System[0].Type)
	assert.Equal(t, prompt, request.body.System[0].Text)
	assert.Equal(t, "/model/"+judgmentTransportModel+"/"+method, request.path)
	assert.Equal(t, cap, request.body.MaxTokens)
	assert.Empty(t, request.body.Thinking, "forced tool requests must not enable thinking")
	assert.Equal(t, "tool", request.body.ToolChoice.Type)
	require.Len(t, request.body.Tools, 1)
	assert.Equal(t, request.body.Tools[0].Name, request.body.ToolChoice.Name)
	assert.Contains(t, string(request.body.Tools[0].InputSchema), `"additionalProperties":false`)
	var userText []string
	for _, message := range request.body.Messages {
		if message.Role == "user" {
			for _, content := range message.Content {
				if content.Type == "text" {
					userText = append(userText, content.Text)
				}
			}
		}
	}
	require.Len(t, userText, 1)
	var evidence map[string]string
	require.NoError(t, json.Unmarshal([]byte(userText[0]), &evidence))
	assert.Equal(t, map[string]string{"output": candidate}, evidence)
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(request.body.Tools[0].InputSchema, &schema))
	assert.Equal(t, []string{"complete"}, schema.Required)
	require.Len(t, schema.Properties, 1)
	assert.Equal(t, claim, schema.Properties["complete"].Description)
	assert.NotContains(t, userText[0], "claim_id")
}

// judgmentEventStream encodes real AWS event-stream frames containing two tool
// argument fragments. The SDK must join those fragments without changing JSON.
func judgmentEventStream(t *testing.T, name, arguments, stopReason string, outputTokens int) []byte {
	t.Helper()
	var body bytes.Buffer
	for _, event := range []string{
		fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":123,"output_tokens":0}}}`, judgmentTransportModel),
		fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_test","name":%q,"input":{}}}`, name),
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, arguments[:len(arguments)/2]),
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, arguments[len(arguments)/2:]),
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, stopReason, outputTokens),
		`{"type":"message_stop"}`,
	} {
		payload, err := json.Marshal(struct {
			Bytes []byte `json:"bytes"`
		}{Bytes: []byte(event)})
		require.NoError(t, err)
		message := eventstream.Message{Payload: payload}
		message.Headers.Set(":message-type", eventstream.StringValue("event"))
		message.Headers.Set(":event-type", eventstream.StringValue("chunk"))
		require.NoError(t, eventstream.NewEncoder().Encode(&body, message))
	}
	return body.Bytes()
}
