// These tests exercise Bedrock provider boundaries with synthetic SDK responses.
// The per-client transport records requests and never delegates to a network;
// no developer credentials or global HTTP transport are used.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	sdkvertex "github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	anthropicprovider "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// forcedChoiceTransport supplies synthetic success or terminal error responses
	// so accepted requests prove SDK encoding without remote inference or retries.
	forcedChoiceTransport struct {
		mu       sync.Mutex
		requests []*http.Request
		bodies   [][]byte
		success  bool
	}

	// forcedChoiceCounter records the complete request delegated for counting.
	forcedChoiceCounter struct {
		requests []*model.Request
	}
)

const forcedChoiceCountOperation = "count"

func TestBedrockFableForcedToolRejectedBeforeDispatch(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, operation := range []string{"complete", "stream", forcedChoiceCountOperation} {
			for _, mode := range []model.ToolChoiceMode{model.ToolChoiceModeTool, model.ToolChoiceModeAny} {
				for _, selection := range []string{"explicit", "default", "high", "small"} {
					t.Run(forcedChoiceCaseName(native, operation, string(mode), selection), func(t *testing.T) {
						transport := &forcedChoiceTransport{}
						counter := &forcedChoiceCounter{}
						provider := forcedChoiceProvider(t, native, transport, counter, AnthropicOptions{
							DefaultModel: "us.anthropic.claude-fable-5-1",
							HighModel:    "us.anthropic.claude-fable-5-1",
							SmallModel:   "us.anthropic.claude-fable-5-1",
							MaxTokens:    2048,
						})
						request := forcedChoiceRequest(t, mode)
						switch selection {
						case "explicit":
							request.Model = "anthropic.claude-fable-5-1"
						case "default":
							request.ModelClass = model.ModelClassDefault
						case "high":
							request.ModelClass = model.ModelClassHighReasoning
							request.Thinking = &model.ThinkingOptions{Enable: true}
						case "small":
							request.ModelClass = model.ModelClassSmall
						}
						err := invokeForcedChoiceProvider(t, provider, operation, request)

						var local *model.RequestValidationError
						require.ErrorAs(t, err, &local)
						assert.Contains(t, local.Error(), "claude-fable-5-1")
						assert.Contains(t, local.Error(), string(mode))
						_, remote := model.AsProviderError(err)
						assert.False(t, remote)
						assert.Empty(t, transport.bodies)
						assert.Empty(t, counter.requests)
						assert.Equal(t, mode, request.ToolChoice.Mode)
						client, err := model.NewClient(provider)
						require.NoError(t, err)
						err = invokeForcedChoiceClient(t, client, operation, request)
						require.ErrorAs(t, err, &local)
						assert.Empty(t, transport.bodies)
						assert.Empty(t, counter.requests)
					})
				}
			}
		}
	}
}

func TestBedrockFableAllowedChoicesReturnUsage(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, mode := range []model.ToolChoiceMode{"", model.ToolChoiceModeAuto, model.ToolChoiceModeNone} {
			t.Run(forcedChoiceCaseName(native, string(mode)), func(t *testing.T) {
				transport := &forcedChoiceTransport{success: true}
				counter := &forcedChoiceCounter{}
				provider := forcedChoiceProvider(t, native, transport, counter, AnthropicOptions{
					DefaultModel: "us.anthropic.claude-fable-5-1",
					MaxTokens:    2048,
				})
				request := forcedChoiceRequest(t, mode)
				request.Thinking = &model.ThinkingOptions{Enable: true}
				if mode == model.ToolChoiceModeNone {
					request.Tools = nil
				}
				client, err := model.NewClient(provider)
				require.NoError(t, err)
				response, err := client.Complete(t.Context(), request)
				require.NoError(t, err)
				require.Len(t, response.Content, 1)
				assert.Equal(t, model.TextPart{Text: "done"}, response.Content[0].Parts[0])
				assert.Equal(t, 7, response.Usage.InputTokens)
				assert.Equal(t, 3, response.Usage.OutputTokens)
				assert.Equal(t, "us.anthropic.claude-fable-5-1", response.Usage.Model)
				count, err := client.CountTokens(t.Context(), request)
				require.NoError(t, err)
				assert.Equal(t, 7, count.InputTokens)
				assert.True(t, count.Exact)
				if native {
					assert.Len(t, transport.bodies, 1)
					assert.Len(t, counter.requests, 1)
				} else {
					assert.Len(t, transport.bodies, 2)
					assert.Empty(t, counter.requests)
				}
			})
		}
	}
}

func TestBedrockForcedToolPreservesAcceptedRequests(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		mode          model.ToolChoiceMode
		explicitModel string
	}{
		{name: "fable nil choice", model: "us.anthropic.claude-fable-5-1"},
		{name: "fable auto", model: "us.anthropic.claude-fable-5-1", mode: model.ToolChoiceModeAuto},
		{name: "fable none without tools", model: "us.anthropic.claude-fable-5-1", mode: model.ToolChoiceModeNone},
		{name: "fable none with tools", model: "us.anthropic.claude-fable-5-1", mode: model.ToolChoiceModeNone},
		{name: "fable generation only", model: "us.anthropic.claude-fable-5", mode: model.ToolChoiceModeTool},
		{name: "fable next minor", model: "us.anthropic.claude-fable-5-2", mode: model.ToolChoiceModeAny},
		{name: "fable double digit minor", model: "us.anthropic.claude-fable-5-10", mode: model.ToolChoiceModeTool},
		{name: "fable next generation", model: "us.anthropic.claude-fable-6-1", mode: model.ToolChoiceModeAny},
		{name: "opus", model: "us.anthropic.claude-opus-5", mode: model.ToolChoiceModeTool},
		{name: "sonnet", model: "us.anthropic.claude-sonnet-4-6", mode: model.ToolChoiceModeAny},
		{name: "explicit wins over missing class", model: "us.anthropic.claude-fable-5-1", mode: model.ToolChoiceModeTool, explicitModel: "us.anthropic.claude-sonnet-4-6"},
	}
	for _, native := range []bool{true, false} {
		for _, operation := range []string{"complete", "stream", forcedChoiceCountOperation} {
			for _, test := range tests {
				t.Run(forcedChoiceCaseName(native, operation, test.name), func(t *testing.T) {
					transport := &forcedChoiceTransport{}
					counter := &forcedChoiceCounter{}
					provider := forcedChoiceProvider(t, native, transport, counter, AnthropicOptions{
						DefaultModel: test.model,
						MaxTokens:    2048,
					})
					request := forcedChoiceRequest(t, test.mode)
					selected := test.model
					if test.explicitModel != "" {
						request.Model = test.explicitModel
						request.ModelClass = model.ModelClassHighReasoning
						selected = test.explicitModel
					}
					request.Thinking = &model.ThinkingOptions{Enable: true}
					request.Cache = &model.CacheOptions{AfterTools: true}
					if test.name == "fable none without tools" {
						request.Tools = nil
						request.Cache = nil
					}
					err := invokeForcedChoiceProvider(t, provider, operation, request)
					if !native && test.name == "fable none with tools" {
						require.EqualError(t, err, `bedrock: tool choice mode "none" is unsupported when tools are defined`)
						var local *model.RequestValidationError
						assert.NotErrorAs(t, err, &local)
						assert.Empty(t, transport.bodies)
						assert.Empty(t, counter.requests)
						return
					}
					if operation == forcedChoiceCountOperation && (native || test.name == "opus") {
						require.NoError(t, err)
						require.Len(t, counter.requests, 1)
						counted := counter.requests[0]
						assert.Equal(t, strings.TrimPrefix(selected, "us."), counted.Model)
						assert.Equal(t, request.ModelClass, counted.ModelClass)
						assert.Equal(t, request.Messages, counted.Messages)
						assert.Equal(t, request.Tools, counted.Tools)
						assert.Equal(t, request.ToolChoice, counted.ToolChoice)
						assert.Equal(t, request.Thinking, counted.Thinking)
						assert.Equal(t, request.Cache, counted.Cache)
						assert.Equal(t, request.MaxTokens, counted.MaxTokens)
						assert.Empty(t, transport.bodies)
						return
					}
					var local *model.RequestValidationError
					assert.NotErrorAs(t, err, &local)
					providerErr, ok := model.AsProviderError(err)
					require.True(t, ok)
					assert.Equal(t, http.StatusForbidden, providerErr.HTTPStatus())
					if native {
						assert.Empty(t, providerErr.RequestID())
						var sdkError *sdk.Error
						require.ErrorAs(t, err, &sdkError)
						assert.Equal(t, "synthetic-request", sdkError.RequestID)
					} else {
						assert.Equal(t, "synthetic-request", providerErr.RequestID())
					}
					assert.False(t, providerErr.Retryable())
					assert.Empty(t, counter.requests)
					require.Len(t, transport.bodies, 1)
					requestBody := string(transport.bodies[0])
					assert.Contains(t, transport.requests[0].URL.Path, strings.TrimPrefix(selected, "us."))
					if operation != forcedChoiceCountOperation {
						assert.Contains(t, requestBody, "2048")
					}
					if test.mode != model.ToolChoiceModeNone {
						assert.Contains(t, requestBody, "synthetic_submit")
						assert.Contains(t, requestBody, "cache")
					}
					if native {
						var body map[string]any
						require.NoError(t, json.Unmarshal(transport.bodies[0], &body))
						if test.mode == "" || test.mode == model.ToolChoiceModeAuto {
							assert.NotContains(t, body, "tool_choice")
						} else {
							choice, ok := body["tool_choice"].(map[string]any)
							require.True(t, ok)
							assert.Equal(t, string(test.mode), choice["type"])
						}
						assert.Contains(t, requestBody, `"adaptive"`)
						assert.Equal(t, "bedrock-2023-05-31", body["anthropic_version"])
						assert.NotContains(t, body, "output_config")
					}
				})
			}
		}
	}
}

func TestBedrockForcedToolKeepsOtherClaudeHosts(t *testing.T) {
	for _, vertex := range []bool{false, true} {
		for _, operation := range []string{"complete", "stream", forcedChoiceCountOperation} {
			for _, mode := range []model.ToolChoiceMode{model.ToolChoiceModeTool, model.ToolChoiceModeAny} {
				host := "anthropic"
				if vertex {
					host = "vertex"
				}
				t.Run(host+"/"+operation+"/"+string(mode), func(t *testing.T) {
					transport := &forcedChoiceTransport{}
					httpClient := &http.Client{Transport: transport}
					options := []option.RequestOption{
						option.WithoutEnvironmentDefaults(),
						option.WithAPIKey("synthetic"),
						option.WithHTTPClient(httpClient),
					}
					if vertex {
						credentials := &google.Credentials{
							TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "synthetic"}),
						}
						options = []option.RequestOption{
							sdkvertex.WithCredentials(t.Context(), "global", "synthetic-project", credentials),
							option.WithHTTPClient(httpClient),
						}
					}
					client := sdk.NewClient(options...)
					provider, err := anthropicprovider.NewProvider(&client.Messages, AnthropicOptions{
						DefaultModel: "claude-fable-5-1",
						MaxTokens:    2048,
					})
					require.NoError(t, err)
					err = invokeForcedChoiceProvider(t, provider, operation, forcedChoiceRequest(t, mode))
					var local *model.RequestValidationError
					assert.NotErrorAs(t, err, &local)
					providerErr, ok := model.AsProviderError(err)
					require.True(t, ok, "%v", err)
					assert.Equal(t, http.StatusForbidden, providerErr.HTTPStatus())
					require.Len(t, transport.bodies, 1)
					assert.Contains(t, string(transport.bodies[0]), `"type":"`+string(mode)+`"`)
					if vertex {
						assert.Contains(t, transport.requests[0].URL.Host, "aiplatform")
					} else {
						assert.Equal(t, "api.anthropic.com", transport.requests[0].URL.Host)
					}
				})
			}
		}
	}
}

func TestBedrockForcedToolPreservesSelectionAndPriorErrors(t *testing.T) {
	tests := []struct {
		name       string
		class      model.ModelClass
		model      string
		noMessages bool
		want       string
	}{
		{name: "missing high", class: model.ModelClassHighReasoning, want: "high-reasoning model class requested but HighModel is not configured"},
		{name: "missing small", class: model.ModelClassSmall, want: "small model class requested but SmallModel is not configured"},
		{name: "unknown class", class: "unknown", want: `model request identity: model: token usage has unsupported model class "unknown"`},
		{name: "preview", model: "us.anthropic.claude-mythos-preview-v1:0", want: `model "us.anthropic.claude-mythos-preview-v1:0" does not support forced tool choice mode "tool"`},
		{name: "messages first", class: model.ModelClassHighReasoning, noMessages: true, want: "messages are required"},
	}
	for _, native := range []bool{true, false} {
		for _, operation := range []string{"complete", "stream", forcedChoiceCountOperation} {
			for _, test := range tests {
				t.Run(forcedChoiceCaseName(native, operation, test.name), func(t *testing.T) {
					transport := &forcedChoiceTransport{}
					counter := &forcedChoiceCounter{}
					provider := forcedChoiceProvider(t, native, transport, counter, AnthropicOptions{
						DefaultModel: "us.anthropic.claude-fable-5-1",
						MaxTokens:    2048,
					})
					request := forcedChoiceRequest(t, model.ToolChoiceModeTool)
					request.Model, request.ModelClass = test.model, test.class
					if test.noMessages {
						request.Messages = nil
					}
					err := invokeForcedChoiceProvider(t, provider, operation, request)
					if native && operation == forcedChoiceCountOperation && test.name == "preview" {
						require.NoError(t, err)
						require.Len(t, counter.requests, 1)
						assert.Equal(t, "anthropic.claude-mythos-preview-v1:0", counter.requests[0].Model)
						assert.Empty(t, transport.bodies)
						return
					}
					want := test.want
					if test.name != "unknown class" {
						prefix := "bedrock: "
						if native && operation != forcedChoiceCountOperation {
							prefix = "anthropic: "
						}
						if test.noMessages && native && operation == forcedChoiceCountOperation {
							want = "high-reasoning model class requested but HighModel is not configured"
						}
						want = prefix + want
					}
					require.EqualError(t, err, want)
					var local *model.RequestValidationError
					assert.NotErrorAs(t, err, &local)
					assert.Empty(t, transport.bodies)
					assert.Empty(t, counter.requests)
				})
			}
		}
	}
}

func TestBedrockForcedToolPreservesCountingAndEstimateInputs(t *testing.T) {
	for _, mode := range []model.ToolChoiceMode{"", model.ToolChoiceModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			counter := &forcedChoiceCounter{}
			provider := forcedChoiceProvider(t, true, &forcedChoiceTransport{}, counter, AnthropicOptions{
				DefaultModel: "us.anthropic.claude-fable-5-1",
				MaxTokens:    2048,
			})
			request := &model.Request{MaxTokens: 0}
			if mode != "" {
				request.ToolChoice = &model.ToolChoice{Mode: mode}
			}
			before, err := (model.TokenEstimator{}).CountTokens(t.Context(), request)
			require.NoError(t, err)
			count, err := provider.(model.TokenCounter).CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.True(t, count.Exact)
			require.Len(t, counter.requests, 1)
			assert.Empty(t, counter.requests[0].Messages)
			assert.Zero(t, counter.requests[0].MaxTokens)
			assert.Equal(t, request.ToolChoice, counter.requests[0].ToolChoice)
			after, err := (model.TokenEstimator{}).CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.False(t, after.Exact)
		})
	}
}

func TestBedrockForcedToolKeepsNativeJSONUnsupported(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, operation := range []string{"complete", "stream", forcedChoiceCountOperation} {
			t.Run(forcedChoiceCaseName(native, operation), func(t *testing.T) {
				transport := &forcedChoiceTransport{}
				counter := &forcedChoiceCounter{}
				provider := forcedChoiceProvider(t, native, transport, counter, AnthropicOptions{
					DefaultModel: "us.anthropic.claude-fable-5-1",
					MaxTokens:    2048,
				})
				request := forcedChoiceRequest(t, "")
				request.Tools = nil
				request.StructuredOutput = &model.StructuredOutput{
					Name: "result", Schema: rawjson.Message(`{"type":"object"}`),
				}
				err := invokeForcedChoiceProvider(t, provider, operation, request)
				require.ErrorIs(t, err, model.ErrStructuredOutputUnsupported)
				var local *model.RequestValidationError
				assert.NotErrorAs(t, err, &local)
				assert.Empty(t, transport.bodies)
				assert.Empty(t, counter.requests)
			})
		}
	}
}

// RoundTrip records the encoded SDK request and supplies a synthetic response.
// There is no underlying transport, socket, or remote credential lookup.
func (r *forcedChoiceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if err := request.Body.Close(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.requests = append(r.requests, request.Clone(request.Context()))
	r.bodies = append(r.bodies, bytes.Clone(body))
	r.mu.Unlock()
	responseBody := `{"type":"error","error":{"type":"permission_error","message":"synthetic refusal"},"message":"synthetic refusal"}`
	responseStatus := http.StatusForbidden
	if r.success {
		responseStatus = http.StatusOK
		switch {
		case strings.HasSuffix(request.URL.Path, "/count-tokens"):
			responseBody = `{"inputTokens":7}`
		case strings.HasSuffix(request.URL.Path, "/converse"):
			responseBody = `{"output":{"message":{"role":"assistant","content":[{"text":"done"}]}},"stopReason":"end_turn","usage":{"inputTokens":7,"outputTokens":3,"totalTokens":10}}`
		default:
			responseBody = `{"id":"synthetic","type":"message","role":"assistant","model":"us.anthropic.claude-fable-5-1","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`
		}
	}
	return &http.Response{
		StatusCode: responseStatus,
		Header: http.Header{
			"Content-Type":     {"application/json"},
			"X-Amzn-Requestid": {"synthetic-request"},
			"Request-Id":       {"synthetic-request"},
			"X-Amzn-Errortype": {"AccessDeniedException"},
		},
		Body:    io.NopCloser(strings.NewReader(responseBody)),
		Request: request,
	}, nil
}

// CountTokens keeps the full input visible and supplies synthetic exact usage.
func (c *forcedChoiceCounter) CountTokens(_ context.Context, request *model.Request) (model.TokenCount, error) {
	copied := *request
	c.requests = append(c.requests, &copied)
	return model.TokenCount{Model: request.Model, ModelClass: request.ModelClass, InputTokens: 7, Exact: true}, nil
}

func forcedChoiceProvider(t *testing.T, native bool, transport *forcedChoiceTransport, counter *forcedChoiceCounter, options AnthropicOptions) model.Provider {
	t.Helper()
	client := &http.Client{Transport: transport}
	config := aws.Config{
		Region:      "us-west-2",
		Credentials: testCredentialsProvider{},
		HTTPClient:  client,
	}
	if native {
		provider, err := NewAnthropicProvider(config, counter, options, option.WithHTTPClient(client))
		require.NoError(t, err)
		return provider
	}
	runtime := bedrockruntime.NewFromConfig(config)
	provider, err := NewProvider(runtime, Options{
		DefaultModel:       options.DefaultModel,
		HighModel:          options.HighModel,
		SmallModel:         options.SmallModel,
		MaxTokens:          options.MaxTokens,
		MantleTokenCounter: counter,
	})
	require.NoError(t, err)
	return provider
}

func forcedChoiceRequest(t *testing.T, mode model.ToolChoiceMode) *model.Request {
	t.Helper()
	input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object","additionalProperties":false}`))
	require.NoError(t, err)
	request := &model.Request{
		MaxTokens: 2048,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Report completion."}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "synthetic.submit",
			Description: "Report completion.",
			Input:       input,
		}},
	}
	if mode != "" {
		request.ToolChoice = &model.ToolChoice{Mode: mode}
		if mode == model.ToolChoiceModeTool {
			request.ToolChoice.Name = "synthetic.submit"
		}
	}
	return request
}

func invokeForcedChoiceProvider(t *testing.T, provider model.Provider, operation string, request *model.Request) error {
	t.Helper()
	switch operation {
	case "complete":
		_, err := provider.Complete(t.Context(), request)
		return err
	case "stream":
		stream, err := provider.Stream(t.Context(), request)
		if stream != nil {
			require.NoError(t, stream.Close())
		}
		return err
	case forcedChoiceCountOperation:
		counter, ok := provider.(model.TokenCounter)
		require.True(t, ok)
		_, err := counter.CountTokens(t.Context(), request)
		return err
	default:
		t.Fatalf("unknown test operation %q", operation)
		return nil
	}
}

func invokeForcedChoiceClient(t *testing.T, client model.Client, operation string, request *model.Request) error {
	t.Helper()
	switch operation {
	case "complete":
		_, err := client.Complete(t.Context(), request)
		return err
	case "stream":
		stream, err := client.Stream(t.Context(), request)
		if stream != nil {
			require.NoError(t, stream.Close())
		}
		return err
	case forcedChoiceCountOperation:
		_, err := client.CountTokens(t.Context(), request)
		return err
	default:
		t.Fatalf("unknown test operation %q", operation)
		return nil
	}
}

func forcedChoiceCaseName(native bool, parts ...string) string {
	protocol := "converse"
	if native {
		protocol = "native"
	}
	return protocol + "/" + strings.Join(parts, "/")
}
