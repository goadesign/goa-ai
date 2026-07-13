package runtime

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingPlannerEvents struct {
		chunks   []string
		thinking []model.ThinkingPart
		usage    []model.TokenUsage
	}

	chunkStreamer struct {
		chunks   []model.Chunk
		meta     map[string]any
		response *model.Response
		index    int
		closed   bool
	}
)

func (e *recordingPlannerEvents) AssistantChunk(_ context.Context, text string) {
	e.chunks = append(e.chunks, text)
}

func (e *recordingPlannerEvents) ToolCallArgsDelta(context.Context, string, tools.Ident, string) {}

func (e *recordingPlannerEvents) PlannerThinkingBlock(_ context.Context, thinking model.ThinkingPart) {
	e.thinking = append(e.thinking, thinking)
}

func (e *recordingPlannerEvents) PlannerThought(context.Context, string, map[string]string) {}

func (e *recordingPlannerEvents) UsageDelta(_ context.Context, usage model.TokenUsage) {
	e.usage = append(e.usage, usage)
}

func (s *chunkStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *chunkStreamer) Close() error {
	s.closed = true
	return nil
}

func (s *chunkStreamer) Response() *model.Response {
	return s.response
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
			model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{"q":"x"}`)},
			},
			model.UsageChunk{
				Usage: model.TokenUsage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			},
			model.StopChunk{
				Reason: "tool_use",
			},
		},
		meta: map[string]any{
			"usage": model.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
		response: testModelResponseWithUsage(
			[]model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hello"}}}},
			model.TokenUsage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
			model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{"q":"x"}`)},
		),
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

func TestPlannerModelClientIsSingleUseAndSelectsCanonicalResponse(t *testing.T) {
	events := &recordingPlannerEvents{}
	invocations := &modelInvocationJournal{}
	client := newPlannerModelClient(newDesignatedModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "selected"}},
			}}), nil
		},
	}, invocations), events)

	response, err := client.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), &model.Request{})
	require.EqualError(t, err, "runtime: PlannerModelClient permits exactly one model invocation per planner turn")

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	})
	require.NoError(t, err)
	require.Equal(t, "selected", transcript[0].Parts[0].(model.TextPart).Text)
}

func TestDesignatedModelInvocationWinsIdenticalProbeToolCalls(t *testing.T) {
	invocations := &modelInvocationJournal{}
	call := model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: rawjson.Message(`{"q":"status"}`)}
	probe := newModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "probe"}},
			}}, call), nil
		},
	}, invocations)
	designated := newDesignatedModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "designated"}},
			}}, call), nil
		},
	}, invocations)

	_, err := probe.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = designated.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:       call.Name,
			Payload:    call.Payload,
			ToolCallID: call.ID,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "designated", transcript[0].Parts[0].(model.TextPart).Text)
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

// fakeModelInvocationSink records complete responses and stream chunks by
// runtime-owned invocation.
type fakeModelInvocationSink struct {
	begins    uint64
	last      modelInvocationID
	responses map[modelInvocationID]*model.Response
	chunks    map[modelInvocationID][]model.Chunk
	finished  map[modelInvocationID]error
}

func (s *fakeModelInvocationSink) beginModelInvocation() modelInvocationID {
	s.begins++
	s.last = provenance.New()
	return s.last
}

func (s *fakeModelInvocationSink) designateModelInvocation(modelInvocationID) error {
	return nil
}

func (s *fakeModelInvocationSink) recordModelResponse(invocationID modelInvocationID, response *model.Response) error {
	if s.responses == nil {
		s.responses = make(map[modelInvocationID]*model.Response)
	}
	s.responses[invocationID] = response
	return nil
}

func (s *fakeModelInvocationSink) recordModelChunk(invocationID modelInvocationID, chunk model.Chunk) error {
	if s.chunks == nil {
		s.chunks = make(map[modelInvocationID][]model.Chunk)
	}
	s.chunks[invocationID] = append(s.chunks[invocationID], chunk)
	return nil
}

func (s *fakeModelInvocationSink) finishModelInvocation(invocationID modelInvocationID, err error) error {
	if s.finished == nil {
		s.finished = make(map[modelInvocationID]error)
	}
	s.finished[invocationID] = err
	return err
}

func TestNewModelInvocationClientReturnsInnerWhenSinkNil(t *testing.T) {
	inner := stubModelClient{}
	client := newModelInvocationClient(inner, nil)
	require.Equal(t, inner, client)
}

// TestModelInvocationClientCapturesFromCompleteResponse covers the
// non-streaming boundary: the call starts a fresh tentative transcript and
// captures finalized tool-call signatures before planner-facing types exist.
func TestModelInvocationClientCapturesFromCompleteResponse(t *testing.T) {
	sink := &fakeModelInvocationSink{}
	client := newModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil,
				model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
				model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)},
			), nil
		},
	}, sink)

	resp, err := client.Complete(context.Background(), &model.Request{})

	require.NoError(t, err)
	require.Equal(t, uint64(1), sink.begins)
	require.Same(t, resp, sink.responses[sink.last])
}

func TestPlannerModelClientScopesCompleteResponseTranscript(t *testing.T) {
	events := &recordingPlannerEvents{}
	invocations := &modelInvocationJournal{}
	client := newPlannerModelClient(newModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "reasoning",
						Signature: "thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "answer"},
				},
			}},
				model.ToolCall{
					ID:      "call-1",
					Name:    "svc.lookup",
					Payload: []byte(`{"query":"status"}`),
				},
			), nil
		},
	}, invocations), events)

	_, err := client.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	require.Equal(t, []string{"answer"}, events.chunks)
	require.Equal(t, []model.ThinkingPart{{
		Text:      "reasoning",
		Signature: "thinking-signature",
		Index:     0,
		Final:     true,
	}}, events.thinking)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ToolCallID: "call-1",
			Name:       "svc.lookup",
			Payload:    []byte(`{"query":"status"}`),
		}},
	})
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, model.ConversationRoleAssistant, transcript[0].Role)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "reasoning",
			Signature: "thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "answer"},
		model.ToolUsePart{
			ID:    "call-1",
			Name:  "svc.lookup",
			Input: rawjson.Message(`{"query":"status"}`),
		},
	}, transcript[0].Parts)
}

// TestModelInvocationClientCapturesFromStreamedToolCallChunk is the
// capture-side test for the streaming path: a ChunkTypeToolCall chunk
// observed via Recv must be recorded into the sink as it is received.
func TestModelInvocationClientCapturesFromStreamedToolCallChunk(t *testing.T) {
	sink := &fakeModelInvocationSink{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "working"}},
				},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)}, // no signature
			},
		},
		response: testModelResponse([]model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "working"}},
		}},
			model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)},
		),
	}
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, sink)

	st, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	for {
		_, err := st.Recv()
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}

	require.Equal(t, uint64(1), sink.begins)
	require.Equal(t, streamer.chunks, sink.chunks[sink.last])
	require.Contains(t, sink.finished, sink.last)
	require.NoError(t, sink.finished[sink.last])
}

func TestModelInvocationClientCapturesCanonicalResponseAtEOF(t *testing.T) {
	events := newPlannerEvents(New(), "svc.agent", "run-1", "sess-1", "turn-1")
	invocations := &modelInvocationJournal{}
	response := testModelResponse([]model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "canonical"}},
		Meta:  map[string]any{"provider_item": "item-1"},
	}},
		model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
	)
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: response,
	}
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, invocations)
	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(context.Background(), stream, &model.Request{}, events)
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{ToolCalls: summary.ToolCalls})

	require.NoError(t, err)
	require.Equal(t, "canonical", agentMessageText(transcript[0]))
	require.Equal(t, map[string]any{"provider_item": "item-1"}, transcript[0].Meta)
}

func TestModelInvocationClientRejectsStreamWithoutCanonicalResponse(t *testing.T) {
	events := newPlannerEvents(New(), "svc.agent", "run-1", "sess-1", "turn-1")
	invocations := &modelInvocationJournal{}
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{chunks: []model.Chunk{model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "partial"}},
				},
			}}}, nil
		},
	}, invocations)

	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = planner.ConsumeStream(context.Background(), stream, &model.Request{}, events)

	require.EqualError(t, err, "model stream ended without a canonical response")
}

func TestModelInvocationClientRejectsMalformedStreamChunk(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{chunks: []model.Chunk{model.ToolCallChunk{}}}, nil
		},
	}, invocations)

	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, err, "tool call is missing its ID")
}

// TestRawModelInvocationSelectionKeepsResponsesIsolated reproduces two probe
// calls and proves that call order does not choose the durable transcript.
func TestRawModelInvocationSelectionKeepsResponsesIsolated(t *testing.T) {
	rt := New()
	events := newPlannerEvents(rt, "svc.agent", "run-1", "sess-1", "turn-1")
	invocations := &modelInvocationJournal{}
	streamers := []model.Streamer{
		&chunkStreamer{
			chunks: []model.Chunk{
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:  "tentative ",
							Index: 0,
						}},
					},
				},
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:      "tentative reasoning",
							Signature: "tentative-thinking-signature",
							Index:     0,
							Final:     true,
						}},
					},
				},
				model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "tentative response"}},
					},
				},
				model.ToolCallChunk{
					ToolCall: model.ToolCall{
						ID:               "tentative-call",
						Name:             "svc.lookup",
						Payload:          []byte(`{}`),
						ThoughtSignature: "tentative-tool-signature",
					},
				},
				model.UsageChunk{
					Usage: model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
				},
				model.StopChunk{Reason: "tool_use"},
			},
			response: testModelResponseWithUsage([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "tentative reasoning",
						Signature: "tentative-thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "tentative response"},
				},
			}}, model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
				model.ToolCall{
					ID:               "tentative-call",
					Name:             "svc.lookup",
					Payload:          []byte(`{}`),
					ThoughtSignature: "tentative-tool-signature",
				},
			),
		},
		&chunkStreamer{
			chunks: []model.Chunk{
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:      "accepted reasoning",
							Signature: "accepted-thinking-signature",
							Index:     0,
							Final:     true,
						}},
					},
				},
				model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "accepted response"}},
					},
				},
				model.ToolCallChunk{
					ToolCall: model.ToolCall{
						ID:               "accepted-call",
						Name:             "svc.lookup",
						Payload:          []byte(`{}`),
						ThoughtSignature: "accepted-tool-signature",
					},
				},
				model.UsageChunk{
					Usage: model.TokenUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
				},
				model.StopChunk{Reason: "tool_use"},
			},
			response: testModelResponseWithUsage([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "accepted reasoning",
						Signature: "accepted-thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "accepted response"},
				},
			}}, model.TokenUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
				model.ToolCall{
					ID:               "accepted-call",
					Name:             "svc.lookup",
					Payload:          []byte(`{}`),
					ThoughtSignature: "accepted-tool-signature",
				},
			),
		},
	}
	next := 0
	client := newModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			streamer := streamers[next]
			next++
			return streamer, nil
		},
	}, invocations)

	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	tentative, err := planner.ConsumeStream(context.Background(), stream, &model.Request{}, events)
	require.NoError(t, err)
	stream, err = client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	accepted, err := planner.ConsumeStream(context.Background(), stream, &model.Request{}, events)
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: accepted.ToolCalls,
	})
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "accepted reasoning",
			Signature: "accepted-thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "accepted response"},
		model.ToolUsePart{
			ID:               "accepted-call",
			Name:             "svc.lookup",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "accepted-tool-signature",
		},
	}, transcript[0].Parts)

	tentativeTranscript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: tentative.ToolCalls,
	})
	require.NoError(t, err)
	require.Len(t, tentativeTranscript, 1)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "tentative reasoning",
			Signature: "tentative-thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "tentative response"},
		model.ToolUsePart{
			ID:               "tentative-call",
			Name:             "svc.lookup",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "tentative-tool-signature",
		},
	}, tentativeTranscript[0].Parts)
	require.Equal(t, model.TokenUsage{
		InputTokens:  10,
		OutputTokens: 16,
		TotalTokens:  26,
	}, invocations.exportUsage())
}

// TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient exercises
// the full runtime wiring for the "Option 2" streaming style (AGENTS.md):
// PlannerContext.ModelClient returns the raw client, and a planner drains it
// directly with planner.ConsumeStream. Capture must still happen even though
// ConsumeStream itself never sees or forwards a signature.
func TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient(t *testing.T) {
	rt := New()
	events := newPlannerEvents(rt, "svc.agent", "run-1", "sess-1", "turn-1")
	invocations := &modelInvocationJournal{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse(nil, model.ToolCall{
			ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1",
		}),
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
		runtime:     rt,
		agentID:     "svc.agent",
		runID:       "run-1",
		events:      events,
		invocations: invocations,
	})

	cli, ok := agentCtx.ModelClient("primary")
	require.True(t, ok)
	st, err := cli.Stream(context.Background(), &model.Request{Model: "gemini"})
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(context.Background(), st, &model.Request{Model: "gemini"}, events)
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: summary.ToolCalls,
	})
	require.NoError(t, err)
	require.Equal(t, "sig-1", transcript[0].Parts[0].(model.ToolUsePart).ThoughtSignature)
}

// TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients pins the
// production wiring: preparePlannerActivity constructs an invocation journal
// independently from planner events, and a model client obtained from that
// context captures provider thought signatures without planner participation.
func TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients(t *testing.T) {
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse(nil, model.ToolCall{
			ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1",
		}),
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
	summary, err := planner.ConsumeStream(context.Background(), st, &model.Request{Model: "gemini"}, act.events)
	require.NoError(t, err)

	transcript, err := act.invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: summary.ToolCalls,
	})
	require.NoError(t, err)
	require.Equal(t, "sig-1", transcript[0].Parts[0].(model.ToolUsePart).ThoughtSignature)
}

func TestToolUnavailableConfiguredClientDoesNotRewriteUnknownHistoricalTool(t *testing.T) {
	client := newToolUnavailableConfiguredClient(stubModelClient{
		complete: func(_ context.Context, req *model.Request) (*model.Response, error) {
			names := make([]string, 0, len(req.Tools))
			for _, tool := range req.Tools {
				names = append(names, tool.Name)
			}
			require.Equal(t, []string{"svc.lookup"}, names)
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
				Input: rawjson.Message(`{"q":"status"}`),
			}},
		}},
	})

	require.NoError(t, err)
}

func TestToolUnavailableConfiguredClientRestoresHistoricalRuntimeTool(t *testing.T) {
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
		Tools: []*model.ToolDefinition{{Name: "svc.lookup"}},
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "tool-1",
				Name:  tools.ToolUnavailable.String(),
				Input: rawjson.Message(`{"requested_tool":"svc.old_lookup"}`),
			}},
		}},
	})

	require.NoError(t, err)
}
