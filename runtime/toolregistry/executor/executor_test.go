package executor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
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
		resultEventName = "result"
	)

	specs := fakeSpecs{
		spec: &tools.ToolSpec{
			Name:          "todos.update_todos",
			Toolset:       "todos.todos",
			Result:        tools.TypeSpec{},
			Payload:       tools.TypeSpec{},
			Sidecar:       nil,
			BoundedResult: false,
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
		toolUseID:      toolUseID,
		resultStreamID: resultStreamID,
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
	assert.Equal(t, tools.Ident("todos.update_todos"), res.Name)
}

type fakeRegistryClient struct {
	toolUseID      string
	resultStreamID string
}

func (c fakeRegistryClient) CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (string, string, error) {
	return c.toolUseID, c.resultStreamID, nil
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
