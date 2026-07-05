package runtime

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
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

func TestSimplePlannerContextModelClientDoesNotEmitPlannerEvents(t *testing.T) {
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
	ctx := &simplePlannerContext{
		rt:        rt,
		agent:     "svc.agent",
		runID:     "run-1",
		sessionID: "sess-1",
		ev:        events,
	}

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
	ctx := &simplePlannerContext{
		rt:        rt,
		agent:     "svc.agent",
		runID:     "run-1",
		sessionID: "sess-1",
		ev:        events,
	}

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

func TestToolUnavailableConfiguredClientDoesNotAdvertiseInternalToolByDefault(t *testing.T) {
	client := newToolUnavailableConfiguredClient(stubModelClient{
		complete: func(_ context.Context, req *model.Request) (*model.Response, error) {
			require.Len(t, req.Tools, 1)
			require.Equal(t, "svc.lookup", req.Tools[0].Name)
			return &model.Response{}, nil
		},
	})

	_, err := client.Complete(context.Background(), &model.Request{
		Tools: []*model.ToolDefinition{{
			Name: "svc.lookup",
		}},
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "lookup"}},
		}},
	})

	require.NoError(t, err)
}

// fakeSignatureSink is a minimal toolCallSignatureSink recorder for tests.
type fakeSignatureSink struct {
	captured map[string]string
}

func (s *fakeSignatureSink) recordToolCallSignature(toolCallID, signature string) {
	if s.captured == nil {
		s.captured = make(map[string]string)
	}
	s.captured[toolCallID] = signature
}

func TestNewSignatureCapturingClientReturnsInnerWhenSinkNil(t *testing.T) {
	inner := stubModelClient{}
	client := newSignatureCapturingClient(inner, nil)
	require.Equal(t, inner, client)
}

// TestSignatureCapturingClientCapturesFromCompleteResponse is the
// capture-side test for the non-streaming path: a finalized tool call on a
// Complete response must be recorded into the sink before any planner-facing
// type is constructed.
func TestSignatureCapturingClientCapturesFromCompleteResponse(t *testing.T) {
	sink := &fakeSignatureSink{}
	client := newSignatureCapturingClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				ToolCalls: []model.ToolCall{
					{ID: "call-1", Name: "svc.lookup", ThoughtSignature: "sig-1"},
					{ID: "call-2", Name: "svc.other"}, // no signature: must not be recorded
				},
			}, nil
		},
	}, sink)

	_, err := client.Complete(context.Background(), &model.Request{})

	require.NoError(t, err)
	require.Equal(t, map[string]string{"call-1": "sig-1"}, sink.captured)
}

// TestSignatureCapturingClientCapturesFromStreamedToolCallChunk is the
// capture-side test for the streaming path: a ChunkTypeToolCall chunk
// observed via Recv must be recorded into the sink as it is received.
func TestSignatureCapturingClientCapturesFromStreamedToolCallChunk(t *testing.T) {
	sink := &fakeSignatureSink{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			{Type: model.ChunkTypeText, Message: &model.Message{Role: model.ConversationRoleAssistant}},
			{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{ID: "call-1", Name: "svc.lookup", ThoughtSignature: "sig-1"},
			},
			{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{ID: "call-2", Name: "svc.other"}, // no signature
			},
		},
	}
	client := newSignatureCapturingClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, sink)

	st, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	for {
		if _, err := st.Recv(); err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}

	require.Equal(t, map[string]string{"call-1": "sig-1"}, sink.captured)
}

// TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient exercises
// the full runtime wiring for the "Option 2" streaming style (AGENTS.md):
// PlannerContext.ModelClient returns the raw client, and a planner drains it
// directly with planner.ConsumeStream. Capture must still happen even though
// ConsumeStream itself never sees or forwards a signature.
func TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient(t *testing.T) {
	rt := New()
	events := newPlannerEvents(rt, "svc.agent", "run-1", "sess-1", "turn-1")
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{ID: "call-1", Name: "svc.lookup", ThoughtSignature: "sig-1"},
			},
		},
	}
	rt.mu.Lock()
	rt.models = map[string]model.Client{
		"primary": stubModelClient{
			stream: func(context.Context, *model.Request) (model.Streamer, error) {
				return streamer, nil
			},
		},
	}
	rt.mu.Unlock()
	agentCtx := newAgentContext(agentContextOptions{
		runtime: rt,
		agentID: "svc.agent",
		runID:   "run-1",
		events:  events,
	})

	cli, ok := agentCtx.ModelClient("primary")
	require.True(t, ok)
	st, err := cli.Stream(context.Background(), &model.Request{Model: "gemini"})
	require.NoError(t, err)
	for {
		if _, err := st.Recv(); err != nil {
			break
		}
	}

	require.Equal(t, map[string]string{"call-1": "sig-1"}, events.exportToolCallSignatures())
}

// TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients pins the
// production wiring: preparePlannerActivity constructs runtimePlannerEvents
// and threads it into the planner context as the tool-call signature sink, so
// a model client obtained from that context captures provider thought
// signatures without any test-side replica of the wiring.
func TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients(t *testing.T) {
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{ID: "call-1", Name: "svc.lookup", ThoughtSignature: "sig-1"},
			},
		},
	}
	rt := &Runtime{
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {ID: "svc.agent"},
		},
		models: map[string]model.Client{
			"primary": stubModelClient{
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					return streamer, nil
				},
			},
		},
		logger:  telemetry.NewNoopLogger(),
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}

	act, err := rt.preparePlannerActivity(context.Background(), &PlanActivityInput{
		AgentID:    "svc.agent",
		RunID:      "run-1",
		RunContext: run.Context{SessionID: "sess-1", TurnID: "turn-1"},
	})
	require.NoError(t, err)

	cli, ok := act.agentCtx.ModelClient("primary")
	require.True(t, ok)
	st, err := cli.Stream(context.Background(), &model.Request{Model: "gemini"})
	require.NoError(t, err)
	for {
		if _, err := st.Recv(); err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}

	require.Equal(t, map[string]string{"call-1": "sig-1"}, act.events.exportToolCallSignatures())
}

func TestToolUnavailableConfiguredClientAdvertisesInternalToolForMissingHistoryToolUse(t *testing.T) {
	client := newToolUnavailableConfiguredClient(stubModelClient{
		complete: func(_ context.Context, req *model.Request) (*model.Response, error) {
			names := make([]string, 0, len(req.Tools))
			for _, tool := range req.Tools {
				names = append(names, tool.Name)
			}
			require.ElementsMatch(t, []string{"svc.lookup", tools.ToolUnavailable.String()}, names)
			return &model.Response{}, nil
		},
	})

	_, err := client.Complete(context.Background(), &model.Request{
		Tools: []*model.ToolDefinition{{
			Name: "svc.lookup",
		}},
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "tool-1",
				Name:  "svc.old_lookup",
				Input: map[string]any{"q": "status"},
			}},
		}},
	})

	require.NoError(t, err)
}
