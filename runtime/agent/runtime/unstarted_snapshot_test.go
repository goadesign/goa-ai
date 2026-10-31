package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

type gatedStartEngine struct {
	engine.Engine
	release <-chan struct{}
	entered chan<- struct{}
	status  engine.RunStatus
}

func (e *gatedStartEngine) RegisterWorkflow(ctx context.Context, def engine.WorkflowDefinition) error {
	wrapped := def
	wrapped.Handler = func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		if e.entered != nil {
			select {
			case e.entered <- struct{}{}:
			default:
			}
		}
		if e.release != nil {
			select {
			case <-e.release:
			case <-wfCtx.Context().Done():
				return nil, wfCtx.Context().Err()
			}
		}
		return def.Handler(wfCtx, input)
	}
	return e.Engine.RegisterWorkflow(ctx, wrapped)
}

func (e *gatedStartEngine) QueryAcceptedRun(ctx context.Context, workflowID string) (engine.AcceptedRun, error) {
	querier, ok := e.Engine.(engine.AcceptedRunQuerier)
	if !ok {
		return engine.AcceptedRun{}, engine.ErrWorkflowNotFound
	}
	accepted, err := querier.QueryAcceptedRun(ctx, workflowID)
	if err != nil {
		return engine.AcceptedRun{}, err
	}
	if e.status != "" && !isTerminalEngineRunStatus(accepted.Status) {
		accepted.Status = e.status
	}
	return accepted, nil
}

type terminalUnstartedEngine struct {
	engine.Engine
	accepted engine.AcceptedRun
	err      error
}

func (e *terminalUnstartedEngine) QueryAcceptedRun(_ context.Context, _ string) (engine.AcceptedRun, error) {
	if e.err != nil {
		return engine.AcceptedRun{}, e.err
	}
	return e.accepted, nil
}

func TestGetRunSnapshotAcceptedBeforeStartRootRun(t *testing.T) {
	ctx := t.Context()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	store := storageinmem.New()
	eng := &gatedStartEngine{
		Engine:  engineinmem.New(),
		release: release,
		entered: entered,
	}
	rt := New(store, WithEngine(eng))

	agentID := agent.Ident("service.chat")
	def := NewAgentDefinition(
		AgentRoute{
			ID:               agentID,
			WorkflowName:     "service.chat.workflow",
			DefaultTaskQueue: "service.chat.queue",
		},
		nil,
		nil,
		[]string{"TenantID", "CustomerID"},
		nil,
		nil,
		nil,
	)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition:      def,
		WorkflowHandler: rt.ExecuteWorkflow,
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{
					FinalResponse: &planner.FinalResponse{
						Message: &model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: "done"}},
						},
					},
				}, nil
			},
		},
		ExecuteToolActivity: "service.chat.execute_tool",
		PlanActivityName:    "service.chat.plan",
		ResumeActivityName:  "service.chat.resume",
	}))

	_, err := store.CreateSession(ctx, "sess-1", time.Now().UTC())
	require.NoError(t, err)

	client := rt.MustClient(agentID)
	prepared, err := client.Prepare(
		ctx,
		"sess-1",
		[]*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
		WithRunID("run-accepted-1"),
		WithTurnID("turn-1"),
		WithPreparation("cmd-custom-1", "attempt-custom-1"),
		WithLabels(map[string]string{
			"TenantID":   "tenant-acme",
			"CustomerID": "cust-42",
		}),
	)
	require.NoError(t, err)

	// Before StartPrepared, the workflow is not yet accepted by the engine.
	_, err = rt.GetRunSnapshot(ctx, "run-accepted-1")
	require.ErrorIs(t, err, session.ErrRunNotFound)

	handle, err := client.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	<-entered

	// Storage.LoadRun remains strictly for recorded runs.
	_, err = store.LoadRun(ctx, "run-accepted-1")
	require.ErrorIs(t, err, session.ErrRunNotFound)

	// Runtime.GetRunSnapshot returns the pending snapshot with labels, even when
	// custom WithPreparation(commandID, attemptID) was used.
	snap, err := rt.GetRunSnapshot(ctx, "run-accepted-1")
	require.NoError(t, err)
	require.Equal(t, "run-accepted-1", snap.RunID)
	require.Equal(t, "sess-1", snap.SessionID)
	require.Equal(t, "turn-1", snap.TurnID)
	require.Equal(t, run.StatusPending, snap.Status)
	require.Equal(t, run.PhasePrompted, snap.Phase)
	require.True(t, snap.StartedAt.IsZero())
	require.True(t, snap.UpdatedAt.IsZero())
	require.Equal(t, map[string]string{
		"TenantID":   "tenant-acme",
		"CustomerID": "cust-42",
	}, snap.Labels)

	// Paused engine status maps to run.StatusPaused while still unstarted.
	eng.status = engine.RunStatusPaused
	pausedSnap, err := rt.GetRunSnapshot(ctx, "run-accepted-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusPaused, pausedSnap.Status)
	eng.status = ""

	// Once the worker proceeds and completes, GetRunSnapshot returns the recorded run.
	close(release)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)

	completedSnap, err := rt.GetRunSnapshot(ctx, "run-accepted-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusCompleted, completedSnap.Status)
	require.False(t, completedSnap.StartedAt.IsZero())
	require.Equal(t, "done", completedSnap.LastAssistantMessage)

	// After EndSession + PurgeSession removes stored runs while the engine still
	// retains the execution record, GetRunSnapshot must return ErrRunNotFound
	// rather than ErrRunCompletionCorrupt or resurrecting accepted metadata.
	_, err = store.EndSession(ctx, "sess-1", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(ctx, "sess-1"))
	_, err = rt.GetRunSnapshot(ctx, "run-accepted-1")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestGetRunSnapshotAcceptedContinuationBeforeStartRootRun(t *testing.T) {
	ctx := t.Context()
	spec := newAnyJSONSpec("svc.lookup")
	definition := testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", []tools.ToolSpec{spec}, []string{"TenantID", "CustomerID"})
	sourceClient, sourceStore := newPreparedRunTestClient(&stubEngine{}, definition)
	require.NoError(t, createPreparedRunSession(ctx, sourceStore))

	suspension := suspensionContractFixtureWithContext(
		t,
		spec.Name,
		"svc.agent",
		"run-1",
		map[string]string{"TenantID": "tenant-acme", "CustomerID": "cust-42", "policy_applied": "review"},
		nil,
	)
	now := time.Now().UTC()
	admitRunForTest(t, sourceStore, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("svc.agent"))},
		RunID:     "run-1",
		SessionID: "session-1",
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
		Labels:    map[string]string{"TenantID": "tenant-acme", "CustomerID": "cust-42"},
	})
	suspensionData, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(ctx, sourceStore, "run-1", session.RunSuspension{
		ID:   suspension.ID,
		Data: suspensionData,
	}))

	prepared, err := sourceClient.PrepareContinuation(
		ctx,
		"session-1",
		"run-1",
		"run-2",
		"turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID:     "clarification-1",
			Answer: "Building A",
		}},
		WorkflowOptions{},
	)
	require.NoError(t, err)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	freshEngine := &gatedStartEngine{
		Engine:  engineinmem.New(),
		release: release,
		entered: entered,
	}
	require.NoError(t, freshEngine.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(_ engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
			return &RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
		},
	}))
	freshRT := New(sourceStore, WithEngine(freshEngine))
	freshClient := freshRT.MustClientFor(definition)
	handle, err := freshClient.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	<-entered

	snap, err := freshRT.GetRunSnapshot(ctx, "run-2")
	require.NoError(t, err)
	require.Equal(t, "run-2", snap.RunID)
	require.Equal(t, "session-1", snap.SessionID)
	require.Equal(t, "turn-2", snap.TurnID)
	require.Equal(t, run.StatusPending, snap.Status)
	require.Equal(t, map[string]string{
		"TenantID":       "tenant-acme",
		"CustomerID":     "cust-42",
		"policy_applied": "review",
	}, snap.Labels)

	close(release)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
}

func TestGetRunSnapshotAcceptedServiceToolSubjectIsIsolatedFromCallerMutation(t *testing.T) {
	ctx := t.Context()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	eng := &gatedStartEngine{
		Engine:  engineinmem.New(),
		release: release,
		entered: entered,
	}
	require.NoError(t, eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "svc.tool.workflow",
		Handler: func(_ engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
			return &RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
		},
	}))

	subject := genrunidentity.RunSubject{
		Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
			Registry: "reg.main",
			Tool:     "svc.lookup",
		}),
	}
	store := storageinmem.New()
	rt := New(store, WithEngine(eng))
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:        "run-service-tool-1",
		Workflow:  "svc.tool.workflow",
		TaskQueue: "svc.queue",
		Input: &RunInput{
			Subject:   subject,
			RunID:     "run-service-tool-1",
			TurnID:    "turn-st-1",
			SeedEndID: "empty",
			Labels:    map[string]string{"TenantID": "tenant-acme"},
		},
	})
	require.NoError(t, err)
	<-entered

	snap1, err := rt.GetRunSnapshot(ctx, "run-service-tool-1")
	require.NoError(t, err)
	st1, ok := snap1.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	require.Equal(t, "reg.main", st1.Registry)
	require.Equal(t, "svc.lookup", st1.Tool)

	// Mutating the returned ServiceToolSubject pointer must not mutate the
	// in-memory engine's retained accepted identity.
	st1.Registry = "mutated.registry"
	st1.Tool = "mutated.tool"

	snap2, err := rt.GetRunSnapshot(ctx, "run-service-tool-1")
	require.NoError(t, err)
	st2, ok := snap2.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	require.Equal(t, "reg.main", st2.Registry)
	require.Equal(t, "svc.lookup", st2.Tool)

	close(release)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
}

func TestGetRunSnapshotUnstartedTerminalStatuses(t *testing.T) {
	ctx := t.Context()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("service.chat")}
	closedAt := time.Unix(1700000000, 0).UTC()

	for _, tc := range []struct {
		name       string
		status     engine.RunStatus
		wfErr      error
		wantStatus run.Status
		wantPhase  run.Phase
		wantErr    error
	}{
		{
			name:       "failed before StartRootRun",
			status:     engine.RunStatusFailed,
			wfErr:      errors.New("terminated before worker start"),
			wantStatus: run.StatusFailed,
			wantPhase:  run.PhaseFailed,
		},
		{
			name:       "timed out before StartRootRun",
			status:     engine.RunStatusTimedOut,
			wfErr:      context.DeadlineExceeded,
			wantStatus: run.StatusFailed,
			wantPhase:  run.PhaseFailed,
		},
		{
			name:       "canceled before StartRootRun",
			status:     engine.RunStatusCanceled,
			wfErr:      context.Canceled,
			wantStatus: run.StatusCanceled,
			wantPhase:  run.PhaseCanceled,
		},
		{
			name:    "completed without stored run is corrupt",
			status:  engine.RunStatusCompleted,
			wantErr: ErrRunCompletionCorrupt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storageinmem.New()
			_, err := store.CreateSession(ctx, "sess-1", closedAt)
			require.NoError(t, err)
			eng := &terminalUnstartedEngine{
				Engine: engineinmem.New(),
				accepted: engine.AcceptedRun{
					RunID:       "run-unstarted-terminal",
					Subject:     subject,
					SessionID:   "sess-1",
					TurnID:      "turn-1",
					Labels:      map[string]string{"TenantID": "tenant-acme"},
					Status:      tc.status,
					CompletedAt: closedAt,
				},
			}
			rt := New(store, WithEngine(eng))
			snap, err := rt.GetRunSnapshot(ctx, "run-unstarted-terminal")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "run-unstarted-terminal", snap.RunID)
			require.Equal(t, subject, snap.Subject)
			require.Equal(t, "sess-1", snap.SessionID)
			require.Equal(t, "turn-1", snap.TurnID)
			require.Equal(t, map[string]string{"TenantID": "tenant-acme"}, snap.Labels)
			require.Equal(t, tc.wantStatus, snap.Status)
			require.Equal(t, tc.wantPhase, snap.Phase)
			require.True(t, snap.StartedAt.IsZero())
			require.Equal(t, closedAt, snap.UpdatedAt)
		})
	}

	t.Run("ended session still permits accepted run inspection", func(t *testing.T) {
		store := storageinmem.New()
		_, err := store.CreateSession(ctx, "sess-ended", closedAt)
		require.NoError(t, err)
		_, err = store.EndSession(ctx, "sess-ended", closedAt.Add(time.Minute))
		require.NoError(t, err)
		eng := &terminalUnstartedEngine{
			Engine: engineinmem.New(),
			accepted: engine.AcceptedRun{
				RunID:       "run-ended-sess",
				Subject:     subject,
				SessionID:   "sess-ended",
				TurnID:      "turn-1",
				Labels:      map[string]string{"TenantID": "tenant-acme"},
				Status:      engine.RunStatusFailed,
				CompletedAt: closedAt,
			},
		}
		rt := New(store, WithEngine(eng))
		snap, err := rt.GetRunSnapshot(ctx, "run-ended-sess")
		require.NoError(t, err)
		require.Equal(t, run.StatusFailed, snap.Status)
		require.Equal(t, "sess-ended", snap.SessionID)
	})

	t.Run("missing or purged session preserves ErrRunNotFound before metadata or corruption handling", func(t *testing.T) {
		store := storageinmem.New()
		_, err := store.CreateSession(ctx, "sess-purged", closedAt)
		require.NoError(t, err)
		_, err = store.EndSession(ctx, "sess-purged", closedAt.Add(time.Minute))
		require.NoError(t, err)
		require.NoError(t, store.PurgeSession(ctx, "sess-purged"))

		for _, sessID := range []string{"sess-missing", "sess-purged"} {
			eng := &terminalUnstartedEngine{
				Engine: engineinmem.New(),
				accepted: engine.AcceptedRun{
					RunID:       "run-" + sessID,
					Subject:     subject,
					SessionID:   sessID,
					Labels:      map[string]string{"TenantID": "tenant-acme"},
					Status:      engine.RunStatusCompleted,
					CompletedAt: closedAt,
				},
			}
			rt := New(store, WithEngine(eng))
			_, err := rt.GetRunSnapshot(ctx, "run-"+sessID)
			require.ErrorIs(t, err, session.ErrRunNotFound)
		}
	})

	t.Run("sessionless run skips session status lookup", func(t *testing.T) {
		store := storageinmem.New()
		eng := &terminalUnstartedEngine{
			Engine: engineinmem.New(),
			accepted: engine.AcceptedRun{
				RunID:       "run-sessionless",
				Subject:     subject,
				Labels:      map[string]string{"TenantID": "tenant-acme"},
				Status:      engine.RunStatusPending,
				CompletedAt: closedAt,
			},
		}
		rt := New(store, WithEngine(eng))
		snap, err := rt.GetRunSnapshot(ctx, "run-sessionless")
		require.NoError(t, err)
		require.Equal(t, run.StatusPending, snap.Status)
		require.Empty(t, snap.SessionID)
		require.Equal(t, map[string]string{"TenantID": "tenant-acme"}, snap.Labels)
	})
}
