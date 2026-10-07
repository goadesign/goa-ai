package runtime

// These tests exercise recovery through the validated model client and through
// workflow activity dispatch. A virtual workflow clock advances durable timers
// without sleeping; model and tool requests retain their original identities.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	providerRecoveryWorkflow struct {
		*routeWorkflowContext
		clock   time.Time
		delays  []time.Duration
		onTimer func()
	}
	providerRecoveryScopedWorkflow struct {
		engine.WorkflowContext
		clock *providerRecoveryWorkflow
	}
)

func (w *providerRecoveryWorkflow) Now() time.Time { return w.clock }

func (w *providerRecoveryWorkflow) NewTimer(ctx context.Context, delay time.Duration) (engine.Future[time.Time], error) {
	w.delays = append(w.delays, delay)
	w.clock = w.clock.Add(delay)
	if w.onTimer != nil {
		w.onTimer()
	}
	future := &controlledTimeFuture{ready: make(chan struct{}), v: w.clock, err: ctx.Err()}
	close(future.ready)
	return future, nil
}

func (w *providerRecoveryWorkflow) WithCancel() (engine.WorkflowContext, func()) {
	derived, cancel := w.routeWorkflowContext.WithCancel()
	return &providerRecoveryScopedWorkflow{WorkflowContext: derived, clock: w}, cancel
}

func (w *providerRecoveryScopedWorkflow) Now() time.Time {
	return w.clock.Now()
}

func (w *providerRecoveryScopedWorkflow) NewTimer(ctx context.Context, delay time.Duration) (engine.Future[time.Time], error) {
	return w.clock.NewTimer(ctx, delay)
}

func (w *providerRecoveryScopedWorkflow) WithCancel() (engine.WorkflowContext, func()) {
	derived, cancel := w.WorkflowContext.WithCancel()
	return &providerRecoveryScopedWorkflow{WorkflowContext: derived, clock: w.clock}, cancel
}

func TestProviderRecoveryActivityProvesSafeFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      model.ProviderErrorKind
		chunk     model.Chunk
		closeErr  error
		extraErr  error
		disabled  bool
		noPolicy  bool
		wantRetry bool
	}{
		{name: "rate limit", kind: model.ProviderErrorKindRateLimited, wantRetry: true},
		{name: "unavailable", kind: model.ProviderErrorKindUnavailable, wantRetry: true},
		{name: "permanent", kind: model.ProviderErrorKindInvalidRequest},
		{name: "disabled", kind: model.ProviderErrorKindRateLimited, disabled: true, wantRetry: true},
		{name: "absent policy", kind: model.ProviderErrorKindRateLimited, noPolicy: true, wantRetry: true},
		{name: "cleanup", kind: model.ProviderErrorKindRateLimited, closeErr: errors.New("cleanup failed")},
		{name: "extra failure", kind: model.ProviderErrorKindRateLimited, extraErr: errors.New("planner failed")},
		{name: "text", kind: model.ProviderErrorKindRateLimited, chunk: model.TextChunk{Message: model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "partial"}},
		}}},
		{name: "thinking", kind: model.ProviderErrorKindRateLimited, chunk: model.ThinkingChunk{Message: model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "partial", Final: false}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := model.NewProviderError("synthetic", "stream", 0, tc.kind, "temporary", "temporary failure", "", tc.kind != model.ProviderErrorKindInvalidRequest, nil)
			pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := input.Agent.PlannerModelClient("test")
				require.True(t, ok)
				_, err := client.Stream(ctx, &model.Request{Model: "synthetic"})
				return nil, errors.Join(err, tc.extraErr)
			}}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			stream := &stubStreamer{recvErr: failure, closeErr: tc.closeErr}
			if tc.chunk != nil {
				stream.chunks = []model.Chunk{tc.chunk}
			}
			rt.models["test"] = mustTestModelClient(stubModelClient{stream: func(context.Context, *model.Request) (model.Streamer, error) {
				return stream, nil
			}})
			policy := &PolicyOverrides{ProviderRetryBudget: time.Hour}
			if tc.disabled {
				policy.ProviderRetryBudget = 0
			}
			if tc.noPolicy {
				policy = nil
			}
			out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{
				AgentID: "service.agent", RunID: "run", RunContext: run.Context{RunID: "run"}, Policy: policy,
			}, nil))
			switch {
			case tc.wantRetry:
				require.NoError(t, err)
				require.NotNil(t, out.ProviderFailure)
				assert.Same(t, failure, out.ProviderFailure)
				assert.Equal(t, tc.kind, out.ProviderFailure.Kind())
				assert.Nil(t, out.Result)
			case out != nil:
				assert.Nil(t, out.ProviderFailure)
				assert.NotNil(t, out.PlanningFailure)
			default:
				require.Error(t, err)
			}
		})
	}
}

func TestProviderRecoveryRetainsRequestAndActiveBudget(t *testing.T) {
	var calls int
	rt := newTestRuntimeWithPlanner("service.agent", nil)
	base := &workflowConversation{providerRecovery: &providerRecoveryBudget{Remaining: time.Hour}}
	w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run", HistoryEndID: "saved-position", RunContext: run.Context{RunID: "run", Attempt: 3}}
	w.routeWorkflowContext = &routeWorkflowContext{
		ctx: t.Context(), runID: "run", hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": func(_ context.Context, actual *PlanActivityInput) (*PlanActivityOutput, error) { //nolint:unparam // Planner route requires an error result.
				calls++
				require.Equal(t, input, *actual)
				w.clock = w.clock.Add(2 * time.Second)
				if calls <= 2 {
					return providerRecoveryFailureOutput(), nil
				}
				out := deadlineTestFinalOutput()
				out.Usage = model.TokenUsage{InputTokens: 3, TotalTokens: 3}
				return out, nil
			},
		},
	}
	budget := w.Now().Add(15 * time.Minute)
	hard := budget.Add(time.Minute)
	initialBudget, initialHard := budget, hard
	wf, err := installProviderRecovery(w, base.providerRecovery)
	require.NoError(t, err)
	base.providerControl = wf.actor
	wf.actor.budget = &budget
	wf.actor.hard = &hard
	out, err := rt.runPlanActivity(wf, "resume", engine.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}}, input, base, budget)
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	require.Len(t, w.delays, 2)
	assert.Equal(t, 5, out.Usage.TotalTokens)
	credited := 4*time.Second + w.delays[0] + w.delays[1]
	assert.Equal(t, credited, wf.actor.elapsed)
	assert.Equal(t, initialBudget.Add(credited), budget)
	assert.Equal(t, initialHard.Add(credited), hard)
	assert.Equal(t, time.Hour-credited, base.providerRecovery.Remaining)
	assert.Equal(t, engine.RetryPolicy{MaxAttempts: 1}, w.lastPlannerCall.Options.RetryPolicy)
}

func TestProviderRecoveryStopsOnCancellationExpirationAndActivityError(t *testing.T) {
	for _, tc := range []string{"cancellation", "expiration", "activity error"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rt := newTestRuntimeWithPlanner("service.agent", nil)
			calls := 0
			w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
			w.routeWorkflowContext = &routeWorkflowContext{
				ctx: ctx, hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
						calls++
						if tc == "activity error" {
							return nil, model.NewProviderError("synthetic", "stream", 429, model.ProviderErrorKindRateLimited, "temporary", "lost activity reply", "", true, nil)
						}
						return providerRecoveryFailureOutput(), nil
					},
				},
			}
			if tc == "cancellation" {
				w.onTimer = cancel
			}
			base := &workflowConversation{providerRecovery: &providerRecoveryBudget{Remaining: time.Second}}
			out, err := rt.runPlanActivity(w, "plan", engine.ActivityOptions{}, PlanActivityInput{RunID: "run"}, base, time.Time{})
			require.Error(t, err)
			assert.Equal(t, 1, calls)
			switch tc {
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
			case "expiration":
				require.ErrorContains(t, err, "provider recovery budget exhausted")
				require.NotNil(t, out.ProviderFailure)
				assert.Zero(t, base.providerRecovery.Remaining)
			case "activity error":
				assert.Empty(t, w.delays)
			}
		})
	}
}

func TestProviderRecoveryBoundsRetryQueueAndExecution(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", nil)
	base := &workflowConversation{providerRecovery: &providerRecoveryBudget{Remaining: time.Minute}}
	w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
	calls := 0
	w.routeWorkflowContext = &routeWorkflowContext{
		ctx: t.Context(), hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) { //nolint:unparam // Planner route requires an error result.
				calls++
				if calls == 1 {
					return providerRecoveryFailureOutput(), nil
				}
				return deadlineTestFinalOutput(), nil
			},
		},
	}
	_, err := rt.runPlanActivity(w, "plan", engine.ActivityOptions{StartToCloseTimeout: time.Hour}, PlanActivityInput{RunID: "run"}, base, w.Now().Add(15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	want := time.Minute - w.delays[0]
	assert.Equal(t, want, w.lastPlannerCall.Options.StartToCloseTimeout)
	assert.Equal(t, want, w.lastPlannerCall.Options.ScheduleToCloseTimeout)
}

func TestProviderRecoveryDisabledAndExhaustedPreserveTypedFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recovery *providerRecoveryBudget
	}{
		{name: "disabled"},
		{name: "already exhausted", recovery: &providerRecoveryBudget{}},
		{name: "failure exhausts remainder", recovery: &providerRecoveryBudget{Remaining: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", nil)
			w := &providerRecoveryWorkflow{clock: time.Unix(100, 0)}
			output := providerRecoveryFailureOutput()
			calls := 0
			w.routeWorkflowContext = &routeWorkflowContext{
				ctx: t.Context(), hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) { //nolint:unparam // Planner route requires an error result.
						calls++
						w.clock = w.clock.Add(time.Second)
						return output, nil
					},
				},
			}
			actual, err := rt.runPlanActivity(w, "plan", engine.ActivityOptions{}, PlanActivityInput{RunID: "run"},
				&workflowConversation{providerRecovery: tc.recovery}, time.Time{})
			require.Error(t, err)
			assert.Same(t, output, actual)
			failure, ok := model.AsProviderError(err)
			require.True(t, ok)
			assert.Same(t, output.ProviderFailure, failure)
			assert.Equal(t, 1, calls)
			assert.Empty(t, w.delays)
		})
	}
}

func providerRecoveryFailureOutput() *PlanActivityOutput {
	return &PlanActivityOutput{
		PublicationBatchID: uuid.NewString(),
		ProviderFailure:    model.NewProviderError("synthetic", "stream", 429, model.ProviderErrorKindRateLimited, "throttled", "", "request-1", true, errors.New("capacity temporarily unavailable")),
		Usage:              model.TokenUsage{InputTokens: 1, TotalTokens: 1},
	}
}

func TestProviderRecoveryCheckpointPreservesRemainingBudget(t *testing.T) {
	for _, remaining := range []time.Duration{0, 37 * time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			rt := New(newTestStore())
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(rt, spec)
			suspension := suspensionContractFixture(t, spec.Name)
			rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
				checkpoint.Policy = &PolicyOverrides{ProviderRetryBudget: time.Hour}
				checkpoint.ProviderRecovery = &providerRecoveryBudget{
					Remaining: remaining,
					allowance: &providerRecoveryAllowance{initial: time.Hour},
				}
			})
			checkpoint, err := decodeWorkflowCheckpoint(suspension, testRuntimeDefinition(rt, "svc.agent"))
			require.NoError(t, err)
			require.NotNil(t, checkpoint.ProviderRecovery)
			assert.Equal(t, remaining, checkpoint.ProviderRecovery.Remaining)
			assert.Nil(t, checkpoint.ProviderRecovery.allowance)
			checkpoint.ProviderRecovery.activate()
			assert.Equal(t, remaining, checkpoint.ProviderRecovery.allowance.initial)
		})
	}
}

func TestProviderRecoveryCheckpointRejectsInvalidBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		budget   time.Duration
		recovery *providerRecoveryBudget
	}{
		{name: "missing state", budget: time.Hour},
		{name: "disabled with state", recovery: &providerRecoveryBudget{}},
		{name: "negative policy", budget: -time.Hour},
		{name: "negative remaining", budget: time.Hour, recovery: &providerRecoveryBudget{Remaining: -time.Second}},
		{name: "remaining exceeds allowance", budget: time.Hour, recovery: &providerRecoveryBudget{Remaining: 2 * time.Hour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := New(newTestStore())
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(rt, spec)
			suspension := suspensionContractFixture(t, spec.Name)
			rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
				checkpoint.Policy = &PolicyOverrides{ProviderRetryBudget: tc.budget}
				checkpoint.ProviderRecovery = tc.recovery
			})
			_, err := decodeWorkflowCheckpoint(suspension, testRuntimeDefinition(rt, "svc.agent"))
			require.Error(t, err)
		})
	}
}

func TestProviderRecoveryRejectsMixedActivityResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlanActivityOutput)
	}{
		{name: "accepted result", mutate: func(out *PlanActivityOutput) { out.Result = &PlanResult{} }},
		{name: "published text", mutate: func(out *PlanActivityOutput) { out.PublishedAssistantText = "visible" }},
		{name: "permanent failure", mutate: func(out *PlanActivityOutput) {
			out.ProviderFailure = model.NewProviderError("synthetic", "stream", 429, model.ProviderErrorKindRateLimited, "", "", "", false, nil)
		}},
		{name: "unknown kind", mutate: func(out *PlanActivityOutput) {
			out.ProviderFailure = model.NewProviderError("synthetic", "stream", 0, "unclassified", "", "", "", true, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := providerRecoveryFailureOutput()
			tc.mutate(out)
			require.Error(t, validateProviderFailureOutput(out))
		})
	}
}
