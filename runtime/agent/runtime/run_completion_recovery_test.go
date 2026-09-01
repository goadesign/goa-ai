// Package runtime tests engine-result recovery for workflows that closed before
// their final result reached durable storage.
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateEngineCompletionRejectsInvalidWorkflowOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		output *api.RunOutput
	}{
		{
			name:   "missing terminal result",
			output: &api.RunOutput{AgentID: "svc.agent", RunID: "run"},
		},
		{
			name: "multiple terminal results",
			output: &api.RunOutput{
				AgentID:    "svc.agent",
				RunID:      "run",
				Final:      &model.Message{},
				Suspension: &api.RunSuspension{},
			},
		},
		{
			name: "invalid suspension",
			output: &api.RunOutput{
				AgentID:    "svc.agent",
				RunID:      "run",
				Suspension: &api.RunSuspension{},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateEngineCompletion(engine.RunCompletion{
				Status:      engine.RunStatusCompleted,
				CompletedAt: ensuredCompletionTime,
				Output:      test.output,
			}, &RunInput{AgentID: "svc.agent", RunID: "run"})

			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
		})
	}
}

func TestEnsureRunCompletionStoresWorkflowFailure(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{
				Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime, WorkflowError: errors.New("planner failed"),
			},
		},
		streamSubscriber: newCompletionSubscriber(t),
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestEnsureRunCompletionRetriesStreamWithoutRepeatingStore(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	store := &countingTerminalRepairStore{Store: stored}
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &retryingStreamSink{err: errors.New("stream send failed")}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime, Output: &api.RunOutput{
			AgentID: "svc.agent", RunID: "run", Final: &model.Message{},
		}}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.Equal(t, 1, store.calls)
	require.Equal(t, 1, busCalls)
	require.GreaterOrEqual(t, sink.callCount(), 2)
}

func TestEnsureRunCompletionExactRetryResumesStreamDelivery(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	store := &lostTerminalRepairResponseStore{Store: stored}
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &retryingStreamSink{}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status:      engine.RunStatusCompleted,
			CompletedAt: ensuredCompletionTime,
			Output: &api.RunOutput{
				AgentID: "svc.agent",
				RunID:   "run",
				Final:   &model.Message{},
			},
		}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.Equal(t, 2, store.calls)
	require.Zero(t, busCalls)
	require.Equal(t, 2, sink.callCount())
}

func TestEnsureRunCompletionStoresSuspension(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	suspension := suspensionContractFixtureWithContext(
		t,
		tools.Ident("svc.tools.lookup"),
		"svc.agent",
		"run-1",
		nil,
		nil,
	)
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime, Output: &api.RunOutput{
				AgentID: "svc.agent", RunID: "run-1", Suspension: suspension,
			}},
		},
		streamSubscriber: newCompletionSubscriber(t),
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run-1"))
	meta, err := store.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, meta.Status)
	stored, err := runtime.LoadRunSuspension(t.Context(), "run-1")
	require.NoError(t, err)
	require.Equal(t, suspension.ID, stored.ID)
}

func TestEnsureRunCompletionRejectsSuspensionParentBeforeWriting(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	suspension := suspensionContractFixtureWithContext(
		t,
		tools.Ident("svc.tools.lookup"),
		"svc.agent",
		"run-1",
		nil,
		nil,
	)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Context.ParentRunID = completionParentRunID
	})
	store := &completionReplayStore{Store: stored}
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime, Output: &api.RunOutput{
				AgentID: "svc.agent", RunID: "run-1", Suspension: suspension,
			}},
		},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run-1")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
	require.ErrorContains(t, err, "does not match run parent")
	require.Nil(t, store.suspensionCommand)
}

func TestEnsureRunCompletionRejectsMissingChildLinkBeforeWriting(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "parent.agent", RunID: completionParentRunID,
		SessionID: "session", Status: session.RunStatusRunning,
	})
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "child.agent", RunID: "child", SessionID: "session",
		ParentRunID: completionParentRunID, Status: session.RunStatusRunning,
	})
	store := &completionReplayStore{
		Store: stored,
		listRun: func(runID string, page runlog.Page) runlog.Page {
			if runID != completionParentRunID {
				return page
			}
			filtered := page.Events[:0]
			for _, event := range page.Events {
				if event.Type != hooks.ChildRunLinked {
					filtered = append(filtered, event)
				}
			}
			page.Events = filtered
			return page
		},
	}
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime,
			Output: &api.RunOutput{AgentID: "child.agent", RunID: "child", Final: &model.Message{}},
		}},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "child")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
	require.ErrorContains(t, err, "matching child links, want 1")
	require.Nil(t, store.terminalCommand)
}

func TestEnsureRunCompletionDoesNotPublishWhenWorkflowTerminalWins(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	workflowTimestamp := ensuredCompletionTime.Add(-time.Second)
	bus := hooks.NewBus()
	published := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		published++
		return nil
	}))
	require.NoError(t, err)
	sink := &completionEventSink{}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: workflowTerminalWinsStore{Store: store, workflowTimestamp: workflowTimestamp},
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime,
			WorkflowError: errors.New("planner failed"),
		}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.Zero(t, published)
	require.Len(t, sink.events, 2)
	for _, event := range sink.events {
		require.Equal(t, workflowTimestamp, event.OccurredAt())
	}
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflowTimestamp, page.Events[1].Timestamp)
}

func TestEnsureRunCompletionDoesNotPublishWhenWorkflowSuspensionWins(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	suspension := suspensionContractFixtureWithContext(
		t,
		tools.Ident("svc.tools.lookup"),
		"svc.agent",
		"run-1",
		nil,
		nil,
	)
	workflowTimestamp := ensuredCompletionTime.Add(-time.Second)
	bus := hooks.NewBus()
	published := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		published++
		return nil
	}))
	require.NoError(t, err)
	sink := &completionEventSink{}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: workflowSuspensionWinsStore{Store: store, workflowTimestamp: workflowTimestamp},
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime, Output: &api.RunOutput{
			AgentID: "svc.agent", RunID: "run-1", Suspension: suspension,
		}}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run-1"))
	require.Zero(t, published)
	require.Len(t, sink.events, 2)
	for _, event := range sink.events {
		require.Equal(t, workflowTimestamp, event.OccurredAt())
	}
	page, err := store.ListRunRecords(t.Context(), "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflowTimestamp, page.Events[1].Timestamp)
}

func TestEnsureRunCompletionSuppressesConcurrentWinnerForEndedSession(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	sink := &completionEventSink{}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: workflowTerminalWinsStore{
			Store: store, workflowTimestamp: ensuredCompletionTime.Add(-time.Second), endSession: true,
		},
		Bus: hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime,
			WorkflowError: errors.New("engine-derived loser"),
		}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.Empty(t, sink.events)
}

func TestEnsureRunCompletionRejectsCorruptConcurrentWinner(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	winner := workflowTerminalWinsStore{
		Store: stored, workflowTimestamp: ensuredCompletionTime.Add(-time.Second),
	}
	store := &completionReplayStore{
		Store: winner,
		list: func(page runlog.Page) runlog.Page {
			page = cloneCompletionPage(page)
			page.Events[len(page.Events)-1].Payload = []byte(`{`)
			return page
		},
	}
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime,
			WorkflowError: errors.New("engine-derived loser"),
		}},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
}

func TestEnsureRunCompletionRejectsMismatchedConcurrentWinnerStatus(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{
		Store: workflowTerminalWinsStore{
			Store:             store,
			workflowTimestamp: ensuredCompletionTime.Add(-time.Second),
			reportedStatus:    session.RunStatusCompleted,
		},
		Bus: hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime,
			WorkflowError: errors.New("engine-derived loser"),
		}},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
	require.ErrorContains(t, err, `stored winner has status "failed", store reported "completed"`)
}

func TestEnsureRunCompletionRejectsDifferentTerminalForClosedRun(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusCompleted,
	})
	runtime := &Runtime{
		Store:  differentTerminalRepairStore{Store: stored},
		Bus:    hooks.NewBus(),
		Engine: completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
}

func TestEnsureRunCompletionDoesNotStoreRetrievalFailure(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	retrievalErr := errors.New("temporal unavailable")
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completionErr: retrievalErr,
		},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, retrievalErr)
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
}

func TestEnsureRunCompletionRejectsOpenWorkflow(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{
		Store:  store,
		Bus:    hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusRunning}},
	}

	err := runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, ErrRunCompletionNotReady)
}

func TestEnsureRunCompletionStopsRetryingWhenCallerCancels(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	called := make(chan struct{}, 1)
	runtime := &Runtime{
		Store: unavailableTerminalStore{Store: stored, called: called},
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: ensuredCompletionTime, WorkflowError: errors.New("planner failed"),
		}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- runtime.EnsureRunCompletion(ctx, "run")
	}()
	<-called
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

// cloneCompletionPage lets corruption tests change records without changing
// the integrated store that supplies them.
func cloneCompletionPage(page runlog.Page) runlog.Page {
	cloned := runlog.Page{NextCursor: page.NextCursor, Events: make([]*runlog.Event, len(page.Events))}
	for i, event := range page.Events {
		copy := *event
		copy.Payload = append([]byte(nil), event.Payload...)
		cloned.Events[i] = &copy
	}
	return cloned
}
