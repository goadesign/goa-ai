package executor

import (
	"context"
	"encoding/json"
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

type fakeRegistryClient struct {
	toolUseID string
}

func (c fakeRegistryClient) CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (string, error) {
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
