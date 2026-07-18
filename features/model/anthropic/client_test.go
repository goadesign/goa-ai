package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// stubMessagesClient records the last request per Messages endpoint and
	// replays canned responses.
	stubMessagesClient struct {
		lastParams      sdk.MessageNewParams
		lastCountParams sdk.MessageCountTokensParams
		resp            *sdk.Message
		countResp       *sdk.MessageTokensCount
		err             error
		countErr        error

		stream *ssestream.Stream[sdk.MessageStreamEventUnion]
	}

	// noopDecoder terminates a stream immediately for streaming stubs.
	noopDecoder struct{}

	// roundTripFunc adapts a function to http.RoundTripper so tests can
	// observe the wire request a real SDK client produces.
	roundTripFunc func(*http.Request) (*http.Response, error)
)

func TestTranslateResponsePreservesThinkingInOneAssistantMessage(t *testing.T) {
	resp, err := translateResponse(&sdk.Message{
		StopReason: sdk.StopReasonToolUse,
		Content: []sdk.ContentBlockUnion{
			{Type: "thinking", Thinking: "reasoning", Signature: "sig"},
			{Type: "text", Text: "answer"},
			{Type: "tool_use", ID: "call-1", Name: "lookup", Input: json.RawMessage(`{"id":"a"}`)},
		},
	}, map[string]string{"lookup": "svc.lookup"})

	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	require.Equal(t, []model.Part{
		model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
		model.TextPart{Text: "answer"},
		model.ToolUsePart{ID: "call-1", Name: "svc.lookup", Input: rawjson.Message(`{"id":"a"}`)},
	}, resp.Content[0].Parts)
}

func TestTranslateResponsePreservesTextCitations(t *testing.T) {
	resp, err := translateResponse(&sdk.Message{
		StopReason: sdk.StopReasonEndTurn,
		Content: []sdk.ContentBlockUnion{{
			Type: "text",
			Text: "supported answer",
			Citations: []sdk.TextCitationUnion{{
				Type:           "char_location",
				CitedText:      "source excerpt",
				DocumentIndex:  2,
				DocumentTitle:  "Manual",
				FileID:         "file-1",
				StartCharIndex: 10,
				EndCharIndex:   20,
			}},
		}},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, []model.Part{model.CitationsPart{
		Text: "supported answer",
		Citations: []model.Citation{{
			Title:         "Manual",
			Source:        "file-1",
			SourceContent: []string{"source excerpt"},
			Location: model.CitationLocation{
				DocumentChar: &model.DocumentCharLocation{
					DocumentIndex: 2,
					Start:         10,
					End:           20,
				},
			},
		}},
	}}, resp.Content[0].Parts)
}

func TestTranslateResponsePreservesRedactedThinking(t *testing.T) {
	resp, err := translateResponse(&sdk.Message{
		StopReason: sdk.StopReasonEndTurn,
		Content: []sdk.ContentBlockUnion{{
			Type: "redacted_thinking",
			Data: "opaque",
		}},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, []model.Part{
		model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
	}, resp.Content[0].Parts)
}

func (s *stubMessagesClient) New(_ context.Context, body sdk.MessageNewParams, _ ...option.RequestOption) (*sdk.Message, error) {
	s.lastParams = body
	return s.resp, s.err
}

func (s *stubMessagesClient) NewStreaming(_ context.Context, body sdk.MessageNewParams, _ ...option.RequestOption) *ssestream.Stream[sdk.MessageStreamEventUnion] {
	s.lastParams = body
	if s.stream == nil {
		dec := &noopDecoder{}
		s.stream = ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)
	}
	return s.stream
}

func (s *stubMessagesClient) CountTokens(_ context.Context, body sdk.MessageCountTokensParams, _ ...option.RequestOption) (*sdk.MessageTokensCount, error) {
	s.lastCountParams = body
	return s.countResp, s.countErr
}

func (n *noopDecoder) Event() ssestream.Event { return ssestream.Event{} }
func (n *noopDecoder) Next() bool             { return false }
func (n *noopDecoder) Close() error           { return nil }
func (n *noopDecoder) Err() error             { return nil }

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestComplete_TextOnly(t *testing.T) {
	stub := &stubMessagesClient{}
	cl, err := New(stub, Options{
		DefaultModel: "claude-3.5-sonnet",
		MaxTokens:    128,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
	}

	stub.resp = &sdk.Message{
		Content: []sdk.ContentBlockUnion{
			{
				Type: "text",
				Text: "world",
			},
		},
		StopReason: sdk.StopReasonEndTurn,
		Usage: sdk.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}

	resp, err := cl.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("expected 1 content message, got %d", len(resp.Content))
	}
	if got := resp.Content[0].Parts[0].(model.TextPart).Text; got != "world" {
		t.Fatalf("unexpected text %q", got)
	}
	if resp.StopReason != string(sdk.StopReasonEndTurn) {
		t.Fatalf("unexpected stop reason %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestCountTokensUsesCanonicalAnthropicRequest(t *testing.T) {
	stub := &stubMessagesClient{
		countResp: &sdk.MessageTokensCount{InputTokens: 42},
	}
	client, err := New(stub, Options{
		DefaultModel: "anthropic.claude-sonnet-5",
		MaxTokens:    128,
	})
	require.NoError(t, err)

	count, err := client.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleSystem,
				Parts: []model.Part{model.TextPart{Text: "system"}},
			},
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
					model.TextPart{Text: "answer"},
				},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "lookup",
			Description: "Look up a value.",
			Input: model.ToolInputFromSchema(rawjson.Message(
				`{"type":"object","properties":{"id":{"type":"string"}}}`,
			)),
		}},
		Cache: &model.CacheOptions{
			AfterSystem: true,
			AfterTools:  true,
		},
	})

	require.NoError(t, err)
	assert.Equal(t, model.TokenCount{
		Model:       "anthropic.claude-sonnet-5",
		ModelClass:  model.ModelClassDefault,
		InputTokens: 42,
		Exact:       true,
	}, count)
	assert.Equal(t, sdk.Model("anthropic.claude-sonnet-5"), stub.lastCountParams.Model)
	require.Len(t, stub.lastCountParams.Messages, 2)
	require.Len(t, stub.lastCountParams.Messages[1].Content, 1)
	assert.NotNil(t, stub.lastCountParams.Messages[1].Content[0].OfText)
	require.Len(t, stub.lastCountParams.System.OfTextBlockArray, 1)
	assert.Equal(t, "ephemeral", string(stub.lastCountParams.System.OfTextBlockArray[0].CacheControl.Type))
	require.Len(t, stub.lastCountParams.Tools, 1)
	assert.Equal(t, "ephemeral", string(stub.lastCountParams.Tools[0].OfTool.CacheControl.Type))
}

// Counting is exempt from completion policy: a client that relies on
// per-request MaxTokens for completions (Options.MaxTokens unset) must still
// count, because the count API carries no max_tokens at all.
func TestCountTokensWithoutDefaultMaxTokens(t *testing.T) {
	stub := &stubMessagesClient{
		countResp: &sdk.MessageTokensCount{InputTokens: 7},
	}
	client, err := New(stub, Options{DefaultModel: "anthropic.claude-sonnet-5"})
	require.NoError(t, err)

	count, err := client.CountTokens(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, 7, count.InputTokens)
	assert.True(t, count.Exact)
}

// A tool-less count request must omit the tools field entirely, exactly as
// the equivalent completion does — gateways may treat "tools": [] and an
// absent field differently.
func TestCountTokensOmitsEmptyTools(t *testing.T) {
	stub := &stubMessagesClient{
		countResp: &sdk.MessageTokensCount{InputTokens: 7},
	}
	client, err := New(stub, Options{DefaultModel: "anthropic.claude-sonnet-5"})
	require.NoError(t, err)

	_, err = client.CountTokens(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
	})

	require.NoError(t, err)
	assert.Nil(t, stub.lastCountParams.Tools)
}

func TestCountTokensEnablesToolExamplesBeta(t *testing.T) {
	var (
		beta string
		body []byte
	)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		beta = req.Header.Get("anthropic-beta")
		var err error
		body, err = io.ReadAll(req.Body)
		require.NoError(t, err)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		}, nil
	})}
	sdkClient := sdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL("https://anthropic.test"),
		option.WithHTTPClient(httpClient),
	)
	client, err := New(&sdkClient.Messages, Options{
		DefaultModel: "anthropic.claude-sonnet-5",
		MaxTokens:    128,
	})
	require.NoError(t, err)

	_, err = client.CountTokens(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "reports.concurrent",
			Description: "Check concurrent reports",
			Input: model.ToolInputFromSpec(tools.TypeSpec{
				Name:                     "ReportsConcurrentPayload",
				Schema:                   tools.RawJSON(`{"type":"object","properties":{"minimum":{"type":"integer","const":9007199254740993},"ratios":{"type":"array","items":{"type":"number"}}},"required":["minimum","ratios"],"example":{"minimum":9007199254740993,"ratios":[0.25,2]}}`),
				SchemaWithoutRootExample: tools.RawJSON(`{"type":"object","properties":{"minimum":{"type":"integer","const":9007199254740993},"ratios":{"type":"array","items":{"type":"number"}}},"required":["minimum","ratios"]}`),
				ExampleJSON:              tools.RawJSON(`{"minimum":9007199254740993,"ratios":[0.25,2]}`),
			}),
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, "tool-examples-2025-10-29", beta)
	var request struct {
		Tools []struct {
			InputSchema   json.RawMessage   `json:"input_schema"`
			InputExamples []json.RawMessage `json:"input_examples"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(body, &request))
	require.Len(t, request.Tools, 1)
	require.Len(t, request.Tools[0].InputExamples, 1)
	require.JSONEq(t,
		`{"type":"object","properties":{"minimum":{"type":"integer","const":9007199254740993},"ratios":{"type":"array","items":{"type":"number"}}},"required":["minimum","ratios"]}`,
		string(request.Tools[0].InputSchema),
	)
	require.JSONEq(t,
		`{"minimum":9007199254740993,"ratios":[0.25,2]}`,
		string(request.Tools[0].InputExamples[0]),
	)
}

// The beta must be opt-in per request: gateways that proxy the Messages API
// (Bedrock Mantle) reject beta identifiers they do not recognize, so a tool set
// carrying no authored examples must go out with no beta header at all.
func TestToolExamplesBetaOmittedWithoutAuthoredExamples(t *testing.T) {
	cases := []struct {
		name  string
		tools []*model.ToolDefinition
	}{
		{
			name:  "no tools",
			tools: nil,
		},
		{
			name: "tool without examples",
			tools: []*model.ToolDefinition{{
				Name:        "reports.complete",
				Description: "Complete a report",
				Input: model.ToolInputFromSchema(rawjson.Message(
					`{"type":"object","properties":{"id":{"type":"string"}}}`,
				)),
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var headers http.Header
			httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				headers = req.Header
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"input_tokens":7}`)),
				}, nil
			})}
			sdkClient := sdk.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL("https://anthropic.test"),
				option.WithHTTPClient(httpClient),
			)
			client, err := New(&sdkClient.Messages, Options{
				DefaultModel: "anthropic.claude-sonnet-5",
				MaxTokens:    128,
			})
			require.NoError(t, err)

			_, err = client.CountTokens(context.Background(), &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
				Tools: tc.tools,
			})

			require.NoError(t, err)
			assert.Empty(t, headers.Values("anthropic-beta"))
		})
	}
}

func TestComplete_ToolUse(t *testing.T) {
	stub := &stubMessagesClient{}
	cl, err := New(stub, Options{
		DefaultModel: "claude-3.5-sonnet",
		MaxTokens:    128,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "call tool"},
				},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "test.tool",
				Description: "test tool",
				Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
			},
		},
	}

	tools, canon, prov, err := encodeTools(context.Background(), req.Tools, false)
	if err != nil {
		t.Fatalf("encodeTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 encoded tool, got %d", len(tools))
	}
	if len(canon) != 1 || len(prov) != 1 {
		t.Fatalf("expected name maps, got canon=%v prov=%v", canon, prov)
	}

	sanitized := canon["test.tool"]
	if sanitized == "" {
		t.Fatalf("sanitizeToolName returned empty")
	}

	stub.resp = &sdk.Message{
		Content: []sdk.ContentBlockUnion{
			{
				Type:  "tool_use",
				Name:  sanitized,
				ID:    "tool-1",
				Input: json.RawMessage(`{"x":1}`),
			},
		},
		StopReason: sdk.StopReasonToolUse,
	}

	resp, err := cl.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls()))
	}
	call := resp.ToolCalls()[0]
	if string(call.Name) != "test.tool" {
		t.Fatalf("unexpected tool name %q", call.Name)
	}
	if call.ID != "tool-1" {
		t.Fatalf("unexpected tool ID %q", call.ID)
	}
	if string(call.Payload) != `{"x":1}` {
		t.Fatalf("unexpected payload %s", string(call.Payload))
	}
}

func TestPrepareRequestForcedToolDisablesThinking(t *testing.T) {
	cl := &Client{
		defaultModel: "claude-opus-4-7",
		maxTok:       4096,
		think:        2048,
	}

	req := &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "finish the task"},
			},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.complete",
			Description: "complete the task",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "tasks.progress.complete",
		},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			BudgetTokens: 2048,
		},
	}
	params := completionParamsFor(t, cl, req)
	if params.Thinking.OfEnabled != nil {
		t.Fatalf("forced tool choice must not send thinking config")
	}
	if params.ToolChoice.OfTool == nil {
		t.Fatalf("expected forced tool choice to survive")
	}
}

func TestPrepareRequestAnyToolDisablesThinking(t *testing.T) {
	cl := &Client{
		defaultModel: "claude-opus-4-7",
		maxTok:       4096,
		think:        2048,
	}

	req := &model.Request{
		Messages: []*model.Message{{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "continue through tools"},
			},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.update",
			Description: "update task progress",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeAny,
		},
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			BudgetTokens: 2048,
		},
	}
	params := completionParamsFor(t, cl, req)
	if params.Thinking.OfEnabled != nil {
		t.Fatalf("any tool choice must not send thinking config")
	}
	if params.ToolChoice.OfAny == nil {
		t.Fatalf("expected any tool choice to survive")
	}
}

// completionParamsFor runs the full encode + completion-policy pipeline the
// way Complete and Stream do, failing the test on any error.
func completionParamsFor(t *testing.T, cl *Client, req *model.Request) *sdk.MessageNewParams {
	t.Helper()
	enc, err := cl.encodeRequest(context.Background(), req)
	require.NoError(t, err)
	params, err := cl.completionParams(context.Background(), req, enc)
	require.NoError(t, err)
	return params
}

func TestComplete_RateLimited(t *testing.T) {
	stub := &stubMessagesClient{
		err: model.ErrRateLimited,
	}
	cl, err := New(stub, Options{
		DefaultModel: "claude-3.5-sonnet",
		MaxTokens:    64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hi"},
				},
			},
		},
	}

	_, err = cl.Complete(context.Background(), req)
	if !errors.Is(err, model.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

// TestComplete_RealSDK429Classified verifies that a genuine Anthropic SDK
// error (*sdk.Error, always returned as a pointer) with StatusCode 429 is
// classified as rate-limited. Before this decorator-free classification
// existed, isRateLimited only matched errors that already wrapped
// model.ErrRateLimited, so a real SDK 429 was never detected.
func TestComplete_RealSDK429Classified(t *testing.T) {
	stub := &stubMessagesClient{
		err: &sdk.Error{StatusCode: http.StatusTooManyRequests},
	}
	cl, err := New(stub, Options{DefaultModel: "claude-3.5-sonnet", MaxTokens: 64})
	require.NoError(t, err)

	req := &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
	}

	_, err = cl.Complete(context.Background(), req)
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
	assert.Equal(t, "anthropic", pe.Provider())
	assert.True(t, pe.Retryable())
}

// TestStream_EstablishmentErrorClassified verifies that a stream
// establishment failure (surfaced by ssestream.Stream.Err() immediately
// after NewStreaming) is classified via the same status-to-kind table as
// Complete, not just left as an opaque wrapped error.
func TestStream_EstablishmentErrorClassified(t *testing.T) {
	stub := &stubMessagesClient{
		stream: ssestream.NewStream[sdk.MessageStreamEventUnion](
			&noopDecoder{},
			&sdk.Error{StatusCode: http.StatusInternalServerError},
		),
	}
	cl, err := New(stub, Options{DefaultModel: "claude-3.5-sonnet", MaxTokens: 64})
	require.NoError(t, err)

	req := &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
	}

	streamer, err := cl.Stream(context.Background(), req)
	require.Error(t, err)
	assert.Nil(t, streamer)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
	assert.True(t, pe.Retryable())
}

// TestComplete_ContextCancelPassthrough verifies that a context-cancellation
// error surfaced by the SDK call passes through unclassified: cancellation
// is consumer-side flow control, not a provider failure.
func TestComplete_ContextCancelPassthrough(t *testing.T) {
	cause := fmt.Errorf("rpc: %w", context.Canceled)
	stub := &stubMessagesClient{err: cause}
	cl, err := New(stub, Options{DefaultModel: "claude-3.5-sonnet", MaxTokens: 64})
	require.NoError(t, err)

	req := &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
	}

	_, err = cl.Complete(context.Background(), req)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, cause, err) // returned unwrapped, exactly as surfaced
	_, ok := model.AsProviderError(err)
	assert.False(t, ok)
}

func TestComplete_RejectsStructuredOutput(t *testing.T) {
	stub := &stubMessagesClient{}
	cl, err := New(stub, Options{
		DefaultModel: "claude-3.5-sonnet",
		MaxTokens:    64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hi"}},
			},
		},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: tools.RawJSON(`{"type":"object"}`),
		},
	})
	if !errors.Is(err, model.ErrStructuredOutputUnsupported) {
		t.Fatalf("expected ErrStructuredOutputUnsupported, got %v", err)
	}
}

func TestStream_RejectsStructuredOutput(t *testing.T) {
	stub := &stubMessagesClient{}
	cl, err := New(stub, Options{
		DefaultModel: "claude-3.5-sonnet",
		MaxTokens:    64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hi"}},
			},
		},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: tools.RawJSON(`{"type":"object"}`),
		},
	})
	if !errors.Is(err, model.ErrStructuredOutputUnsupported) {
		t.Fatalf("expected ErrStructuredOutputUnsupported, got %v", err)
	}
}
