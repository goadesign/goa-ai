//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type cancellationObservingStreamer struct {
	ctx    context.Context
	closed chan struct{}
}

func (s *cancellationObservingStreamer) Recv() (model.Chunk, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *cancellationObservingStreamer) Response() *model.Response {
	return nil
}

func (s *cancellationObservingStreamer) Close() error {
	if s.ctx.Err() == nil {
		return errors.New("stream closed before provider context cancellation")
	}
	close(s.closed)
	return nil
}

func TestRunPlanActivityUsesOptions(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}},
			},
		},
	}

	opts := engine.ActivityOptions{
		Queue:                  "custom_queue",
		ScheduleToStartTimeout: 10 * time.Second,
		ScheduleToCloseTimeout: 45 * time.Second,
		StartToCloseTimeout:    30 * time.Second,
		RetryPolicy:            engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2},
	}
	_, err := rt.runPlanActivity(wf, "calc.agent.plan", opts, PlanActivityInput{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, opts.Queue, wf.lastPlannerCall.Options.Queue)
	require.Equal(t, opts.ScheduleToStartTimeout, wf.lastPlannerCall.Options.ScheduleToStartTimeout)
	require.Equal(t, opts.ScheduleToCloseTimeout, wf.lastPlannerCall.Options.ScheduleToCloseTimeout)
	require.Equal(t, opts.StartToCloseTimeout, wf.lastPlannerCall.Options.StartToCloseTimeout)
	require.Equal(t, opts.RetryPolicy, wf.lastPlannerCall.Options.RetryPolicy)
}

func TestRunPlanActivityBoundsTotalLifetimeToRemainingDeadline(t *testing.T) {
	retry := engine.RetryPolicy{
		MaxAttempts:        3,
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
	}
	tests := []struct {
		name string
		opts engine.ActivityOptions
		want time.Duration
	}{
		{
			name: "preserves queue and attempt timeouts",
			opts: engine.ActivityOptions{
				ScheduleToStartTimeout: 5 * time.Second,
				ScheduleToCloseTimeout: 5 * time.Second,
				StartToCloseTimeout:    5 * time.Second,
				RetryPolicy:            retry,
			},
			want: 10 * time.Second,
		},
		{
			name: "replaces longer configured total timeout",
			opts: engine.ActivityOptions{
				ScheduleToStartTimeout: 30 * time.Second,
				ScheduleToCloseTimeout: 30 * time.Second,
				StartToCloseTimeout:    30 * time.Second,
				RetryPolicy:            retry,
			},
			want: 10 * time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := &Runtime{
				logger:  telemetry.NoopLogger{},
				metrics: telemetry.NoopMetrics{},
				tracer:  telemetry.NoopTracer{},
				Bus:     noopHooks{},
			}
			wf := &testWorkflowContext{
				ctx:           context.Background(),
				hasPlanResult: true,
				planResult: &PlanResult{
					FinalResponse: &planner.FinalResponse{
						Message: &model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: "ok"}},
						},
					},
				},
			}

			_, err := rt.runPlanActivity(
				wf,
				"calc.agent.plan",
				test.opts,
				PlanActivityInput{},
				wf.Now().Add(10*time.Second),
			)

			require.NoError(t, err)
			require.Equal(t, test.opts.ScheduleToStartTimeout, wf.lastPlannerCall.Options.ScheduleToStartTimeout)
			require.Equal(t, test.want, wf.lastPlannerCall.Options.ScheduleToCloseTimeout)
			require.Equal(t, test.opts.StartToCloseTimeout, wf.lastPlannerCall.Options.StartToCloseTimeout)
			require.Equal(t, retry, wf.lastPlannerCall.Options.RetryPolicy)
		})
	}
}

func TestRunPlanActivityRejectsExpiredDeadline(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx: context.Background(),
	}

	_, err := rt.runPlanActivity(wf, "calc.agent.plan", engine.ActivityOptions{}, PlanActivityInput{}, time.Unix(-1, 0))

	require.ErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, wf.lastPlannerCall.Name)
}

func TestRunPlanActivityAcceptsTerminalFinalToolResult(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &PlanResult{
			FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message([]byte(`{"status":"ok"}`)),
			},
		},
	}

	out, err := rt.runPlanActivity(wf, "calc.agent.plan", engine.ActivityOptions{}, PlanActivityInput{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Result)
	require.NotNil(t, out.Result.FinalToolResult)
	require.JSONEq(t, `{"status":"ok"}`, string(out.Result.FinalToolResult.Result))
}

func TestRunPlanActivityRejectsNilPlanResultWithoutCriticalPrefix(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx: context.Background(),
	}

	_, err := rt.runPlanActivity(wf, "calc.agent.plan", engine.ActivityOptions{}, PlanActivityInput{}, time.Time{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil PlanResult")
	require.NotContains(t, err.Error(), "CRITICAL:")
}

func TestPlanStartActivityInvokesPlanner(t *testing.T) {
	called := false
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.Equal(t, run.Context{RunID: "run-123"}, input.RunContext)
		require.Len(t, input.Messages, 1)
		require.Equal(t, "hello", agentMessageText(input.Messages[0]))
		require.NotNil(t, input.Agent)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}}, RunContext: run.Context{RunID: "run-123"}}
	out, err := rt.PlanStartActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlanStartActivityCannotHideMalformedModelOutput(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		rawResponse, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, rawResponse)
		require.ErrorIs(t, err, outputErr)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return nil, nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityRejectsModelToolPayloadBeforeRecovery(t *testing.T) {
	providerCalls := 0
	call := model.ToolCall{
		ID:      "call-1",
		Name:    "service.lookup",
		Payload: rawjson.Message(`{"query":42}`),
	}
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{
			Model: "test",
			Tools: input.Agent.AdvertisedToolDefinitions(),
		})
		require.Nil(t, response)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		_, secondErr := client.Complete(ctx, &model.Request{
			Model: "test",
			Tools: input.Agent.AdvertisedToolDefinitions(),
		})
		require.ErrorIs(t, secondErr, outputErr)
		return &planner.PlanResult{
			ToolCalls: []planner.ToolRequest{{
				Name:            call.Name,
				Payload:         call.Payload,
				ModelToolCallID: call.ID,
			}},
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	spec := newAnyJSONSpec(call.Name, "service")
	spec.Payload.Codec.FromJSON = func(data []byte) (any, error) {
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
	seedTestToolSpecs(rt, spec)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {spec},
	}
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return testModelResponse(nil, call), nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityRejectsStreamedToolPayloadBeforePlannerExposure(t *testing.T) {
	providerCalls := 0
	call := model.ToolCall{
		ID:      "call-1",
		Name:    "service.lookup",
		Payload: rawjson.Message(`{"query":42}`),
	}
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		request := &model.Request{
			Model: "test",
			Tools: input.Agent.AdvertisedToolDefinitions(),
		}
		stream, err := client.Stream(ctx, request)
		require.NoError(t, err)
		chunk, err := stream.Recv()
		require.Nil(t, chunk)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		second, secondErr := client.Stream(ctx, request)
		require.Nil(t, second)
		require.ErrorIs(t, secondErr, outputErr)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	spec := newAnyJSONSpec(call.Name, "service")
	spec.Payload.Codec.FromJSON = func(data []byte) (any, error) {
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
	seedTestToolSpecs(rt, spec)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {spec},
	}
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.ToolCallChunk{ToolCall: call},
					model.StopChunk{Reason: "tool_use"},
				},
				response: testModelResponse(nil, call),
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityCannotHideTypedCompletionFailure(t *testing.T) {
	providerCalls := 0
	spec := completion.Spec[string]{
		Name:   "strict_completion",
		Schema: rawjson.Message(`{"type":"string"}`),
		Codec: tools.JSONCodec[string]{
			ToJSON: func(string) ([]byte, error) {
				return []byte(`"valid"`), nil
			},
			FromJSON: func([]byte) (string, error) {
				return "", errors.New("typed value is invalid")
			},
		},
	}
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := completion.Complete(ctx, client, &model.Request{Model: "test"}, spec)
		require.Nil(t, response)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		rawResponse, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, rawResponse)
		require.ErrorIs(t, err, outputErr)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: `"valid"`}},
				}},
				StopReason: "end_turn",
				Usage: model.TokenUsage{
					InputTokens:  3,
					OutputTokens: 2,
					TotalTokens:  5,
				},
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 5, out.Usage.TotalTokens)
	require.Len(t, out.PlannerEvents, 1)
	require.Len(t, out.OutputContractFailure.ModelResponseSHA256, 64)
	require.Positive(t, out.OutputContractFailure.ModelResponseSize)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityCannotHideConflictingStreamOutput(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := client.Stream(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		_, err = planner.ConsumeStream(ctx, stream)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		require.ErrorContains(t, err, "streamed text does not match canonical response")
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.ErrorIs(t, err, outputErr)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "streamed"}},
					}},
					model.StopChunk{Reason: "end_turn"},
				},
				response: &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "canonical"}},
					}},
					StopReason: "end_turn",
				},
			}, nil
		},
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "second"}},
			}}), nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivitySelectsIdenticalStreamMessageByOrigin(t *testing.T) {
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.PlannerModelClient("test")
		require.True(t, ok)
		summary, err := client.Stream(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		return &planner.PlanResult{
			FinalResponse: summary.FinalResponse(),
			Streamed:      true,
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			message := model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "same"}},
			}
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: message},
					model.TextChunk{Message: message},
					model.StopChunk{Reason: "end_turn"},
				},
				response: &model.Response{
					Content:    []model.Message{message, message},
					StopReason: "end_turn",
				},
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	require.NoError(t, err)
	require.NotNil(t, out.Result)
	require.NotNil(t, out.Result.FinalResponse)
	require.Equal(t, "same", out.Result.FinalResponse.Message.Parts[0].(model.TextPart).Text)
}

func TestPlanStartActivityCannotHideTypedStreamFailure(t *testing.T) {
	providerCalls := 0
	spec := completion.Spec[string]{
		Name:   "strict_completion",
		Schema: rawjson.Message(`{"type":"string"}`),
		Codec: tools.JSONCodec[string]{
			ToJSON: func(string) ([]byte, error) {
				return []byte(`"valid"`), nil
			},
			FromJSON: func([]byte) (string, error) {
				return "", errors.New("typed stream value is invalid")
			},
		},
	}
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := completion.Stream(ctx, client, &model.Request{Model: "test"}, spec)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, stream.Close())
		}()
		_, err = stream.Recv()
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		require.ErrorContains(t, err, "typed stream value is invalid")
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.ErrorIs(t, err, outputErr)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.CompletionChunk{Completion: model.Completion{
						Name:    "strict_completion",
						Payload: []byte(`"invalid"`),
					}},
					model.StopChunk{Reason: "end_turn"},
				},
				response: &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: `"invalid"`}},
					}},
					StopReason: "end_turn",
				},
			}, nil
		},
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "second"}},
			}}), nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityBoundsOversizedUnaryUsageModel(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.ErrorContains(t, err, "token usage model exceeds 512 bytes")
		response, err = client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
				}},
				Usage: model.TokenUsage{
					Model:       strings.Repeat("provider-model", maxHookPayloadBytes),
					TotalTokens: 1,
				},
				StopReason: "end_turn",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.Less(t, len(encoded), maxHookPayloadBytes)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityBoundsOversizedStreamUsageModel(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := client.Stream(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		_, err = planner.ConsumeStream(ctx, stream)
		require.ErrorContains(t, err, "token usage model exceeds 512 bytes")
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.UsageChunk{Usage: model.TokenUsage{
						Model:            strings.Repeat("provider-model", maxHookPayloadBytes),
						InputTokens:      1,
						OutputTokens:     2,
						TotalTokens:      3,
						CacheReadTokens:  4,
						CacheWriteTokens: 5,
					}},
				},
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, model.TokenUsage{
		InputTokens:      1,
		OutputTokens:     2,
		TotalTokens:      3,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
	}, out.Usage)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.Less(t, len(encoded), maxHookPayloadBytes)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityAggregatesUsageAcrossManyRejectedInvocations(t *testing.T) {
	const probeCount = 128
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		for range probeCount {
			response, err := client.Complete(ctx, &model.Request{Model: "test"})
			require.NoError(t, err)
			require.NotNil(t, response)
		}
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			if providerCalls > probeCount {
				return &model.Response{}, nil
			}
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "probe"}},
				}},
				Usage:      model.TokenUsage{TotalTokens: 1},
				StopReason: "end_turn",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, probeCount, out.Usage.TotalTokens)
	require.Len(t, out.PlannerEvents, probeCount)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.Less(t, len(encoded), maxHookPayloadBytes)
	require.Equal(t, probeCount+1, providerCalls)
}

func TestPlanStartActivityPublishesAttributedUsagePerInvocationOnFailure(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{
			Model:      "small-model",
			ModelClass: model.ModelClassSmall,
		})
		require.NoError(t, err)
		require.NotNil(t, response)
		response, err = client.Complete(ctx, &model.Request{
			Model:      "large-model",
			ModelClass: model.ModelClassDefault,
		})
		require.Nil(t, response)
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			if providerCalls == 1 {
				return &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "probe"}},
					}},
					Usage: model.TokenUsage{
						Model:       "provider-small",
						TotalTokens: 3,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &model.Response{
				Usage: model.TokenUsage{
					Model:       "provider-large",
					TotalTokens: 7,
				},
				StopReason: "end_turn",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-attributed-usage",
		RunContext: run.Context{RunID: "run-attributed-usage"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, 10, out.Usage.TotalTokens)
	require.Len(t, out.PlannerEvents, 2)
	usage := make([]model.TokenUsage, 0, len(out.PlannerEvents))
	for _, record := range out.PlannerEvents {
		require.Equal(t, hooks.Usage, record.Type)
		var event hooks.UsageEvent
		require.NoError(t, json.Unmarshal(record.Payload, &event))
		usage = append(usage, event.TokenUsage)
	}
	require.Equal(t, []model.TokenUsage{
		{
			Model:       "provider-small",
			ModelClass:  model.ModelClassSmall,
			TotalTokens: 3,
		},
		{
			Model:       "provider-large",
			ModelClass:  model.ModelClassDefault,
			TotalTokens: 7,
		},
	}, usage)
	require.Equal(t, 2, providerCalls)
}

func TestPlanStartActivityRejectsUnaryUsageOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		require.NotNil(t, response)
		response, err = client.Complete(ctx, &model.Request{Model: "test"})
		require.Nil(t, response)
		require.ErrorContains(t, err, "total token usage exceeds the supported integer range")
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			tokens := 1
			if providerCalls == 1 {
				tokens = maxInt
			}
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
				}},
				Usage:      model.TokenUsage{TotalTokens: tokens},
				StopReason: "end_turn",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, maxInt, out.Usage.TotalTokens)
	require.Equal(t, planner.OutputContractOriginPlanner, out.OutputContractFailure.Origin)
	require.False(t, out.OutputContractFailure.ModelResponsePresent)
	require.Empty(t, out.OutputContractFailure.ModelResponseSHA256)
	require.Zero(t, out.OutputContractFailure.ModelResponseSize)
	require.Equal(t, 2, providerCalls)
}

func TestPlanStartActivityRejectsStreamUsageOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := client.Stream(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		_, err = planner.ConsumeStream(ctx, stream)
		require.ErrorContains(t, err, "total token usage exceeds the supported integer range")
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.UsageChunk{Usage: model.TokenUsage{TotalTokens: maxInt}},
					model.UsageChunk{Usage: model.TokenUsage{TotalTokens: 1}},
				},
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, maxInt, out.Usage.TotalTokens)
	require.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
	require.False(t, out.OutputContractFailure.ModelResponsePresent)
	require.Equal(t, 1, providerCalls)
}

func TestPlanStartActivityRequiresExactBytesForStreamReconciliation(t *testing.T) {
	spec := completion.Spec[string]{
		Name:   "normalized_completion",
		Schema: rawjson.Message(`{"type":"string"}`),
		Codec: tools.JSONCodec[string]{
			ToJSON: func(string) ([]byte, error) {
				return []byte(`"same value"`), nil
			},
			FromJSON: func([]byte) (string, error) {
				return "same value", nil
			},
		},
	}
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := completion.Stream(ctx, client, &model.Request{Model: "test"}, spec)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, stream.Close())
		}()

		for {
			if _, err = stream.Recv(); err != nil {
				return nil, err
			}
		}
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.CompletionChunk{Completion: model.Completion{
						Name:    "normalized_completion",
						Payload: []byte(`"stream encoding"`),
					}},
					model.StopChunk{Reason: "end_turn"},
				},
				response: &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: `"response encoding"`}},
					}},
					StopReason: "end_turn",
				},
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.NotNil(t, out)
	require.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
}

func TestValidatePlannerAdvertisedToolsUsesExecutableName(t *testing.T) {
	definitions := []*model.ToolDefinition{{Name: "visible"}}
	tests := []struct {
		name    string
		call    planner.ToolRequest
		wantErr string
	}{
		{
			name: "advertised tool",
			call: planner.ToolRequest{
				Name: "visible",
			},
		},
		{
			name: "unadvertised executable tool",
			call: planner.ToolRequest{
				Name: "hidden",
			},
			wantErr: `planner called tool "hidden" outside the advertised catalog`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePlannerAdvertisedTools(
				&planner.PlanResult{ToolCalls: []planner.ToolRequest{test.call}},
				definitions,
			)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestPlanStartActivityDoesNotPublishRejectedModelOutput(t *testing.T) {
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.PlannerModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{
			Model: "test",
			Tools: input.Agent.AdvertisedToolDefinitions(),
		})
		require.Nil(t, response)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		return nil, err
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID:    "call-hidden",
						Name:  "hidden",
						Input: rawjson.Message(`{}`),
					}},
				}},
				StopReason: "tool_use",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
	})

	requirePlannerOutputContractFailure(
		t,
		out,
		err,
	)
	require.Empty(t, recorder.events)
}

func TestPlanStartActivityReturnsEventsForWorkflowPublication(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.PlannerModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.NoError(t, err)
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "accepted"}},
				}},
				StopReason: "end_turn",
				Usage: model.TokenUsage{
					InputTokens:  1,
					OutputTokens: 1,
					TotalTokens:  2,
				},
			}, nil
		},
	})
	input := &PlanActivityInput{
		AgentID: "service.agent",
		RunID:   "run-123",
		RunContext: run.Context{
			RunID:  "run-123",
			TurnID: "turn-1",
		},
	}

	first, err := rt.PlanStartActivity(context.Background(), input)
	require.NoError(t, err)
	second, err := rt.PlanStartActivity(context.Background(), input)
	require.NoError(t, err)

	require.Equal(t, 2, providerCalls)
	require.Len(t, first.PlannerEvents, 2)
	require.Len(t, second.PlannerEvents, 2)
	require.Empty(t, recorder.events)

	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		workflowID:  "workflow-1",
		hookRuntime: rt,
	}
	batch, err := preparePlannerPublicationBatch(wfCtx, *input, first)
	require.NoError(t, err)
	require.NoError(t, rt.publishPlannerPublicationBatch(wfCtx, batch))
	require.Len(t, recorder.events, 2)
}

func TestRunPlanActivityPublishesUsageAndRejectedResponseBeforeOutputContractFailure(t *testing.T) {
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		_, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Usage: model.TokenUsage{
					InputTokens:  4,
					OutputTokens: 1,
					TotalTokens:  5,
				},
			}, nil
		},
	})
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
	}

	out, err := rt.runPlanActivity(wfCtx, "plan", engine.ActivityOptions{}, input, time.Time{})

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
	require.Len(t, recorder.events, 2)
	require.Equal(t, hooks.Usage, recorder.events[0].Type())
	rejected, ok := recorder.events[1].(*hooks.ModelOutputRejectedEvent)
	require.True(t, ok)
	require.Equal(t, out.OutputContractFailure.ReasonSHA256, rejected.ReasonSHA256)
	require.Equal(t, out.OutputContractFailure.ReasonSize, rejected.ReasonSize)
	require.Equal(
		t,
		api.ModelResponseFingerprintVersionV1,
		rejected.ModelResponseFingerprintVersion,
	)
	require.True(t, rejected.ModelResponsePresent)
	require.Len(t, rejected.ModelResponseSHA256, 64)
	require.Positive(t, rejected.ModelResponseSize)
}

func TestRunPlanActivityDoesNotPublishModelRejectionForPlannerFailure(t *testing.T) {
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}

	out, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-123",
			RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
		},
		time.Time{},
	)

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(
		t,
		planner.OutputContractOriginPlanner,
		out.OutputContractFailure.Origin,
	)
	require.Empty(t, out.OutputContractFailure.ModelResponseFingerprintVersion)
	require.Len(t, recorder.events, 1)
	rejected, ok := recorder.events[0].(*hooks.PlannerOutputRejectedEvent)
	require.True(t, ok)
	require.Equal(t, out.OutputContractFailure.ReasonSHA256, rejected.ReasonSHA256)
	require.Equal(t, out.OutputContractFailure.ReasonSize, rejected.ReasonSize)
}

func TestRunPlanActivityPublishesEmptyPlannerRejectionReason(t *testing.T) {
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return nil, planner.NewOutputContractError(errors.New(""))
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}

	out, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-123",
			RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
		},
		time.Time{},
	)

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, int64(0), out.OutputContractFailure.ReasonSize)
	require.Equal(
		t,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		out.OutputContractFailure.ReasonSHA256,
	)
	require.Len(t, recorder.events, 1)
	rejected, ok := recorder.events[0].(*hooks.PlannerOutputRejectedEvent)
	require.True(t, ok)
	require.Equal(t, out.OutputContractFailure.ReasonSHA256, rejected.ReasonSHA256)
	require.Equal(t, out.OutputContractFailure.ReasonSize, rejected.ReasonSize)
}

func TestRunPlanActivityPublishesTypedModelRejectionReason(t *testing.T) {
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		request := &model.Request{
			Model:            "test",
			StructuredOutput: &model.StructuredOutput{Name: "answer"},
		}
		require.NoError(t, model.SetCompletionValidator(
			request,
			func(*model.Response, *model.Completion) error {
				return errors.New("")
			},
		))
		response, err := client.Complete(ctx, request)
		require.Nil(t, response)
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "answer"}},
			}}), nil
		},
	})
	recorder := &recordingHooks{}
	rt.Bus = recorder
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}

	out, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-123",
			RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
		},
		time.Time{},
	)

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
	require.Positive(t, out.OutputContractFailure.ReasonSize)
	require.Len(t, out.OutputContractFailure.ReasonSHA256, 64)
	require.Len(t, recorder.events, 1)
	rejected, ok := recorder.events[0].(*hooks.ModelOutputRejectedEvent)
	require.True(t, ok)
	require.Equal(t, out.OutputContractFailure.ReasonSHA256, rejected.ReasonSHA256)
	require.Equal(t, out.OutputContractFailure.ReasonSize, rejected.ReasonSize)
}

func TestRunPlanActivityRetriesPublicationBeforeOutputFailure(t *testing.T) {
	providerCalls := 0
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		_, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	store := &replayPublicationStore{
		failCall: 2,
		stored:   make(map[string]*runlog.Event),
	}
	rt.RunEventStore = store
	rt.Bus = noopHooks{}
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{}, nil
		},
	})
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}

	out, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-123",
			RunContext: run.Context{RunID: "run-123", TurnID: "turn-1"},
		},
		time.Time{},
	)

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, out.OutputContractFailure.Origin)
	require.Equal(t, 1, providerCalls)
	require.Equal(t, 1, store.storedCount())
}

func TestRunPlanActivityBoundsOversizedRejectedResponseFingerprint(t *testing.T) {
	rejectedText := strings.Repeat("x", 2*maxHookPayloadBytes)
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		_, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Error(t, err)
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: rejectedText}},
				}},
			}, nil
		},
	})
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-oversized",
		RunContext: run.Context{RunID: "run-oversized", TurnID: "turn-1"},
	}

	out, err := rt.runPlanActivity(wfCtx, "plan", engine.ActivityOptions{}, input, time.Time{})

	require.NotNil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	encodedOutput, marshalErr := json.Marshal(out)
	require.NoError(t, marshalErr)
	require.Less(t, len(encodedOutput), maxHookPayloadBytes)
	require.Len(t, out.OutputContractFailure.ModelResponseSHA256, 64)
	require.Greater(t, out.OutputContractFailure.ModelResponseSize, int64(maxHookPayloadBytes))
	require.Len(t, recorder.events, 1)
	rejected, ok := recorder.events[0].(*hooks.ModelOutputRejectedEvent)
	require.True(t, ok)
	require.Equal(t, out.OutputContractFailure.ModelResponseSHA256, rejected.ModelResponseSHA256)
	require.Equal(t, out.OutputContractFailure.ModelResponseSize, rejected.ModelResponseSize)
}

func TestPlanStartActivityRejectsOverBudgetRawToolPayloadWithoutFingerprint(t *testing.T) {
	toolCallID := strings.Repeat("provider-controlled-id", maxHookPayloadBytes)
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		response, err := client.Complete(ctx, &model.Request{Model: "test"})
		require.Error(t, err)
		require.Nil(t, response)
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "hidden fallback"}},
				},
			},
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID:    toolCallID,
						Name:  "service.lookup",
						Input: rawjson.Message(`{`),
					}},
				}},
				StopReason: "tool_use",
			}, nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Empty(t, out.OutputContractFailure.ModelResponseSHA256)
	require.Zero(t, out.OutputContractFailure.ModelResponseSize)
	encodedOutput, err := json.Marshal(out)
	require.NoError(t, err)
	require.Less(t, len(encodedOutput), maxHookPayloadBytes)
}

func TestPlanStartActivityFingerprintsCompleteResponseBeforeCloneValidation(t *testing.T) {
	type unsupportedMetadata struct {
		Value string
	}
	type uncopyableStructMetadata struct {
		value string
	}
	tests := []struct {
		name               string
		response           *model.Response
		expectsFingerprint bool
	}{
		{
			name:               "nil part",
			expectsFingerprint: true,
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{nil},
				}},
				StopReason: "end_turn",
			},
		},
		{
			name:               "uncopyable struct metadata",
			expectsFingerprint: true,
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
					Meta: map[string]any{
						"unsupported": uncopyableStructMetadata{value: "value"},
					},
				}},
				StopReason: "end_turn",
			},
		},
		{
			name: "uncopyable metadata",
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
					Meta: map[string]any{
						"unsupported": &unsupportedMetadata{Value: "value"},
					},
				}},
				StopReason: "end_turn",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerCalls := 0
			pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := input.Agent.ModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{Model: "test"})
				require.Nil(t, response)
				require.Error(t, err)
				return finalPlannerResult("planner tried to continue"), nil
			}}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			rt.models["test"] = mustTestModelClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					providerCalls++
					return test.response, nil
				},
			})

			out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "run-123",
				RunContext: run.Context{RunID: "run-123"},
			})

			requirePlannerOutputContractFailure(t, out, err)
			require.Truef(
				t,
				out.OutputContractFailure.ModelResponsePresent,
				"failure: %#v",
				out.OutputContractFailure,
			)
			if test.expectsFingerprint {
				require.Len(t, out.OutputContractFailure.ModelResponseSHA256, 64)
				require.Positive(t, out.OutputContractFailure.ModelResponseSize)
			} else {
				require.Empty(t, out.OutputContractFailure.ModelResponseSHA256)
				require.Zero(t, out.OutputContractFailure.ModelResponseSize)
			}
			require.Equal(t, 1, providerCalls)
		})
	}
}

func TestPlanStartActivityFingerprintsStreamResponseBeforeCloneValidation(t *testing.T) {
	type unsupportedMetadata struct {
		Value string
	}
	type uncopyableStructMetadata struct {
		value string
	}
	tests := []struct {
		name               string
		response           *model.Response
		expectsFingerprint bool
	}{
		{
			name:               "nil part",
			expectsFingerprint: true,
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{nil},
				}},
				StopReason: "end_turn",
			},
		},
		{
			name:               "uncopyable struct metadata",
			expectsFingerprint: true,
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
					Meta: map[string]any{
						"unsupported": uncopyableStructMetadata{value: "value"},
					},
				}},
				StopReason: "end_turn",
			},
		},
		{
			name: "uncopyable metadata",
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer"}},
					Meta: map[string]any{
						"unsupported": &unsupportedMetadata{Value: "value"},
					},
				}},
				StopReason: "end_turn",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerCalls := 0
			pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := input.Agent.ModelClient("test")
				require.True(t, ok)
				stream, err := client.Stream(ctx, &model.Request{Model: "test"})
				require.NoError(t, err)
				_, err = planner.ConsumeStream(ctx, stream)
				require.Error(t, err)
				return finalPlannerResult("planner tried to continue"), nil
			}}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			rt.models["test"] = mustTestModelClient(stubModelClient{
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					providerCalls++
					return &chunkStreamer{
						chunks:   []model.Chunk{model.StopChunk{Reason: "end_turn"}},
						response: test.response,
					}, nil
				},
			})

			out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "run-123",
				RunContext: run.Context{RunID: "run-123"},
			})

			requirePlannerOutputContractFailure(t, out, err)
			require.Truef(
				t,
				out.OutputContractFailure.ModelResponsePresent,
				"failure: %#v",
				out.OutputContractFailure,
			)
			if test.expectsFingerprint {
				require.Len(t, out.OutputContractFailure.ModelResponseSHA256, 64)
				require.Positive(t, out.OutputContractFailure.ModelResponseSize)
			} else {
				require.Empty(t, out.OutputContractFailure.ModelResponseSHA256)
				require.Zero(t, out.OutputContractFailure.ModelResponseSize)
			}
			require.Equal(t, 1, providerCalls)
		})
	}
}

func TestPlanStartActivityAdvertisesHistoricalContinuation(t *testing.T) {
	search, continuation := continuationTestSpecs()
	actionName := continuationActionName(continuation.Name, "source-1")
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		definitions := input.Agent.AdvertisedToolDefinitions()
		require.Len(t, definitions, 2)
		require.Equal(t, actionName.String(), definitions[1].Name)
		require.Equal(
			t,
			`Continue the unfinished tools.search query with original input {"query":"alarms"}. The latest page returned 1 items.`,
			definitions[1].Description,
		)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name:    actionName,
			Payload: rawjson.Message(`{}`),
		}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	seedTestToolSpecs(rt, search, continuation)
	rt.agentToolSpecs = make(map[agent.Ident][]tools.ToolSpec)
	rt.agentToolSpecs["service.agent"] = []tools.ToolSpec{search, continuation}
	store := runloginmem.New()
	rt.RunEventStore = store
	_, err := rt.SessionStore.CreateSession(t.Context(), "session-1", time.Now().UTC())
	require.NoError(t, err)

	appendHistoricalHookEvent(t, store, hooks.NewToolCallScheduledEvent(
		"run-1",
		"service.agent",
		"session-1",
		search.Name,
		"source-1",
		rawjson.Message(`{"query":"alarms"}`),
		"",
		"",
		0,
	), "source-call", 1)
	cursor := "opaque-next"
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
		"run-1",
		"service.agent",
		"session-1",
		"run-1",
		search.Name,
		"source-1",
		"",
		rawjson.Message(`{"items":["page-1"]}`),
		len(`{"items":["page-1"]}`),
		false,
		"",
		nil,
		"page 1",
		&agent.Bounds{Returned: 1, Truncated: true, NextCursor: &cursor},
		time.Second,
		nil,
		nil,
	), "source-result", 2)

	input := &PlanActivityInput{
		AgentID: "service.agent",
		RunID:   "run-2",
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Search alarms."}}},
			{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{
				ID: "source-1", Name: search.Name.String(), Input: rawjson.Message(`{"query":"alarms"}`),
			}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
				ToolUseID: "source-1", Content: map[string]any{"items": []any{"page-1"}},
			}}},
			{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "First page."}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Show the next page."}}},
		},
		RunContext: run.Context{
			RunID:     "run-2",
			SessionID: "session-1",
		},
	}

	out, err := rt.PlanStartActivity(t.Context(), input)

	require.NoError(t, err)
	require.Len(t, out.Result.ToolCalls, 1)
	require.Equal(t, continuation.Name, out.Result.ToolCalls[0].Name)
	require.Equal(t, actionName, out.Result.ToolCalls[0].ModelName)
	require.Equal(t, "source-1", out.Result.ToolCalls[0].ContinuationRootToolCallID)
	require.JSONEq(t, `{"cursor":"opaque-next"}`, string(out.Result.ToolCalls[0].Payload))
}

func TestPlanStartActivityReturnsNativeProviderError(t *testing.T) {
	providerErr := model.NewProviderError(
		"anthropic",
		"count_tokens",
		400,
		model.ProviderErrorKindInvalidRequest,
		"",
		"invalid tool ID",
		"req-1",
		false,
		nil,
	)
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return nil, providerErr
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		Messages:   []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
		RunContext: run.Context{RunID: "run-123"},
	})

	require.Nil(t, out)
	require.ErrorIs(t, err, providerErr)
}

func TestPlanStartActivityCancelsAndJoinsPendingUnaryInvocation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		go func() {
			_, _ = client.Complete(ctx, &model.Request{Model: "pending-unary"})
		}()
		<-started
		return finalPlannerResult("planner returned early"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(ctx context.Context, _ *model.Request) (*model.Response, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-pending-unary",
		RunContext: run.Context{RunID: "run-pending-unary"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, planner.OutputContractOriginPlanner, out.OutputContractFailure.Origin)
	select {
	case <-finished:
	default:
		t.Fatal("unary provider work continued after activity output")
	}
}

func TestPlanStartActivityCancelsClosesAndJoinsPendingStream(t *testing.T) {
	returned := make(chan struct{})
	closed := make(chan struct{})
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		_, err := client.Stream(context.Background(), &model.Request{Model: "pending-stream"})
		require.NoError(t, err)
		close(returned)
		return finalPlannerResult("planner returned early"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(ctx context.Context, _ *model.Request) (model.Streamer, error) {
			return &cancellationObservingStreamer{ctx: ctx, closed: closed}, nil
		},
	})

	out, err := rt.PlanStartActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-pending-stream",
		RunContext: run.Context{RunID: "run-pending-stream"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, planner.OutputContractOriginPlanner, out.OutputContractFailure.Origin)
	select {
	case <-returned:
	default:
		t.Fatal("planner did not receive the stream")
	}
	select {
	case <-closed:
	default:
		t.Fatal("stream provider work continued after activity output")
	}
}

func TestPlanStartActivityPersistsPlannerOriginForPrematureStreamClose(t *testing.T) {
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := client.Stream(ctx, &model.Request{Model: "closed-stream"})
		require.NoError(t, err)
		require.ErrorContains(t, stream.Close(), "planner closed model stream before EOF")
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{}, nil
		},
	})

	out, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-closed-stream",
		RunContext: run.Context{RunID: "run-closed-stream"},
	})

	requirePlannerOutputContractFailure(t, out, err)
	require.Equal(t, planner.OutputContractOriginPlanner, out.OutputContractFailure.Origin)
}

func TestPlanActivitiesReturnPlannerOutputFailureEvidence(t *testing.T) {
	contractErr := planner.NewOutputContractError(errors.New("missing required citation"))
	tests := []struct {
		name    string
		planner *stubPlanner
		run     func(*Runtime) (*PlanActivityOutput, error)
	}{
		{
			name: "start",
			planner: &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return nil, contractErr
				},
			},
			run: func(rt *Runtime) (*PlanActivityOutput, error) {
				return rt.PlanStartActivity(context.Background(), &PlanActivityInput{
					AgentID:    "service.agent",
					RunID:      "run-start",
					Messages:   []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
					RunContext: run.Context{RunID: "run-start"},
				})
			},
		},
		{
			name: "resume",
			planner: &stubPlanner{
				resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return nil, contractErr
				},
			},
			run: func(rt *Runtime) (*PlanActivityOutput, error) {
				return rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
					AgentID:    "service.agent",
					RunID:      "run-resume",
					Messages:   []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
					RunContext: run.Context{RunID: "run-resume"},
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", test.planner)

			out, err := test.run(rt)

			requirePlannerOutputContractFailure(t, out, err)
		})
	}
}

func TestPlanStartActivityAdvertisesPolicyFilteredTools(t *testing.T) {
	called := false
	pl := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			called = true
			definitions := input.Agent.AdvertisedToolDefinitions()
			require.Len(t, definitions, 1)
			require.Equal(t, "svc.tools.visible", definitions[0].Name)
			require.Equal(t, "Visible tool", definitions[0].Description)
			require.JSONEq(t, `{"type":"object","properties":{"q":{"type":"string"}}}`, string(definitions[0].Input.Contract().Schema))
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  "assistant",
						Parts: []model.Part{model.TextPart{Text: "ok"}},
					},
				},
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	visible := newAnyJSONSpec("svc.tools.visible", "svc.tools")
	visible.Description = "Visible tool"
	visible.Payload.Schema = tools.RawJSON(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	visible.Tags = []string{"system", "profile"}
	blocked := newAnyJSONSpec("svc.tools.blocked", "svc.tools")
	blocked.Tags = []string{"system"}
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {visible, blocked},
	}
	input := PlanActivityInput{
		AgentID:  "service.agent",
		RunID:    "run-123",
		Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
		RunContext: run.Context{
			RunID: "run-123",
		},
		Policy: &PolicyOverrides{
			TagClauses: []TagPolicyClause{{AllowedAny: []string{"profile"}}},
		},
	}
	out, err := rt.PlanStartActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlannerBoundaryOmitsToolResultsField(t *testing.T) {
	t.Parallel()

	planResumeInputType := reflect.TypeOf(planner.PlanResumeInput{})
	_, hasPlannerToolResults := planResumeInputType.FieldByName("ToolResults")
	require.False(t, hasPlannerToolResults, "PlanResumeInput must expose ToolOutputs as its only execution-history field")

	planActivityInputType := reflect.TypeOf(PlanActivityInput{})
	_, hasActivityToolResults := planActivityInputType.FieldByName("ToolResults")
	require.False(t, hasActivityToolResults, "PlanActivityInput must expose ToolOutputs as its only execution-history field")
}

func TestPlanResumeActivityPassesToolOutputs(t *testing.T) {
	called := false
	toolName := tools.Ident("svc.ts.tool")
	resultJSON := rawjson.Message([]byte(`{"status":"ok"}`))
	serverData := rawjson.Message([]byte(`[{"kind":"evidence"}]`))
	total := 17
	bounds := &agent.Bounds{Returned: 10, Total: &total, Truncated: true, RefinementHint: "narrow the window"}
	toolOutputs := []*api.ToolOutputRef{{CallRunID: "run-123", ResultRunID: "run-123", ToolCallID: "call-1"}}
	pl := &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.True(t, input.SynthesisOnly)
		require.Len(t, input.ToolOutputs, 1)
		require.Equal(t, toolName, input.ToolOutputs[0].Name)
		require.Equal(t, "call-1", input.ToolOutputs[0].ToolCallID)
		require.JSONEq(t, `{"from":"test"}`, string(input.ToolOutputs[0].Payload))
		require.JSONEq(t, `{"status":"ok"}`, string(input.ToolOutputs[0].Result))
		require.JSONEq(t, `[{"kind":"evidence"}]`, string(input.ToolOutputs[0].ServerData))
		require.Equal(t, len(resultJSON), input.ToolOutputs[0].ResultBytes)
		require.NotNil(t, input.ToolOutputs[0].Bounds)
		require.True(t, input.ToolOutputs[0].Bounds.Truncated)
		require.Equal(t, "narrow the window", input.ToolOutputs[0].Bounds.RefinementHint)
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	seedTestToolSpecs(rt, newAnyJSONSpec(toolName, "svc.tools"))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			toolName,
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolResultReceivedEvent(
			"run-123",
			"service.agent",
			"",
			"run-123",
			toolName,
			"call-1",
			"",
			resultJSON,
			len(resultJSON),
			false,
			"",
			serverData,
			"preview",
			bounds,
			50*time.Millisecond,
			nil,
			nil,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID:       "service.agent",
		RunID:         "run-123",
		RunContext:    run.Context{RunID: "run-123", Attempt: 3},
		ToolOutputs:   toolOutputs,
		SynthesisOnly: true,
	}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlanResumeActivityAdvancesEmptyContinuationBeforePlanner(t *testing.T) {
	const nextCursorValue = "next-page"

	plannerCalled := false
	pl := &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
		plannerCalled = true
		return nil, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	search, continuation := continuationTestSpecs()
	seedTestToolSpecs(rt, search, continuation)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {search, continuation},
	}
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			search.Name,
			"source-1",
			rawjson.Message(`{"query":"alarms"}`),
			"queue",
			"",
			0,
		),
		"",
	))
	nextCursor := nextCursorValue
	resultJSON := rawjson.Message(`{"items":[]}`)
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolResultReceivedEvent(
			"run-123",
			"service.agent",
			"",
			"run-123",
			search.Name,
			"source-1",
			"",
			resultJSON,
			len(resultJSON),
			false,
			"",
			nil,
			"",
			&agent.Bounds{Returned: 0, Truncated: true, NextCursor: &nextCursor},
			0,
			nil,
			nil,
		),
		"",
	))

	out, err := rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
		ToolOutputs: []*api.ToolOutputRef{{
			CallRunID:   "run-123",
			ResultRunID: "run-123",
			ToolCallID:  "source-1",
		}},
	})
	require.NoError(t, err)
	require.False(t, plannerCalled)
	require.Len(t, out.Result.ToolCalls, 1)
	require.Equal(t, continuation.Name, out.Result.ToolCalls[0].Name)
	require.Equal(t, "source-1", out.Result.ToolCalls[0].ContinuationRootToolCallID)
	require.JSONEq(t, `{"cursor":"`+nextCursorValue+`"}`, string(out.Result.ToolCalls[0].Payload))
}

func TestPlanResumeActivityAdvertisesOnlyRestrictedCorrectionTool(t *testing.T) {
	first := newAnyJSONSpec("svc.tools.first", "svc.tools")
	wrong := newAnyJSONSpec("svc.tools.wrong", "svc.tools")
	second := newAnyJSONSpec("svc.tools.second", "svc.tools")
	bookkeeping := newBookkeepingSpec("svc.tools.progress")
	specs := []tools.ToolSpec{first, wrong, second, bookkeeping}

	pl := &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		definitions := input.Agent.AdvertisedToolDefinitions()
		require.Len(t, definitions, 1)
		require.Equal(t, first.Name.String(), definitions[0].Name)
		require.Len(t, input.ToolOutputs, 2)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name: first.Name, Payload: rawjson.Message(`{"valid":"first"}`),
		}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	seedTestToolSpecs(rt, specs...)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"service.agent": specs}

	for _, failed := range []struct {
		spec   tools.ToolSpec
		callID string
	}{
		{spec: first, callID: "call-1"},
		{spec: second, callID: "call-2"},
	} {
		require.NoError(t, rt.publishHookErr(
			context.Background(),
			hooks.NewToolCallScheduledEvent(
				"run-123",
				"service.agent",
				"",
				failed.spec.Name,
				failed.callID,
				rawjson.Message(`{"invalid":true}`),
				"queue",
				"",
				0,
			),
			"",
		))
		require.NoError(t, rt.publishHookErr(
			context.Background(),
			hooks.NewToolResultReceivedEvent(
				"run-123",
				"service.agent",
				"",
				"run-123",
				failed.spec.Name,
				failed.callID,
				"",
				nil,
				0,
				false,
				"",
				nil,
				"",
				nil,
				0,
				nil,
				testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "invalid call"),
			),
			"",
		))
	}

	out, err := rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123", Attempt: 2},
		Policy:     &PolicyOverrides{RestrictToTool: first.Name},
		ToolOutputs: []*api.ToolOutputRef{
			{CallRunID: "run-123", ResultRunID: "run-123", ToolCallID: "call-1"},
			{CallRunID: "run-123", ResultRunID: "run-123", ToolCallID: "call-2"},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Result.ToolCalls, 1)
}

func TestPlanResumeActivityEnforcesSynthesisOnly(t *testing.T) {
	pl := &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{
			ToolCalls: []planner.ToolRequest{{
				Name:            "svc.other.tool",
				ModelToolCallID: "other-1",
				Payload:         rawjson.Message(`{}`),
			}},
		}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)

	out, err := rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
		AgentID:       "service.agent",
		RunID:         "run-123",
		RunContext:    run.Context{RunID: "run-123", Attempt: 3},
		SynthesisOnly: true,
	})

	requirePlannerOutputContractFailure(t, out, err)
}

func TestPlanResumeActivityFailsWhenCanonicalToolResultIsMissing(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.ts.tool", "svc.tools"))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			"svc.ts.tool",
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID: "service.agent",
		RunID:   "run-123",
		RunContext: run.Context{
			RunID:   "run-123",
			Attempt: 1,
		},
		ToolOutputs: []*api.ToolOutputRef{{CallRunID: "run-123", ResultRunID: "run-123", ToolCallID: "call-1"}},
	}

	_, err := rt.PlanResumeActivity(context.Background(), &input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing canonical tool result in run log")
}

func TestPlanResumeActivityHydratesOmittedResultMetadataFromCanonicalRunlog(t *testing.T) {
	called := false
	pl := &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.Len(t, input.ToolOutputs, 1)
		require.Equal(t, "call-1", input.ToolOutputs[0].ToolCallID)
		require.True(t, input.ToolOutputs[0].ResultOmitted)
		require.Equal(t, "workflow_budget", input.ToolOutputs[0].ResultOmittedReason)
		require.Equal(t, 12345, input.ToolOutputs[0].ResultBytes)
		require.Nil(t, input.ToolOutputs[0].Result)
		require.JSONEq(t, `[{"kind":"evidence"}]`, string(input.ToolOutputs[0].ServerData))
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name:            "svc.other.tool",
			ModelToolCallID: "other-1",
			Payload:         rawjson.Message(`{}`),
		}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	source := newAnyJSONSpec("svc.ts.tool", "svc.tools")
	other := newAnyJSONSpec("svc.other.tool", "svc.tools")
	seedTestToolSpecs(rt, source, other)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"service.agent": {source, other}}
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			"svc.ts.tool",
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolResultReceivedEvent(
			"run-123",
			"service.agent",
			"",
			"run-123",
			"svc.ts.tool",
			"call-1",
			"",
			nil,
			12345,
			true,
			"workflow_budget",
			rawjson.Message([]byte(`[{"kind":"evidence"}]`)),
			"preview",
			nil,
			0,
			nil,
			nil,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID:     "service.agent",
		RunID:       "run-123",
		RunContext:  run.Context{RunID: "run-123", Attempt: 2},
		ToolOutputs: []*api.ToolOutputRef{{CallRunID: "run-123", ResultRunID: "run-123", ToolCallID: "call-1"}},
	}

	_, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
}

func TestBuildPlannerToolOutputRecordsPreservesOmittedResultMetadata(t *testing.T) {
	t.Parallel()
	const runID = "run-123"

	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.ts.tool", "svc.tools"))

	records := stepToolRecordsForTest(
		t,
		[]ToolCall{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Payload:    rawjson.Message([]byte(`{"from":"test"}`)),
			},
		},
		[]*planner.ToolResult{
			{
				Name:                "svc.ts.tool",
				ToolCallID:          "call-1",
				ResultOmitted:       true,
				ResultOmittedReason: "workflow_budget",
				ResultBytes:         12345,
				ServerData:          rawjson.Message([]byte(`[{"kind":"evidence"}]`)),
			},
		},
	)
	records[0].callRunID = runID
	records[0].resultRunID = runID
	outputs, err := rt.buildPlannerToolOutputRecords(context.Background(), records)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.True(t, outputs[0].ResultOmitted)
	require.Equal(t, "workflow_budget", outputs[0].ResultOmittedReason)
	require.Equal(t, 12345, outputs[0].ResultBytes)
	require.Empty(t, outputs[0].Result)
	require.JSONEq(t, `[{"kind":"evidence"}]`, string(outputs[0].ServerData))
}

func TestBuildPlannerToolOutputRecordsSkipsBookkeepingResults(t *testing.T) {
	t.Parallel()
	const runID = "run-123"

	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.ts.tool", "svc.tools"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.set_step_status", "tasks.progress")
			spec.Bookkeeping = true
			return spec
		}(),
	)

	records := stepToolRecordsForTest(
		t,
		[]ToolCall{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Payload:    rawjson.Message([]byte(`{"from":"test"}`)),
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Payload:    rawjson.Message([]byte(`{"step":"verify"}`)),
			},
		},
		[]*planner.ToolResult{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Result:     map[string]any{"status": "ok"},
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Result:     map[string]any{"ok": true},
			},
		},
	)
	for i := range records {
		records[i].callRunID = runID
		records[i].resultRunID = runID
	}
	outputs, err := rt.buildPlannerToolOutputRecords(context.Background(), records)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, "call-1", outputs[0].ToolCallID)
	require.Equal(t, tools.Ident("svc.ts.tool"), outputs[0].Name)
}

func TestBuildNextResumeRequestCarriesTurnScopedRecoveryIdentity(t *testing.T) {
	t.Parallel()

	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-123"}}
	nextAttempt := 1
	recovery := []*planner.ToolOutput{{
		Name:       "svc.tools.rejected",
		ToolCallID: "call-rejected",
		Failure: testToolFailure(
			planner.FailureDomainRejection,
			planner.RecoveryReplan,
			"use another capability",
		),
	}}

	req, err := (&Runtime{}).buildNextResumeRequest(
		"service.agent",
		base,
		&PolicyOverrides{RestrictToTool: "svc.tools.first"},
		nil,
		recovery,
		false,
		&nextAttempt,
	)
	require.NoError(t, err)
	require.Equal(t, tools.Ident("svc.tools.first"), req.Policy.RestrictToTool)
	require.Equal(t, []string{"call-rejected"}, req.RecoveryToolCallIDs)

	recovery[0].ToolCallID = "mutated"
	require.Equal(t, []string{"call-rejected"}, req.RecoveryToolCallIDs)
}

func TestBuildNextResumeRequestRejectsNilToolOutputEntry(t *testing.T) {
	t.Parallel()

	rt := &Runtime{}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-123",
			SessionID: "sess-1",
		},
	}
	nextAttempt := 1

	_, err := rt.buildNextResumeRequest(
		"svc.agent",
		base,
		nil,
		[]*planner.ToolOutput{nil},
		nil,
		false,
		&nextAttempt,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil tool output")
}

func TestBuildNextResumeRequestUsesProviderNeutralTranscriptValidation(t *testing.T) {
	t.Parallel()

	rt := &Runtime{}
	base := &planner.PlanInput{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "call-1", Name: "svc.tool"},
			},
		}},
		RunContext: run.Context{
			RunID:     "run-123",
			SessionID: "sess-1",
		},
	}
	nextAttempt := 1

	_, err := rt.buildNextResumeRequest(
		"svc.agent",
		base,
		nil,
		nil,
		nil,
		false,
		&nextAttempt,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid resume transcript")
	require.NotContains(t, err.Error(), "Bedrock")
}

func TestPlanResumeActivityRejectsEmptyRawJSONPayloads(t *testing.T) {
	pl := &stubPlanner{
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{
					{
						ModelToolCallID: "tool-call",
						Name:            "svc.other.tool",
						Payload:         rawjson.Message([]byte{}),
					},
				},
				Await: planner.NewAwait(
					planner.AwaitQuestionsItem(&planner.AwaitQuestions{
						ID:         "await-q",
						ToolName:   "chat.ask_question.ask_question",
						ToolCallID: "call-q",
						Payload:    rawjson.Message([]byte{}),
					}),
					planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
						ID: "await-ext",
						Items: []planner.AwaitToolItem{
							{
								Name:       "external.one",
								ToolCallID: "call-ext",
								Payload:    rawjson.Message([]byte{}),
							},
						},
					}),
				),
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	requirePlannerOutputContractFailure(t, out, err)
}

func TestPlanResumeActivityRejectsMissingFinalResponseMessage(t *testing.T) {
	pl := &stubPlanner{
		resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{}}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)

	out, err := rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(t, out, err)
}

func TestPlanResumeActivityRejectsPlannerAuthoredToolUseInFinalResponse(t *testing.T) {
	pl := &stubPlanner{
		resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{Message: &model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID:    "call-1",
						Name:  "svc.lookup",
						Input: rawjson.Message(`{}`),
					}},
				}},
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)

	out, err := rt.PlanResumeActivity(context.Background(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	})

	requirePlannerOutputContractFailure(
		t,
		out,
		err,
	)
}

// requirePlannerOutputContractFailure verifies that the activity returns the
// bounded failure identity the workflow must publish before it stops the run.
func requirePlannerOutputContractFailure(
	t *testing.T,
	out *PlanActivityOutput,
	err error,
) {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Result)
	require.NotNil(t, out.OutputContractFailure)
	require.Len(t, out.OutputContractFailure.ReasonSHA256, 64)
	require.Positive(t, out.OutputContractFailure.ReasonSize)
	require.Contains(t, []planner.OutputContractOrigin{
		planner.OutputContractOriginModel,
		planner.OutputContractOriginPlanner,
	}, out.OutputContractFailure.Origin)
	if out.OutputContractFailure.ModelResponseSHA256 == "" {
		require.Empty(t, out.OutputContractFailure.ModelResponseFingerprintVersion)
	} else {
		require.Equal(
			t,
			api.ModelResponseFingerprintVersionV1,
			out.OutputContractFailure.ModelResponseFingerprintVersion,
		)
	}
}
