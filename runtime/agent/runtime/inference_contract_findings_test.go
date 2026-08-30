package runtime

// These tests cover planner-activity shutdown and output-envelope contracts.
// They use channels instead of timing so provider cleanup and activity return
// order remain deterministic under the race detector.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// delayedCancellationStreamer keeps Recv active after cancellation until the
	// test releases provider cleanup.
	delayedCancellationStreamer struct {
		ctx            context.Context
		recvStarted    chan struct{}
		cleanupStarted chan struct{}
		cleanupRelease chan struct{}
		cleanupDone    chan struct{}
		closeCalled    chan struct{}
	}

	// planActivityTestResult carries one activity call made asynchronously.
	planActivityTestResult struct {
		output *PlanActivityOutput
		err    error
	}

	// replayPublicationStore fails one append after retaining the successful
	// prefix, then treats replayed event keys as idempotent duplicates.
	replayPublicationStore struct {
		storage.Store
		mu        sync.Mutex
		failCall  int
		failed    bool
		onFailure func()
		attempts  []string
		stored    map[string]*runlog.Event
	}

	// budgetFieldNames makes repeated JSON object keys consume meaningful
	// encoded space even though each value is one boolean.
	budgetFieldNames struct {
		FirstConservativeField  bool
		SecondConservativeField bool
		ThirdConservativeField  bool
		FourthConservativeField bool
		FifthConservativeField  bool
	}

	// unboundedJSONMarshaler proves arbitrary MarshalJSON output is rejected
	// without invoking an encoder that could allocate an oversized result.
	unboundedJSONMarshaler struct{}

	// unboundedTextMarshaler proves both value and pointer TextMarshaler method
	// sets are rejected before an oversized text representation is allocated.
	unboundedTextMarshaler        struct{}
	pointerUnboundedTextMarshaler struct{}
)

// Recv blocks until cancellation and then waits for simulated provider cleanup.
func (s *delayedCancellationStreamer) Recv() (model.Chunk, error) {
	close(s.recvStarted)
	<-s.ctx.Done()
	close(s.cleanupStarted)
	<-s.cleanupRelease
	close(s.cleanupDone)
	return nil, s.ctx.Err()
}

// Response reports no completed provider response for the canceled stream.
func (*delayedCancellationStreamer) Response() *model.Response {
	return nil
}

// Close proves the active receive and its provider cleanup finished first.
func (s *delayedCancellationStreamer) Close() error {
	select {
	case <-s.cleanupDone:
	default:
		return errors.New("provider stream closed before receive cleanup completed")
	}
	close(s.closeCalled)
	return nil
}

func TestPlanStartActivityWaitsForCanceledStreamReceiveCleanup(t *testing.T) {
	recvStarted := make(chan struct{})
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	cleanupDone := make(chan struct{})
	closeCalled := make(chan struct{})
	streamFinished := make(chan error, 1)
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		stream, err := client.Stream(context.Background(), &model.Request{Model: "pending-stream"})
		require.NoError(t, err)
		go func() {
			_, recvErr := stream.Recv()
			streamFinished <- stream.Finalize(recvErr)
		}()
		<-recvStarted
		return finalPlannerResult("planner returned early"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(ctx context.Context, _ *model.Request) (model.Streamer, error) {
			return &delayedCancellationStreamer{
				ctx:            ctx,
				recvStarted:    recvStarted,
				cleanupStarted: cleanupStarted,
				cleanupRelease: cleanupRelease,
				cleanupDone:    cleanupDone,
				closeCalled:    closeCalled,
			}, nil
		},
	})

	returned := make(chan planActivityTestResult, 1)
	go func() {
		output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-pending-stream-recv",
			RunContext: run.Context{RunID: "run-pending-stream-recv"},
		})
		returned <- planActivityTestResult{output: output, err: err}
	}()

	<-cleanupStarted
	select {
	case <-returned:
		t.Fatal("planner activity returned before provider cleanup finished")
	default:
	}
	close(cleanupRelease)
	result := <-returned
	requirePlannerOutputContractFailure(t, result.output, result.err)
	require.Equal(
		t,
		planner.OutputContractOriginPlanner,
		result.output.OutputContractFailure.Origin,
	)
	require.ErrorIs(t, <-streamFinished, context.Canceled)
	<-closeCalled
}

func TestPlanStartActivityPreservesModelRejectionWhileJoiningPendingCall(t *testing.T) {
	pendingStarted := make(chan struct{})
	pendingCleanupStarted := make(chan struct{})
	pendingCleanupRelease := make(chan struct{})
	pendingReturned := make(chan struct{})
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		client, ok := input.Agent.ModelClient("test")
		require.True(t, ok)
		go func() {
			_, _ = client.Complete(ctx, &model.Request{Model: "pending"})
		}()
		<-pendingStarted
		response, err := client.Complete(ctx, &model.Request{
			Model:      "requested-model",
			ModelClass: model.ModelClassSmall,
		})
		require.Nil(t, response)
		var outputErr *planner.OutputContractError
		require.ErrorAs(t, err, &outputErr)
		require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())
		return finalPlannerResult("planner tried to continue"), nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(ctx context.Context, request *model.Request) (*model.Response, error) {
			if request.Model == "pending" {
				close(pendingStarted)
				<-ctx.Done()
				close(pendingCleanupStarted)
				<-pendingCleanupRelease
				close(pendingReturned)
				return nil, ctx.Err()
			}
			return &model.Response{
				Usage: model.TokenUsage{
					Model:        "provider-resolved-model",
					ModelClass:   model.ModelClassHighReasoning,
					InputTokens:  2,
					OutputTokens: 3,
					TotalTokens:  5,
				},
				StopReason: "end_turn",
			}, nil
		},
	})

	returned := make(chan planActivityTestResult, 1)
	go func() {
		output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-model-rejection-with-pending",
			RunContext: run.Context{RunID: "run-model-rejection-with-pending"},
		})
		returned <- planActivityTestResult{output: output, err: err}
	}()

	<-pendingCleanupStarted
	select {
	case <-returned:
		t.Fatal("planner activity returned before pending model call finished")
	default:
	}
	close(pendingCleanupRelease)
	result := <-returned
	require.Nil(t, result.output)
	require.ErrorIs(t, result.err, context.Canceled)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, result.err, &validationErr)
	<-pendingReturned
}

func TestPlanStartActivityRejectsOversizedResultBranches(t *testing.T) {
	oversized := strings.Repeat("x", maxPlanActivityOutputBytes+1)
	tests := []struct {
		name   string
		tool   tools.Ident
		result *planner.PlanResult
	}{
		{
			name: "final response",
			result: &planner.PlanResult{FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: oversized}},
				},
			}},
		},
		{
			name: "final tool result",
			tool: "service.tools.finish",
			result: &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message(`"` + oversized + `"`),
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return test.result, nil
				},
			})
			if test.tool != "" {
				seedTestToolSpecs(rt, newAnyJSONSpec(test.tool))
			}

			output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
				AgentID:    "service.agent",
				RunID:      "run-oversized-result",
				RunContext: run.Context{RunID: "run-oversized-result", Tool: test.tool},
			})

			requirePlannerOutputContractFailure(t, output, err)
			require.Equal(
				t,
				planner.OutputContractOriginPlanner,
				output.OutputContractFailure.Origin,
			)
			require.Empty(t, output.PlannerEvents)
			require.Equal(
				t,
				planActivityBudgetReasonFingerprint(t),
				output.OutputContractFailure.ReasonSHA256,
			)
		})
	}
}

func TestPlanStartActivityRejectsManyIndividuallyBoundedEvents(t *testing.T) {
	const eventCount = 2_000
	eventText := strings.Repeat("e", 600)
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			for index := 0; index < eventCount; index++ {
				input.Events.PlannerThought(ctx, eventText, nil)
			}
			return finalPlannerResult("done"), nil
		},
	})

	output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-many-events",
		RunContext: run.Context{RunID: "run-many-events"},
	})

	requirePlannerOutputContractFailure(t, output, err)
	require.Equal(t, planner.OutputContractOriginPlanner, output.OutputContractFailure.Origin)
	require.Empty(t, output.PlannerEvents)
	require.Equal(
		t,
		planActivityBudgetReasonFingerprint(t),
		output.OutputContractFailure.ReasonSHA256,
	)
}

func TestPlanStartActivityRejectsExcessiveVisitedValues(t *testing.T) {
	notes := make([]planner.PlannerAnnotation, maxPlanActivityOutputVisits)
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			result := finalPlannerResult("done")
			result.Notes = notes
			return result, nil
		},
	})

	output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-many-values",
		RunContext: run.Context{RunID: "run-many-values"},
	})

	requirePlannerOutputContractFailure(t, output, err)
	require.Equal(t, planner.OutputContractOriginPlanner, output.OutputContractFailure.Origin)
	require.Empty(t, output.PlannerEvents)
}

func TestPlanActivityOutputBudgetBoundsEncodedJSONShape(t *testing.T) {
	minimumIntegers := make([]int64, 99_900)
	for index := range minimumIntegers {
		minimumIntegers[index] = -1 << 63
	}
	tests := []struct {
		name  string
		value any
	}{
		{
			name:  "scalar-heavy array",
			value: minimumIntegers,
		},
		{
			name:  "escapable string",
			value: strings.Repeat("\x00", maxPlanActivityOutputBytes/6+1),
		},
		{
			name:  "base64 byte slice",
			value: make([]byte, 800_000),
		},
		{
			name: "escaped map key",
			value: map[string]bool{
				strings.Repeat("\x00", maxPlanActivityOutputBytes/6+1): true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&planActivityOutputBudget{}).add(test.value)
			require.ErrorContains(t, err, "conservative encoded-size bound")
		})
	}
}

func TestPlanActivityOutputBudgetAcceptsExactStringAndRawJSONSizes(t *testing.T) {
	require.NoError(
		t,
		(&planActivityOutputBudget{}).add(strings.Repeat("x", maxPlanActivityOutputBytes/2)),
	)
	require.NoError(
		t,
		(&planActivityOutputBudget{}).add(
			rawjson.Message(`"`+strings.Repeat("x", maxPlanActivityOutputBytes/2)+`"`),
		),
	)
	require.NoError(
		t,
		(&planActivityOutputBudget{}).add(make([]budgetFieldNames, 2_000)),
	)
}

func TestPlanActivityOutputBudgetRejectsUnboundedJSONMarshalerWithoutCallingIt(t *testing.T) {
	err := (&planActivityOutputBudget{}).add(unboundedJSONMarshaler{})

	require.ErrorContains(t, err, "custom JSON marshaler")
}

func TestPlanActivityOutputBudgetRejectsUnboundedTextMarshalersWithoutCallingThem(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "value implementation", value: unboundedTextMarshaler{}},
		{name: "pointer implementation", value: &pointerUnboundedTextMarshaler{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&planActivityOutputBudget{}).add(map[string]any{"metadata": test.value})
			require.ErrorContains(t, err, "custom text marshaler")
		})
	}
}

func TestModelFailureDropsOversizedUsageEventsButKeepsEvidence(t *testing.T) {
	const invocationCount = 2_200
	journal := &modelInvocationJournal{
		invocations: make(map[modelInvocationID]*modelInvocationCandidate, invocationCount),
		usage:       model.TokenUsage{TotalTokens: invocationCount},
	}
	providerModel := strings.Repeat("m", 512)
	for index := 0; index < invocationCount; index++ {
		id := provenance.New()
		journal.order = append(journal.order, id)
		journal.invocations[id] = &modelInvocationCandidate{
			finished: true,
			usage: model.TokenUsage{
				Model:       providerModel,
				ModelClass:  model.ModelClassSmall,
				TotalTokens: 1,
			},
		}
	}
	modelErr := outputcontract.NewWithOrigin(
		errors.New("malformed model output"),
		outputcontract.OriginModel,
	)
	rejected := journal.invocations[journal.order[0]]
	rejected.err = modelErr
	rejected.outcome = &modelcall.Outcome{ProviderCall: modelcall.Result{Called: true, Err: modelErr}}
	journal.recovery = journal.order[0]
	rejected.rejectedResponseEvidence = &model.ResponseEvidence{
		Present: true,
		Version: "response-v1",
		SHA256:  strings.Repeat("a", 64),
		Size:    123,
	}
	journal.outputErr = modelErr
	act := &plannerActivityInvocation{
		events:      newPlannerEvents("service.agent", "run-oversized-failure-events", ""),
		invocations: journal,
	}

	output, err := act.outputContractFailure(t.Context(), modelErr)

	require.NoError(t, err)
	require.NotNil(t, output)
	require.Nil(t, output.Result)
	require.Empty(t, output.PlannerEvents)
	require.Equal(t, invocationCount, output.Usage.TotalTokens)
	require.Equal(t, planner.OutputContractOriginModel, output.OutputContractFailure.Origin)
	require.True(t, output.OutputContractFailure.ModelResponsePresent)
	require.Equal(t, strings.Repeat("a", 64), output.OutputContractFailure.ModelResponseSHA256)
	require.Equal(t, int64(123), output.OutputContractFailure.ModelResponseSize)
	require.Equal(
		t,
		planActivityBudgetReasonFingerprint(t),
		output.OutputContractFailure.ReasonSHA256,
	)
}

func TestSuccessfulOutputReturnsExhaustedPublicationErrorWithoutReplanning(t *testing.T) {
	plannerCalls := 0
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			plannerCalls++
			input.Events.PlannerThought(ctx, "visible progress", nil)
			return finalPlannerResult("done"), nil
		},
	})
	store := &replayPublicationStore{
		failCall: 1,
		stored:   make(map[string]*runlog.Event),
	}
	rt.Store = store
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		runtime:     rt,
		hookRuntime: rt,
	}

	output, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-success-publication-failure",
			RunContext: run.Context{RunID: "run-success-publication-failure"},
		},
		time.Time{},
	)

	require.ErrorContains(t, err, "record backend unavailable")
	require.Nil(t, output)
	require.Equal(t, 1, plannerCalls)
	require.Zero(t, store.storedCount())
	attempts := store.eventKeyAttempts()
	require.Len(t, attempts, 1)
}

func TestNormalizePlanResultContractPreservesProviderIdentityInValidationProjection(t *testing.T) {
	result := &PlanResult{ToolCalls: []ToolCall{{
		Name:            "svc.tools.lookup",
		ToolCallID:      "runtime-call-1",
		ModelToolCallID: "provider-call-1",
		Payload:         rawjson.Message(`{}`),
	}}}

	projected := plannerResultValidationProjection(result)
	require.Equal(t, "provider-call-1", projected.ToolCalls[0].ModelToolCallID)
	require.NotEqual(t, result.ToolCalls[0].ToolCallID, projected.ToolCalls[0].ModelToolCallID)
	_, err := New(newTestStore()).normalizePlanResultContract(result, "")
	require.NoError(t, err)
}

func TestInvalidPlannerActivityResultPublishesNoRecords(t *testing.T) {
	toolSpec := newAnyJSONSpec("svc.tools.lookup")
	final := finalPlannerResult("done").FinalResponse
	tests := []struct {
		name   string
		result *PlanResult
	}{
		{
			name: "invalid deep payload",
			result: &PlanResult{ToolCalls: []ToolCall{{
				Name:       toolSpec.Name,
				ToolCallID: "call-1",
				Payload:    rawjson.Message(`{`),
			}}},
		},
		{
			name: "duplicate call IDs",
			result: &PlanResult{ToolCalls: []ToolCall{
				{Name: toolSpec.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`)},
				{Name: toolSpec.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`)},
			}},
		},
		{
			name: "invalid await union",
			result: &PlanResult{Await: &planner.Await{Items: []planner.AwaitItem{{
				Kind: planner.AwaitItemKindClarification,
				Clarification: &planner.AwaitClarification{
					ID:       "clarification-1",
					Question: "Which site?",
				},
				ExternalTools: &planner.AwaitExternalTools{
					ID: "external-1",
					Items: []planner.AwaitToolItem{{
						Name:            toolSpec.Name,
						ModelToolCallID: "call-1",
						Payload:         rawjson.Message(`{}`),
					}},
				},
			}}}},
		},
		{
			name: "unknown tool",
			result: &PlanResult{ToolCalls: []ToolCall{{
				Name:       "svc.tools.missing",
				ToolCallID: "call-1",
				Payload:    rawjson.Message(`{}`),
			}}},
		},
		{
			name: "invalid synthesis flags",
			result: &PlanResult{
				ToolCalls: []ToolCall{{
					Name:       toolSpec.Name,
					ToolCallID: "call-1",
					Payload:    rawjson.Message(`{}`),
				}},
				SynthesizeAfterTools: true,
				FinalResponse:        final,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &replayPublicationStore{stored: make(map[string]*runlog.Event)}
			rt := New(newTestStore())
			rt.Store = store
			rt.Bus = noopHooks{}
			seedTestToolSpecs(rt, toolSpec)
			events := newPlannerEvents("service.agent", "run-invalid-result", "")
			events.PlannerThought(t.Context(), "must not publish", nil)
			records, err := events.acceptedRecords(nil)
			require.NoError(t, err)
			output := &PlanActivityOutput{
				PublicationBatchID: "11111111-1111-4111-8111-111111111111",
				Result:             test.result,
				PlannerEvents:      records,
			}
			wfCtx := &routeWorkflowContext{
				ctx:         context.Background(),
				runID:       "workflow-invalid-result",
				hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					//nolint:unparam // the route signature requires the error result.
					"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
						return output, nil
					},
				},
			}

			got, err := rt.runPlanActivity(
				wfCtx,
				"plan",
				engine.ActivityOptions{},
				PlanActivityInput{
					AgentID:    "service.agent",
					RunID:      "run-invalid-result",
					RunContext: run.Context{RunID: "run-invalid-result"},
				},
				time.Time{},
			)

			require.Nil(t, got)
			var outputErr *planner.OutputContractError
			require.ErrorAs(t, err, &outputErr)
			require.Zero(t, store.storedCount())
			require.Nil(t, wfCtx.lastHookCall.Command)
		})
	}
}

func TestOutputFailureReturnsExhaustedPublicationErrorWithoutReplanning(t *testing.T) {
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
	rt.Store = store
	rt.Bus = noopHooks{}
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{Usage: model.TokenUsage{TotalTokens: 1}}, nil
		},
	})
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-publication-replay",
		RunContext: run.Context{RunID: "run-publication-replay", TurnID: "turn-1"},
	}
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		workflowID:  "workflow-publication-replay",
		runtime:     rt,
		hookRuntime: rt,
	}

	output, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		input,
		time.Time{},
	)

	require.Nil(t, output)
	require.ErrorContains(t, err, "record backend unavailable")
	require.False(t, temporalerrors.IsOutputContract(err))
	require.Equal(t, 1, store.storedCount())
	require.Equal(t, 1, providerCalls)
	attempts := store.eventKeyAttempts()
	require.Len(t, attempts, 2)
	require.NotEqual(t, attempts[0], attempts[1])
	require.Zero(t, store.countType(hooks.ModelOutputRejected))
}

func TestOutputFailurePublicationDoesNotMaskActivityErrorAfterCancellation(t *testing.T) {
	plannerCalls := 0
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			plannerCalls++
			return nil, planner.NewOutputContractError(errors.New("invalid planner output"))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	store := &replayPublicationStore{
		Store:     newTestStore(),
		failCall:  1,
		onFailure: cancel,
		stored:    make(map[string]*runlog.Event),
	}
	rt.Store = store
	rt.Bus = noopHooks{}
	wfCtx := &testWorkflowContext{
		ctx:         ctx,
		runtime:     rt,
		hookRuntime: rt,
	}

	output, err := rt.runPlanActivity(
		wfCtx,
		"plan",
		engine.ActivityOptions{},
		PlanActivityInput{
			AgentID:    "service.agent",
			RunID:      "run-canceled-publication",
			RunContext: run.Context{RunID: "run-canceled-publication"},
		},
		time.Time{},
	)

	require.Nil(t, output)
	require.ErrorContains(t, err, "record backend unavailable")
	require.False(t, temporalerrors.IsOutputContract(err))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, 1, plannerCalls)
}

// planActivityBudgetReasonFingerprint returns the reason identity emitted when
// the conservative encoded-size bound rejects the activity envelope.
func planActivityBudgetReasonFingerprint(t *testing.T) string {
	t.Helper()
	reason := "planner activity output rejected before Temporal encoding: " +
		"planner activity output exceeds conservative encoded-size bound 1048576 bytes"
	digest, _ := fingerprintBytes([]byte(reason))
	return digest
}

// AppendRunRecord records every attempted identity, fails one configured call, and
// otherwise inserts each stable event key once.
func (s *replayPublicationStore) AppendRunRecord(
	_ context.Context,
	event *runlog.Event,
) (storage.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, event.EventKey)
	if !s.failed && len(s.attempts) == s.failCall {
		s.failed = true
		if s.onFailure != nil {
			s.onFailure()
		}
		return storage.AppendResult{}, errors.New("record backend unavailable")
	}
	if stored, ok := s.stored[event.EventKey]; ok {
		if stored.RunID != event.RunID ||
			stored.AgentID != event.AgentID ||
			stored.SessionID != event.SessionID ||
			stored.TurnID != event.TurnID ||
			stored.Type != event.Type ||
			!stored.Timestamp.Equal(event.Timestamp) ||
			!bytes.Equal(stored.Payload, event.Payload) {
			return storage.AppendResult{}, errors.New("duplicate event key changed immutable record")
		}
		return storage.AppendResult{ID: stored.ID, Inserted: false}, nil
	}
	cloned := *event
	cloned.Payload = append(rawjson.Message(nil), event.Payload...)
	cloned.ID = event.EventKey
	s.stored[event.EventKey] = &cloned
	return storage.AppendResult{ID: cloned.ID, Inserted: true}, nil
}

// storedCount reports the number of unique durable identities.
func (s *replayPublicationStore) storedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stored)
}

// eventKeyAttempts returns a copy of the attempted identity sequence.
func (s *replayPublicationStore) eventKeyAttempts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attempts...)
}

// countType reports how many unique durable records have the requested type.
func (s *replayPublicationStore) countType(recordType runlog.Type) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, event := range s.stored {
		if event.Type == recordType {
			count++
		}
	}
	return count
}

// MarshalJSON panics if the budget walker invokes it; arbitrary marshalers must
// be rejected from reflected shape alone.
func (unboundedJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic("activity output budget invoked custom JSON marshaler")
}

// MarshalText would exceed the complete activity budget if invoked.
func (unboundedTextMarshaler) MarshalText() ([]byte, error) {
	return make([]byte, maxPlanActivityOutputBytes+1), nil
}

// MarshalText would exceed the complete activity budget if invoked through a
// pointer method set.
func (*pointerUnboundedTextMarshaler) MarshalText() ([]byte, error) {
	return make([]byte, maxPlanActivityOutputBytes+1), nil
}
