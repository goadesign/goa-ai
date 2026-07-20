package executor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	aistream "goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestExecutorUsesOldestStartForResultStreamSink(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					ToolUseID: toolUseID,
					Result:    json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	exec := New(fakeRegistryClient{
		toolUseID: toolUseID,
	}, pc, specs, WithResultEventKey(resultEventName))

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	assert.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Equal(t, tools.Ident("todos.update_todos"), res.ToolResult.Name)
}

func TestBuildRetryHintFromIssuesRestrictsToFailedTool(t *testing.T) {
	t.Parallel()

	hint := buildRetryHintFromIssues("atlas.read.recommend_signals", nil, []*tools.FieldIssue{{
		Field:            "selector",
		Constraint:       "invalid_field_type",
		ExpectedJSONType: "object",
		ActualJSONType:   "string",
	}})

	require.NotNil(t, hint)
	assert.Equal(t, planner.RetryReasonInvalidArguments, hint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.recommend_signals"), hint.Tool)
	assert.True(t, hint.RestrictToTool)
	assert.Equal(t, []string{"selector"}, hint.MissingFields)
	assert.Contains(t, hint.ClarifyingQuestion, "`selector` must be a JSON object, not a JSON string")
}

func TestRetryHintFromInvalidArgumentsRestrictsToFailedTool(t *testing.T) {
	t.Parallel()

	hint := retryHintFromToolErrorCode("atlas.read.recommend_signals", "invalid_arguments")

	require.NotNil(t, hint)
	assert.Equal(t, planner.RetryReasonInvalidArguments, hint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.recommend_signals"), hint.Tool)
	assert.True(t, hint.RestrictToTool)
}

func TestRetryHintFromTimeoutClassifiesWithoutRetry(t *testing.T) {
	t.Parallel()

	hint := retryHintFromToolErrorCode("atlas.read.get_time_series", "timeout")

	require.NotNil(t, hint)
	assert.Equal(t, planner.RetryReasonTimeout, hint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.get_time_series"), hint.Tool)
	assert.False(t, hint.RestrictToTool, "a timeout is terminal and must not force a same-tool retry")

	// Unknown codes carry no hint.
	assert.Nil(t, retryHintFromToolErrorCode("atlas.read.get_time_series", "query_too_expensive"))
}

func TestRetryHintFromInvalidInputDoesNotRestrict(t *testing.T) {
	t.Parallel()

	// Service-level invalid_input is a domain rejection that may name a sibling
	// tool as the remedy. It classifies as invalid input for the UI but must not
	// pin the model to the rejecting tool.
	hint := retryHintFromToolErrorCode("atlas.read.get_time_series", "invalid_input")

	require.NotNil(t, hint)
	assert.Equal(t, planner.RetryReasonInvalidArguments, hint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.get_time_series"), hint.Tool)
	assert.False(t, hint.RestrictToTool, "invalid_input must not force a same-tool retry")
}

func TestExecutorErrorHintCarriesExampleJSONOnlyWhenRestricting(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-example"
		toolCallID = "toolcall-example"
	)
	example := tools.RawJSON(`{"query":"latency"}`)

	cases := []struct {
		name         string
		code         string
		wantReason   planner.RetryReason
		wantRestrict bool
		wantExample  bool
	}{
		// A timeout is terminal: it must not hand the model a payload-correction
		// template that reads as a same-tool retry instruction.
		{"timeout carries no retry template", "timeout", planner.RetryReasonTimeout, false, false},
		// invalid_input may name a sibling tool as the remedy; a payload example
		// would wrongly pin the model to the rejecting tool.
		{"invalid_input carries no retry template", "invalid_input", planner.RetryReasonInvalidArguments, false, false},
		// invalid_arguments restricts to the same tool for payload correction, so
		// the canonical example belongs here.
		{"invalid_arguments carries the payload example", "invalid_arguments", planner.RetryReasonInvalidArguments, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := &tools.ToolSpec{
				Name:    "atlas.read.get_time_series",
				Toolset: "atlas.read",
				Result:  tools.TypeSpec{},
				Payload: tools.TypeSpec{ExampleJSON: example},
			}
			res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, toolregistry.ToolResultMessage{
				ToolUseID: toolUseID,
				Error:     &toolregistry.ToolError{Code: tc.code, Message: "boom"},
			}, spec)
			require.NoError(t, err)
			require.NotNil(t, res)
			require.NotNil(t, res.ToolResult)
			require.NotNil(t, res.ToolResult.RetryHint)
			assert.Equal(t, tc.wantReason, res.ToolResult.RetryHint.Reason)
			assert.Equal(t, tc.wantRestrict, res.ToolResult.RetryHint.RestrictToTool)
			if tc.wantExample {
				assert.NotEmpty(t, res.ToolResult.RetryHint.ExampleJSON)
			} else {
				assert.Empty(t, res.ToolResult.RetryHint.ExampleJSON)
			}
		})
	}
}

func TestExecutorTransportFailureClassifiesToolUnavailable(t *testing.T) {
	t.Parallel()

	spec := &tools.ToolSpec{
		Name:    "atlas.read.get_time_series",
		Toolset: "atlas.read",
		Result:  tools.TypeSpec{},
		Payload: tools.TypeSpec{ExampleJSON: tools.RawJSON(`{"query":"latency"}`)},
	}
	exec := New(
		fakeRegistryClient{err: errors.New("dial registry gateway: connection refused")},
		fakePulseClient{},
		fakeSpecs{spec: spec},
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: "toolcall-transport",
	}, &planner.ToolRequest{
		Name:    "atlas.read.get_time_series",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	require.NotNil(t, res.ToolResult.Error)
	require.NotNil(t, res.ToolResult.RetryHint)
	assert.Equal(t, planner.RetryReasonToolUnavailable, res.ToolResult.RetryHint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.get_time_series"), res.ToolResult.RetryHint.Tool)
	assert.False(t, res.ToolResult.RetryHint.RestrictToTool, "a transport blip must not force a same-tool retry")
	assert.Empty(t, res.ToolResult.RetryHint.ExampleJSON, "a transient transport failure is not a payload-correction case")
	assert.Equal(t, "toolcall-transport", res.ToolResult.ToolCallID)
}

func TestExecutorDerivesResultStreamIDFromToolUseID(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-derive-123"
		resultEventName = toolregistry.ResultEventKey
	)
	expectedResultStreamID := toolregistry.ResultStreamID(toolUseID)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					ToolUseID: toolUseID,
					Result:    json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: expectedResultStreamID,
		stream:   stream,
	}

	exec := New(fakeRegistryClient{
		toolUseID: toolUseID,
	}, pc, specs, WithResultEventKey(resultEventName))

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})

	require.NoError(t, err)
	assert.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Equal(t, tools.Ident("todos.update_todos"), res.ToolResult.Name)
}

func TestExecutorEmitsRegistrySpan(t *testing.T) {
	tracer := &recordingTracer{}
	const (
		toolUseID       = "tooluse-genai-123"
		resultEventName = toolregistry.ResultEventKey
	)
	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					ToolUseID: toolUseID,
					Result:    json.RawMessage(`{}`),
				}),
			},
		},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		specs,
		WithTracer(tracer),
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		TurnID:     "turn",
		ToolCallID: "toolcall-1",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, tracer.spans, 1)
	assert.Equal(t, "toolregistry.execute", tracer.spans[0].name)
	attrs := attrsByKey(tracer.spans[0].attrs)
	assert.Equal(t, "todos.update_todos", attrs[attribute.Key("toolregistry.tool")].AsString())
	assert.Equal(t, "todos.todos", attrs[attribute.Key("toolregistry.toolset")].AsString())
	assert.Equal(t, "toolcall-1", attrs[attribute.Key("toolregistry.tool_call_id")].AsString())
	assert.Equal(t, "agent", attrs[attribute.Key("toolregistry.sink")].AsString())
}

type captureSink struct {
	events []aistream.Event
}

type recordingTracer struct {
	spans []*recordingSpan
}

type recordingSpan struct {
	name  string
	attrs []attribute.KeyValue
}

func (s *captureSink) Send(ctx context.Context, event aistream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *captureSink) Close(ctx context.Context) error {
	return nil
}

func TestExecutorForwardsOutputDelta(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "todos.update_todos",
			Toolset: "todos.todos",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
		},
	}

	delta := toolregistry.NewToolOutputDeltaMessage(toolUseID, "stdout", "hi\n")
	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.OutputDeltaEventKey,
				Payload:   mustJSON(t, delta),
			},
			{
				ID:        "2-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					ToolUseID: toolUseID,
					Result:    json.RawMessage(`{}`),
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	sink := &captureSink{}
	exec := New(
		fakeRegistryClient{
			toolUseID: toolUseID,
		},
		pc,
		specs,
		WithResultEventKey(resultEventName),
		WithStreamSink(sink),
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: "toolcall-1",
	}, &planner.ToolRequest{
		Name:    "todos.update_todos",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, sink.events, 1)
	ev, ok := sink.events[0].(aistream.ToolOutputDelta)
	require.True(t, ok)
	assert.Equal(t, aistream.EventToolOutputDelta, ev.Type())
	assert.Equal(t, "run", ev.RunID())
	assert.Equal(t, "sess", ev.SessionID())
	assert.Equal(t, "toolcall-1", ev.Data.ToolCallID)
	assert.Equal(t, "stdout", ev.Data.Stream)
	assert.Equal(t, "hi\n", ev.Data.Delta)
}

func TestExecutorRestoresBoundsFromRegistryMessage(t *testing.T) {
	t.Parallel()

	const (
		toolUseID       = "tooluse-123"
		resultStreamID  = "result:" + toolUseID
		resultEventName = toolregistry.ResultEventKey
	)
	nextCursor := "cursor-2"

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:    "atlas.read.list_devices",
			Toolset: "atlas.read",
			Result:  tools.TypeSpec{},
			Payload: tools.TypeSpec{},
			Bounds: &tools.BoundsSpec{
				Paging: &tools.PagingSpec{
					CursorField:     "cursor",
					NextCursorField: "next_cursor",
				},
			},
		},
	}

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: resultEventName,
				Payload: mustJSON(t, toolregistry.ToolResultMessage{
					ToolUseID: toolUseID,
					Result:    json.RawMessage(`{}`),
					Bounds: &agent.Bounds{
						Returned:       1,
						Truncated:      true,
						NextCursor:     &nextCursor,
						RefinementHint: "narrow by device",
					},
				}),
			},
		},
	}
	pc := fakePulseClient{
		streamID: resultStreamID,
		stream:   stream,
	}

	exec := New(
		fakeRegistryClient{
			toolUseID: toolUseID,
		},
		pc,
		specs,
		WithResultEventKey(resultEventName),
	)

	res, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:     "run",
		SessionID: "sess",
	}, &planner.ToolRequest{
		Name:    "atlas.read.list_devices",
		Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	require.NotNil(t, res.ToolResult.Bounds)
	assert.Equal(t, 1, res.ToolResult.Bounds.Returned)
	assert.True(t, res.ToolResult.Bounds.Truncated)
	require.NotNil(t, res.ToolResult.Bounds.NextCursor)
	assert.Equal(t, nextCursor, *res.ToolResult.Bounds.NextCursor)
	assert.Equal(t, "narrow by device", res.ToolResult.Bounds.RefinementHint)
}

func TestExecutorRejectsInvalidRegistryErrorResults(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-invalid"
		toolCallID = "toolcall-invalid"
	)
	cases := []struct {
		name string
		msg  toolregistry.ToolResultMessage
		want string
	}{
		{
			name: "error and bounds",
			msg: toolregistry.ToolResultMessage{
				ToolUseID: toolUseID,
				Error:     &toolregistry.ToolError{Code: "execution_failed", Message: "failed"},
				Bounds:    &agent.Bounds{Returned: 1},
			},
			want: "error and bounds are both set",
		},
		{
			name: "error and result",
			msg: toolregistry.ToolResultMessage{
				ToolUseID: toolUseID,
				Error:     &toolregistry.ToolError{Code: "execution_failed", Message: "failed"},
				Result:    json.RawMessage(`{"ok":true}`),
			},
			want: "error and result are both set",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, tc.msg, &tools.ToolSpec{
				Name:    "atlas.read.list_devices",
				Toolset: "atlas.read",
				Result:  tools.TypeSpec{},
				Payload: tools.TypeSpec{},
			})

			require.Error(t, err)
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "tool_call_id="+toolCallID)
			assert.Contains(t, err.Error(), "tool_use_id="+toolUseID)
		})
	}
}

func TestExecutorResultDecodeFailureReturnsModelVisibleErrorWithoutBounds(t *testing.T) {
	t.Parallel()

	const (
		toolUseID  = "tooluse-decode"
		toolCallID = "toolcall-decode"
	)
	nextCursor := "cursor-2"
	spec := &tools.ToolSpec{
		Name:    "atlas.read.list_devices",
		Toolset: "atlas.read",
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					return nil, errors.New("invalid enum value \"retired\"")
				},
			},
		},
		Payload: tools.TypeSpec{},
		Bounds: &tools.BoundsSpec{
			Paging: &tools.PagingSpec{
				CursorField:     "cursor",
				NextCursorField: "next_cursor",
			},
		},
	}

	res, err := executeRegistryResultMessage(t, toolUseID, toolCallID, toolregistry.ToolResultMessage{
		ToolUseID: toolUseID,
		Result:    json.RawMessage(`{"status":"retired"}`),
		Bounds: &agent.Bounds{
			Returned:   1,
			Truncated:  true,
			NextCursor: &nextCursor,
		},
		ServerData: []*toolregistry.ServerDataItem{{
			Kind:     "atlas.devices",
			Audience: "internal",
			Data:     json.RawMessage(`{"count":1}`),
		}},
	}, spec)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.ToolResult)
	assert.Nil(t, res.ToolResult.Bounds)
	assert.Nil(t, res.ToolResult.ServerData)
	require.NotNil(t, res.ToolResult.Error)
	assert.Contains(t, res.ToolResult.Error.Error(), "invalid enum value \"retired\"")
	require.NotNil(t, res.ToolResult.RetryHint)
	assert.Equal(t, planner.RetryReasonMalformedResponse, res.ToolResult.RetryHint.Reason)
	assert.Equal(t, tools.Ident("atlas.read.list_devices"), res.ToolResult.RetryHint.Tool)
	assert.False(t, res.ToolResult.RetryHint.RestrictToTool)
	assert.Contains(t, res.ToolResult.RetryHint.Message, "registered result schema")
}

type fakeRegistryClient struct {
	toolUseID string
	err       error
}

func (c fakeRegistryClient) CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	return c.toolUseID, nil
}

type fakeSpecs struct {
	spec *tools.ToolSpec
}

func (s fakeSpecs) Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	if s.spec == nil {
		return nil, false
	}
	if s.spec.Name != name {
		return nil, false
	}
	return s.spec, true
}

type fakePulseClient struct {
	streamID string
	stream   pulse.Stream
}

func (c fakePulseClient) Stream(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
	if name != c.streamID {
		return nil, assert.AnError
	}
	return c.stream, nil
}

func (c fakePulseClient) Close(ctx context.Context) error {
	return nil
}

type fakeStream struct {
	t             *testing.T
	requiredStart string
	events        []*streaming.Event
}

func (s *fakeStream) Add(ctx context.Context, event string, payload []byte) (string, error) {
	return "", assert.AnError
}

func (s *fakeStream) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (pulse.Sink, error) {
	o := streamopts.ParseSinkOptions(opts...)
	assert.Equal(s.t, s.requiredStart, o.LastEventID)
	return &fakeSink{events: s.events}, nil
}

func (s *fakeStream) Destroy(ctx context.Context) error {
	return nil
}

type fakeSink struct {
	events []*streaming.Event
}

func (s *fakeSink) Subscribe() <-chan *streaming.Event {
	ch := make(chan *streaming.Event, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch
}

func (s *fakeSink) Ack(ctx context.Context, ev *streaming.Event) error {
	return nil
}

func (s *fakeSink) Close(ctx context.Context) {}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func executeRegistryResultMessage(
	t *testing.T,
	toolUseID string,
	toolCallID string,
	msg toolregistry.ToolResultMessage,
	spec *tools.ToolSpec,
) (*agentsruntime.ToolExecutionResult, error) {
	t.Helper()

	stream := &fakeStream{
		t:             t,
		requiredStart: "0",
		events: []*streaming.Event{
			{
				ID:        "1-0",
				EventName: toolregistry.ResultEventKey,
				Payload:   mustJSON(t, msg),
			},
		},
	}
	exec := New(
		fakeRegistryClient{toolUseID: toolUseID},
		fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
		fakeSpecs{spec: spec},
	)
	return exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
		RunID:      "run",
		SessionID:  "sess",
		ToolCallID: toolCallID,
	}, &planner.ToolRequest{
		Name:    spec.Name,
		Payload: []byte(`{}`),
	})
}

func (t *recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	span := &recordingSpan{
		name:  name,
		attrs: cfg.Attributes(),
	}
	t.spans = append(t.spans, span)
	return ctx, span
}

func (t *recordingTracer) Span(context.Context) telemetry.Span {
	if len(t.spans) == 0 {
		return &recordingSpan{}
	}
	return t.spans[len(t.spans)-1]
}

func (s *recordingSpan) End(...trace.SpanEndOption) {}

func (s *recordingSpan) AddEvent(string, ...any) {}

func (s *recordingSpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.attrs = append(s.attrs, attrs...)
}

func (s *recordingSpan) SetStatus(codes.Code, string) {}

func (s *recordingSpan) RecordError(error, ...trace.EventOption) {}

func attrsByKey(attrs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	out := make(map[attribute.Key]attribute.Value, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value
	}
	return out
}
