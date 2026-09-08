package runtime

// These tests use planner activities and synthetic providers to prove that
// history preparation is demand-driven, shared only within one activity, and
// cannot be turned into model-output recovery by a planner that ignores errors.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// cancelingHistoryProvider blocks at the selected provider operation until
	// the planner activity is canceled. All responses and counts are synthetic.
	cancelingHistoryProvider struct {
		historyCountingClient
		cancelSummary bool
		started       chan struct{}
		blockedCalls  int
	}
)

const (
	historyPhaseStart  = "start"
	historyPhaseResume = "resume"
)

func TestPlannerHistoryPreparesOnceAndSharesValue(t *testing.T) {
	for _, policyErr := range []error{nil, errors.New("summary provider failed")} {
		name := "prepared messages"
		if policyErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			messages := []*model.Message{userMsg("original")}
			prepared := []*model.Message{userMsg("prepared")}
			definitions := []*model.ToolDefinition{{Name: "lookup"}}
			var calls atomic.Int32
			reg := AgentRegistration{Definition: testRegistrationDefinition("service.agent", engine.WorkflowDefinition{}, nil)}
			reg.Policy.History = func(actualCtx context.Context, actualMessages []*model.Message, actualDefinitions []*model.ToolDefinition) ([]*model.Message, error) {
				calls.Add(1)
				assert.Same(t, ctx, actualCtx)
				assert.Same(t, messages[0], actualMessages[0])
				assert.Same(t, definitions[0], actualDefinitions[0])
				return prepared, policyErr
			}
			rt := &Runtime{}
			history := rt.newPlannerHistory(ctx, &reg, messages, definitions)
			assert.Zero(t, calls.Load())
			require.NoError(t, history.preparationError())
			assert.Zero(t, calls.Load(), "inspecting errors must not prepare history")

			const readers = 16
			results := make([][]*model.Message, readers)
			errs := make([]error, readers)
			var readersDone sync.WaitGroup
			for i := range readers {
				readersDone.Go(func() {
					results[i], errs[i] = history.prepare()
				})
			}
			readersDone.Wait()
			assert.EqualValues(t, 1, calls.Load())
			for i := range readers {
				if policyErr != nil {
					require.ErrorIs(t, errs[i], policyErr)
					assert.Same(t, errs[0], errs[i])
					assert.Nil(t, results[i])
				} else {
					require.NoError(t, errs[i])
					assert.Same(t, &prepared[0], &results[i][0])
					assert.Same(t, prepared[0], results[i][0])
				}
			}
			assert.Equal(t, errs[0], history.preparationError())
			if policyErr == nil {
				results[0][0].Parts[0] = model.TextPart{Text: "planner edit"}
				again, err := history.prepare()
				require.NoError(t, err)
				assert.Equal(t, "planner edit", textPart(t, again[0]))
			}
		})
	}
}

func TestPlannerHistoryPreservesUnconfiguredAndEmptyInputs(t *testing.T) {
	rt := &Runtime{}
	messages := []*model.Message{userMsg("unchanged")}
	history := rt.newPlannerHistory(t.Context(), &AgentRegistration{}, messages, nil)
	actual, err := history.prepare()
	require.NoError(t, err)
	assert.Same(t, &messages[0], &actual[0])

	reg := &AgentRegistration{}
	reg.Policy.History = func(context.Context, []*model.Message, []*model.ToolDefinition) ([]*model.Message, error) {
		t.Fatal("an empty input does not call the history policy")
		return nil, nil
	}
	empty := rt.newPlannerHistory(t.Context(), reg, nil, nil)
	actual, err = empty.prepare()
	require.NoError(t, err)
	assert.Nil(t, actual)
}

func TestPlanActivitiesSkipUnusedCompression(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := historyPhaseStart
		if resume {
			name = historyPhaseResume
		}
		t.Run(name, func(t *testing.T) {
			provider := &historyCountingClient{}
			pl := &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return finalPlannerResult("already complete"), nil
				},
				resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
					return finalPlannerResult("already complete"), nil
				},
			}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			reg := rt.agents["service.agent"]
			reg.Policy.History = Compress(mustTestModelClient(provider), HistoryCompressionConfig{
				CompressAtMaxInputTokens: 50,
				KeepMaxTurns:             1,
			})
			rt.agents["service.agent"] = reg
			input := &PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "run-123",
				RunContext: run.Context{RunID: "run-123"},
				Messages: []*model.Message{
					systemMsg(), userMsg("one"), assistantTextMsg("answer one"),
					userMsg("two"), assistantTextMsg("answer two"), userMsg("three"),
				},
			}
			call := rt.PlanStartActivity
			if resume {
				call = rt.PlanResumeActivity
			}
			output, err := call(t.Context(), input)
			require.NoError(t, err)
			require.NotNil(t, output.Result.FinalResponse)
			assert.False(t, provider.tokenCounted)
			assert.Nil(t, provider.summarized)
		})
	}
}

func TestPlanActivitiesPreserveHistoryFailurePrecedence(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, mode := range []string{"propagated", "ignored", "planner output failure", "model output failure", "history output failure", "empty prepared history"} {
			phase := historyPhaseStart
			if resume {
				phase = historyPhaseResume
			}
			t.Run(phase+"/"+mode, func(t *testing.T) {
				policyErr := errors.New("history provider failed with original details")
				if mode == "history output failure" {
					policyErr = planner.NewOutputContractError(policyErr)
				}
				var preparedErr error
				var policyCalls int
				plan := func(ctx context.Context, prepare func() ([]*model.Message, error), agentCtx planner.PlannerContext) (*planner.PlanResult, error) {
					messages, err := prepare()
					require.Nil(t, messages)
					require.Error(t, err)
					preparedErr = err
					switch mode {
					case "propagated":
						return nil, err
					case "planner output failure":
						return nil, planner.NewOutputContractError(errors.New("later invalid planner output"))
					case "model output failure":
						client, ok := agentCtx.ModelClient("test")
						require.True(t, ok)
						response, modelErr := client.Complete(ctx, &model.Request{Model: "test"})
						require.Nil(t, response)
						require.Error(t, modelErr)
						return nil, modelErr
					default:
						return finalPlannerResult("must not be accepted"), nil
					}
				}
				pl := &stubPlanner{
					start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
						return plan(ctx, in.PrepareMessages, in.Agent)
					},
					resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
						return plan(ctx, in.PrepareMessages, in.Agent)
					},
				}
				rt := newTestRuntimeWithPlanner("service.agent", pl)
				rt.models["test"] = mustTestModelClient(stubModelClient{
					complete: func(context.Context, *model.Request) (*model.Response, error) { return nil, nil },
				})
				reg := rt.agents["service.agent"]
				reg.Definition = testRegistrationDefinition("service.agent", engine.WorkflowDefinition{}, nil)
				reg.Policy.History = func(context.Context, []*model.Message, []*model.ToolDefinition) ([]*model.Message, error) {
					policyCalls++
					if mode == "empty prepared history" {
						return nil, nil
					}
					return nil, policyErr
				}
				rt.agents["service.agent"] = reg
				input := &PlanActivityInput{
					AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123"},
					Messages: []*model.Message{userMsg("question")},
				}
				// This is the exact eager preparation route that previously returned
				// directly from the activity, including typed history errors.
				_, eagerErr := rt.applyHistoryPolicy(t.Context(), &reg, input.Messages, nil)
				require.Error(t, eagerErr)
				call := rt.PlanStartActivity
				if resume {
					call = rt.PlanResumeActivity
				}
				output, err := call(t.Context(), input)
				assert.Nil(t, output, "history errors must not become recovery activity values")
				assert.Same(t, preparedErr, err)
				assert.Equal(t, eagerErr.Error(), err.Error())
				var eagerTyped, actualTyped *planner.OutputContractError
				assert.Equal(t, errors.As(eagerErr, &eagerTyped), errors.As(err, &actualTyped))
				var eagerApplication, actualApplication *temporal.ApplicationError
				require.ErrorAs(t, temporalerrors.Wrap(eagerErr), &eagerApplication)
				require.ErrorAs(t, temporalerrors.Wrap(err), &actualApplication)
				assert.Equal(t, eagerApplication.Type(), actualApplication.Type())
				assert.Equal(t, eagerApplication.NonRetryable(), actualApplication.NonRetryable())
				assert.Equal(t, eagerApplication.Message(), actualApplication.Message())
				if mode != "empty prepared history" {
					require.ErrorIs(t, err, policyErr)
				}
				assert.Equal(t, 2, policyCalls, "one eager reference plus one activity preparation")
			})
		}
	}
}

func TestPlanActivitiesPreservePreparedModelRequest(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, oversized := range []bool{false, true} {
			phase, size := historyPhaseStart, "small"
			if resume {
				phase = historyPhaseResume
			}
			if oversized {
				size = "compressed"
			}
			t.Run(phase+"/"+size, func(t *testing.T) {
				messages := []*model.Message{systemMsg(), userMsg("current question")}
				if oversized {
					messages = []*model.Message{
						systemMsg(), userMsg("one"), assistantTextMsg("answer one"),
						userMsg("two"), assistantTextMsg("answer two"), userMsg("current question"),
					}
				}
				config := HistoryCompressionConfig{CompressAtMaxInputTokens: 50, KeepMaxTurns: 1}
				eagerProvider := &historyCountingClient{}
				provider := &historyCountingClient{}
				policy := Compress(historyTestClient(t, provider), config)
				var policyCalls, modelCalls int
				var preparedMessages []*model.Message
				plan := func(ctx context.Context, prepare func() ([]*model.Message, error), agentCtx planner.PlannerContext) (*planner.PlanResult, error) {
					actual, err := prepare()
					if err != nil {
						return nil, err
					}
					preparedMessages = actual
					again, err := prepare()
					require.NoError(t, err)
					assert.Same(t, &actual[0], &again[0])
					client, ok := agentCtx.PlannerModelClient("test")
					require.True(t, ok)
					response, err := client.Complete(ctx, &model.Request{
						Model: "test", Messages: actual, Tools: agentCtx.AdvertisedToolDefinitions(),
					})
					if err != nil {
						return nil, err
					}
					return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
				}
				pl := &stubPlanner{
					start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
						return plan(ctx, in.PrepareMessages, in.Agent)
					},
					resume: func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
						return plan(ctx, in.PrepareMessages, in.Agent)
					},
				}
				rt := newTestRuntimeWithPlanner("service.agent", pl)
				spec := newAnyJSONSpec("svc.tools.lookup")
				seedTestToolSpecs(rt, spec)
				seedTestToolDefinitions(rt, spec)
				rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"service.agent": {spec}}
				definitions := rt.advertisedToolDefinitions([]tools.ToolSpec{spec}, compiledToolPolicy{})
				want, err := Compress(historyTestClient(t, eagerProvider), config)(t.Context(), messages, definitions)
				require.NoError(t, err)
				reg := rt.agents["service.agent"]
				reg.Policy.History = func(ctx context.Context, actualMessages []*model.Message, actualDefinitions []*model.ToolDefinition) ([]*model.Message, error) {
					policyCalls++
					assert.Equal(t, messages, actualMessages)
					assertToolDefinitionsEqual(t, definitions, actualDefinitions)
					return policy(ctx, actualMessages, actualDefinitions)
				}
				rt.agents["service.agent"] = reg
				rt.models["test"] = mustTestModelClient(stubModelClient{
					complete: func(_ context.Context, req *model.Request) (*model.Response, error) {
						modelCalls++
						assert.Equal(t, want, req.Messages)
						assertToolDefinitionsEqual(t, definitions, req.Tools)
						return &model.Response{
							Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "answer"}}}},
							StopReason: "end_turn",
							Usage:      model.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
						}, nil
					},
				})
				call := rt.PlanStartActivity
				if resume {
					call = rt.PlanResumeActivity
				}
				output, err := call(t.Context(), &PlanActivityInput{
					AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123"},
					Messages: messages,
				})
				require.NoError(t, err)
				require.NotNil(t, output.Result)
				assert.Equal(t, want, preparedMessages)
				assert.Equal(t, 1, policyCalls)
				assert.Equal(t, 1, modelCalls)
				require.Len(t, provider.countedAll, len(eagerProvider.countedAll))
				for i, expected := range eagerProvider.countedAll {
					// Compiled validation state is private and contains functions.
					// Compare the advertised tools separately, then compare every
					// other public request field without comparing that private state.
					wantedCount, actualCount := *expected, *provider.countedAll[i]
					assertToolDefinitionsEqual(t, wantedCount.Tools, actualCount.Tools)
					wantedCount.Tools, actualCount.Tools = nil, nil
					assert.EqualExportedValues(t, wantedCount, actualCount)
				}
				assert.Equal(t, eagerProvider.summarized, provider.summarized)
				assert.Equal(t, model.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}, output.Usage)
				assert.Equal(t, "answer", textPart(t, output.Transcript[0]))
				assert.Equal(t, oversized, provider.summarized != nil)
			})
		}
	}
}

func TestPlanActivityHistoryCancellationAndFreshAttempt(t *testing.T) {
	started := make(chan struct{})
	var calls int
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		_, err := input.PrepareMessages()
		if err != nil {
			return nil, err
		}
		return finalPlannerResult("ready"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	reg := rt.agents["service.agent"]
	reg.Policy.History = func(ctx context.Context, messages []*model.Message, _ []*model.ToolDefinition) ([]*model.Message, error) {
		calls++
		if calls == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return messages, nil
	}
	rt.agents["service.agent"] = reg
	input := &PlanActivityInput{
		AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123"},
		Messages: []*model.Message{userMsg("question")},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		output, err := rt.PlanStartActivity(ctx, input)
		assert.Nil(t, output)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("history preparation did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("history preparation did not cancel")
	}
	output, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, output.Result.FinalResponse)
	assert.Equal(t, 2, calls)
}

func TestPlanActivityHistoryCancelsProviderOperation(t *testing.T) {
	for _, cancelSummary := range []bool{false, true} {
		name := "token count"
		if cancelSummary {
			name = "summary"
		}
		t.Run(name, func(t *testing.T) {
			provider := &cancelingHistoryProvider{cancelSummary: cancelSummary, started: make(chan struct{})}
			pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				_, err := input.PrepareMessages()
				require.ErrorIs(t, err, context.Canceled)
				_, repeatedErr := input.PrepareMessages()
				assert.Same(t, err, repeatedErr)
				// Even a planner that ignores cancellation cannot admit an action.
				return finalPlannerResult("must not be accepted"), nil
			}}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			reg := rt.agents["service.agent"]
			reg.Policy.History = Compress(historyTestClient(t, provider), HistoryCompressionConfig{
				CompressAtMaxInputTokens: 50,
				KeepMaxTurns:             1,
			})
			rt.agents["service.agent"] = reg
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				output, err := rt.PlanStartActivity(ctx, &PlanActivityInput{
					AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123"},
					Messages: []*model.Message{
						systemMsg(), userMsg("one"), assistantTextMsg("answer one"),
						userMsg("two"), assistantTextMsg("answer two"), userMsg("current question"),
					},
				})
				assert.Nil(t, output)
				done <- err
			}()
			select {
			case <-provider.started:
			case <-time.After(5 * time.Second):
				t.Fatal("history provider operation did not start")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("history provider operation did not cancel")
			}
			assert.Equal(t, 1, provider.blockedCalls, "no in-activity retry")
		})
	}
}

func (p *cancelingHistoryProvider) Complete(ctx context.Context, _ *model.Request) (*model.Response, error) {
	p.blockedCalls++
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *cancelingHistoryProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if p.cancelSummary {
		return p.historyCountingClient.CountTokens(ctx, req)
	}
	p.blockedCalls++
	close(p.started)
	<-ctx.Done()
	return model.TokenCount{}, ctx.Err()
}
