package runtime

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingPlannerEvents struct {
		chunks []string
		usage  []model.TokenUsage
	}

	chunkStreamer struct {
		chunks []model.Chunk
		meta   map[string]any
		index  int
		closed bool
	}
)

func (e *recordingPlannerEvents) AssistantChunk(_ context.Context, text string) {
	e.chunks = append(e.chunks, text)
}

func (e *recordingPlannerEvents) ToolCallArgsDelta(context.Context, string, tools.Ident, string) {}

func (e *recordingPlannerEvents) PlannerThinkingBlock(context.Context, model.ThinkingPart) {}

func (e *recordingPlannerEvents) PlannerThought(context.Context, string, map[string]string) {}

func (e *recordingPlannerEvents) UsageDelta(_ context.Context, usage model.TokenUsage) {
	e.usage = append(e.usage, usage)
}

func (s *chunkStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		return model.Chunk{}, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *chunkStreamer) Close() error {
	s.closed = true
	return nil
}

func (s *chunkStreamer) Metadata() map[string]any {
	return s.meta
}

func TestSimplePlannerContextModelClientReturnsRawClient(t *testing.T) {
	events := &recordingPlannerEvents{}
	rt := &Runtime{
		models: map[string]model.Client{
			"primary": stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return &model.Response{
						Usage: model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
						Content: []model.Message{
							{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "hello"}},
							},
						},
					}, nil
				},
			},
		},
		logger: telemetry.NewNoopLogger(),
		tracer: telemetry.NoopTracer{},
	}
	ctx := &simplePlannerContext{rt: rt, ev: events}

	client, ok := ctx.ModelClient("primary")
	require.True(t, ok)

	resp, err := client.Complete(context.Background(), &model.Request{Model: "gpt-5"})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, events.usage)
}

func TestSimplePlannerContextPlannerModelClientOwnsEventEmission(t *testing.T) {
	events := &recordingPlannerEvents{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			{
				Type: model.ChunkTypeText,
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				},
			},
			{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{"q":"x"}`)},
			},
			{
				Type:       model.ChunkTypeUsage,
				UsageDelta: &model.TokenUsage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
			{
				Type:       model.ChunkTypeStop,
				StopReason: "tool_use",
			},
		},
		meta: map[string]any{
			"usage": model.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
	}
	rt := &Runtime{
		models: map[string]model.Client{
			"primary": stubModelClient{
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					return streamer, nil
				},
			},
		},
		logger: telemetry.NewNoopLogger(),
		tracer: telemetry.NoopTracer{},
	}
	ctx := &simplePlannerContext{rt: rt, ev: events}

	client, ok := ctx.PlannerModelClient("primary")
	require.True(t, ok)
	_, isRawModelClient := any(client).(model.Client)
	require.False(t, isRawModelClient)

	summary, err := client.Stream(
		context.Background(),
		&model.Request{Model: "gpt-5", ModelClass: model.ModelClassDefault},
	)

	require.NoError(t, err)
	require.Equal(t, "hello", summary.Text)
	require.Equal(t, []string{"hello"}, events.chunks)
	require.Len(t, summary.ToolCalls, 1)
	require.Equal(t, tools.Ident("svc.lookup"), summary.ToolCalls[0].Name)
	require.Equal(t, "tool_use", summary.StopReason)
	require.Equal(t, "gpt-5", summary.Usage.Model)
	require.Equal(t, model.ModelClassDefault, summary.Usage.ModelClass)
	require.Equal(t, 2, summary.Usage.InputTokens)
	require.Equal(t, 4, summary.Usage.OutputTokens)
	require.Equal(t, 6, summary.Usage.TotalTokens)
	require.True(t, streamer.closed)
	require.Len(t, events.usage, 1)
	require.Equal(t, "gpt-5", events.usage[0].Model)
	require.Equal(t, model.ModelClassDefault, events.usage[0].ModelClass)
}

func TestNewPlannerModelClientRequiresEvents(t *testing.T) {
	require.PanicsWithValue(t,
		"runtime: planner model client requires PlannerEvents",
		func() {
			_ = newPlannerModelClient(stubModelClient{}, nil)
		},
	)
}
