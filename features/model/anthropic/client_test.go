package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

type stubMessagesClient struct {
	lastParams sdk.MessageNewParams
	resp       *sdk.Message
	err        error

	stream *ssestream.Stream[sdk.MessageStreamEventUnion]
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

type noopDecoder struct{}

func (n *noopDecoder) Event() ssestream.Event { return ssestream.Event{} }
func (n *noopDecoder) Next() bool             { return false }
func (n *noopDecoder) Close() error           { return nil }
func (n *noopDecoder) Err() error             { return nil }

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

	tools, canon, prov, err := encodeTools(context.Background(), req.Tools)
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
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
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

	params, _, err := cl.prepareRequest(context.Background(), &model.Request{
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
	})
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
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

	params, _, err := cl.prepareRequest(context.Background(), &model.Request{
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
	})
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	if params.Thinking.OfEnabled != nil {
		t.Fatalf("any tool choice must not send thinking config")
	}
	if params.ToolChoice.OfAny == nil {
		t.Fatalf("expected any tool choice to survive")
	}
}

func TestEncodeTools_UsesSchemaWithoutRootExampleAndInputExamples(t *testing.T) {
	defs := []*model.ToolDefinition{{
		Name:        "reports.complete",
		Description: "Complete a report",
		Input:       model.ToolInputFromSpec(toolInputExampleSpec()),
	}}

	tools, _, _, err := encodeTools(context.Background(), defs)
	if err != nil {
		t.Fatalf("encodeTools: %v", err)
	}
	if len(tools) != 1 || tools[0].OfTool == nil {
		t.Fatalf("expected one encoded tool, got %#v", tools)
	}
	if len(tools[0].OfTool.InputExamples) != 1 {
		t.Fatalf("expected one input example, got %#v", tools[0].OfTool.InputExamples)
	}
	if got := tools[0].OfTool.InputExamples[0]["summary"]; got != "Done" {
		t.Fatalf("unexpected input example summary %v", got)
	}
	if _, ok := tools[0].OfTool.InputSchema.ExtraFields["example"]; ok {
		t.Fatalf("plain schema should not include root example: %#v", tools[0].OfTool.InputSchema.ExtraFields)
	}
}

func toolInputExampleSpec() tools.TypeSpec {
	return tools.TypeSpec{
		Name:                     "ReportsCompletePayload",
		Schema:                   tools.RawJSON(`{"type":"object","example":{"summary":"Done"}}`),
		SchemaWithoutRootExample: tools.RawJSON(`{"type":"object"}`),
		ExampleJSON:              tools.RawJSON(`{"summary":"Done"}`),
	}
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
