// prompt_refs_test.go verifies prompt attribution across child and continuation
// runs, strict relationship decoding, and exact storage retries.
package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentrun "goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type promptRefsStore struct {
	storage.Store
	runOverrides map[string]session.RunMeta
	recordPages  map[string]map[string]runlog.Page
	runReads     map[string]int
	recordReads  map[string]int
}

func TestResolvePromptRefsTraversesReachableRunsBreadthFirst(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	for _, meta := range []session.RunMeta{
		{RunID: "root", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning},
		{RunID: "child-1", AgentID: "child-agent-1", SessionID: "session", ParentRunID: "root", Status: session.RunStatusRunning},
		{RunID: "child-2", AgentID: "child-agent-2", SessionID: "session", ParentRunID: "root", Status: session.RunStatusRunning},
		{RunID: "grandchild", AgentID: "grandchild-agent", SessionID: "session", ParentRunID: "child-1", Status: session.RunStatusRunning},
	} {
		admitRunForTest(t, store, meta)
	}
	appendPromptEvent(t, store, "root", "root-prompt", "v1")
	appendPromptEvent(t, store, "child-1", "child-1-prompt", "v1")
	appendPromptEvent(t, store, "child-2", "child-2-prompt", "v2")
	appendPromptEvent(t, store, "child-2", "root-prompt", "v1")
	appendPromptEvent(t, store, "grandchild", "grandchild-prompt", "v1")
	admitRunForTest(t, store, session.RunMeta{RunID: "unrelated", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning})
	_, err := store.AppendRunRecord(t.Context(), &runlog.Event{
		EventKey: "unrelated-malformed", RunID: "unrelated", AgentID: "agent",
		SessionID: "session", Type: hooks.PromptRendered, Payload: rawjson.Message(`{`),
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	})
	require.NoError(t, err)

	refs, err := rt.ResolvePromptRefs(t.Context(), "session", "root")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: "root-prompt", Version: "v1"},
		{ID: "child-1-prompt", Version: "v1"},
		{ID: "child-2-prompt", Version: "v2"},
		{ID: "grandchild-prompt", Version: "v1"},
	}, refs)
}

func TestResolvePromptRefsPaginatesOneRun(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	admitRunForTest(t, store, session.RunMeta{
		RunID: "root", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	})
	for index := range 500 {
		_, err := store.AppendRunRecord(t.Context(), &runlog.Event{
			EventKey: fmt.Sprintf("irrelevant-%03d", index),
			RunID:    "root", AgentID: "agent", SessionID: "session",
			Type: "irrelevant", Payload: rawjson.Message(`{}`),
			Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		})
		require.NoError(t, err)
	}
	appendPromptEvent(t, store, "root", "second-page", "v1")

	refs, err := rt.ResolvePromptRefs(t.Context(), "session", "root")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{{ID: "second-page", Version: "v1"}}, refs)
}

func TestResolvePromptRefsRequiresRootInSession(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	admitRunForTest(t, store, session.RunMeta{
		RunID: "root", AgentID: "agent", SessionID: "other-session", Status: session.RunStatusRunning,
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "root")
	require.ErrorIs(t, err, session.ErrRunNotFound)

	_, err = rt.ResolvePromptRefs(t.Context(), "", "root")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestResolvePromptRefsSupportsSessionlessRun(t *testing.T) {
	store := newTestStore()
	rt := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "svc.agent.system",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "hello",
		Version:  "v1",
	}))
	require.NoError(t, rt.RunOneShot(t.Context(), OneShotRunInput{
		AgentID: "svc.agent",
		RunID:   "one-shot",
	}, func(ctx context.Context) error {
		_, err := rt.PromptRegistry.Render(ctx, "svc.agent.system", prompt.Scope{}, nil)
		return err
	}))

	refs, err := rt.ResolvePromptRefs(t.Context(), "", "one-shot")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{{ID: "svc.agent.system", Version: "v1"}}, refs)
}

func TestResolvePromptRefsTraversesSessionlessChild(t *testing.T) {
	store := newTestStore()
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	parent := session.RunStart{
		AgentID:   "parent-agent",
		RunID:     "parent",
		StartedAt: startedAt,
	}
	parentStarted := testHookRecord(t, hooks.NewRunStartedEvent(
		parent.RunID,
		agent.Ident(parent.AgentID),
		"",
		"",
		"",
		nil,
	), "parent-start", startedAt)
	_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
		Run: parent, Started: parentStarted,
	})
	require.NoError(t, err)
	child := session.RunStart{
		AgentID:     "child-agent",
		RunID:       "child",
		ParentRunID: parent.RunID,
		StartedAt:   startedAt,
	}
	linked := testHookRecord(t, hooks.NewChildRunLinkedEvent(
		parent.RunID,
		agent.Ident(parent.AgentID),
		"",
		"child.lookup",
		"call-child",
		child.RunID,
		agent.Ident(child.AgentID),
	), "child-link", startedAt)
	childStarted := testHookRecord(t, hooks.NewRunStartedEvent(
		child.RunID,
		agent.Ident(child.AgentID),
		"",
		parent.RunID,
		"",
		nil,
	), "child-start", startedAt)
	_, err = store.StartOneShotChildRun(t.Context(), storage.OneShotChildRunStart{
		Run: child, ParentLinked: linked, Started: childStarted,
	})
	require.NoError(t, err)
	appendPromptEvent(t, store, parent.RunID, "parent-prompt", "v1")
	appendPromptEvent(t, store, child.RunID, "child-prompt", "v2")

	refs, err := New(store).ResolvePromptRefs(t.Context(), "", parent.RunID)
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: "parent-prompt", Version: "v1"},
		{ID: "child-prompt", Version: "v2"},
	}, refs)
}

func TestResolvePromptRefsTreatsStoppedRunAsEmpty(t *testing.T) {
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)
	_, err = store.EndSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)
	meta := session.RunMeta{
		RunID: "stopped", AgentID: "agent", SessionID: "session",
		Status: session.RunStatusCanceled,
	}
	startStoppedRunForTest(t, store, meta)
	rt := New(store)

	refs, err := rt.ResolvePromptRefs(t.Context(), "session", meta.RunID)
	require.NoError(t, err)
	require.Empty(t, refs)

	stored, err := store.LoadRun(t.Context(), meta.RunID)
	require.NoError(t, err)
	for _, recordType := range []runlog.Type{
		hooks.RunStarted,
		hooks.PromptRendered,
		hooks.ChildRunLinked,
	} {
		t.Run(string(recordType), func(t *testing.T) {
			record := runStartedRecord(t, stored, "", "invalid-record")
			record.Type = recordType
			rt.Store = &promptRefsStore{
				Store: store,
				recordPages: map[string]map[string]runlog.Page{
					meta.RunID: {"": {Events: []*runlog.Event{record}}},
				},
			}
			_, err := rt.ResolvePromptRefs(t.Context(), "session", meta.RunID)
			require.ErrorIs(t, err, errPromptRefsCorrupt)
			require.ErrorContains(t, err, meta.RunID)
		})
	}
}

func TestResolvePromptRefsRejectsCorruptStoppedCompletion(t *testing.T) {
	tests := []struct {
		name       string
		completion func(session.RunMeta) *runlog.Event
	}{
		{
			name: "status",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"success", agentrun.PhaseCompleted, meta.Labels, nil, nil,
				), "stopped", meta.StartedAt)
			},
		},
		{
			name: "reason",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"canceled", agentrun.PhaseCanceled, meta.Labels, context.Canceled,
					&agentrun.Cancellation{Reason: agentrun.CancellationReasonUserRequested},
				), "stopped", meta.StartedAt)
			},
		},
		{
			name: "labels",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"canceled", agentrun.PhaseCanceled, map[string]string{"site": "other"}, context.Canceled,
					&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
				), "stopped", meta.StartedAt)
			},
		},
		{
			name: "time",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"canceled", agentrun.PhaseCanceled, meta.Labels, context.Canceled,
					&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
				), "stopped", meta.StartedAt.Add(time.Millisecond))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore()
			_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
			require.NoError(t, err)
			_, err = store.EndSession(t.Context(), "session", time.Now().UTC())
			require.NoError(t, err)
			startStoppedRunForTest(t, store, session.RunMeta{
				RunID: "stopped", AgentID: "agent", SessionID: "session",
				Status: session.RunStatusCanceled, Labels: map[string]string{"site": "one"},
			})
			meta, err := store.LoadRun(t.Context(), "stopped")
			require.NoError(t, err)
			started := testHookRecord(t, hooks.NewRunStartedEvent(
				meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
				meta.ParentRunID, "", meta.Labels,
			), "start", meta.StartedAt)
			rt := New(&promptRefsStore{
				Store: store,
				recordPages: map[string]map[string]runlog.Page{
					meta.RunID: {"": {Events: []*runlog.Event{started, test.completion(meta)}}},
				},
			})

			_, err = rt.ResolvePromptRefs(t.Context(), meta.SessionID, meta.RunID)
			require.ErrorIs(t, err, errPromptRefsCorrupt)
			require.ErrorContains(t, err, "stopped")
		})
	}
}

func TestResolvePromptRefsTraversesStoppedChildAsEmpty(t *testing.T) {
	store := newTestStore()
	parent := session.RunMeta{
		RunID: "parent", AgentID: "parent-agent", SessionID: "session",
		Status: session.RunStatusRunning,
	}
	admitRunForTest(t, store, parent)
	_, err := store.EndSession(t.Context(), parent.SessionID, time.Now().UTC())
	require.NoError(t, err)
	startStoppedRunForTest(t, store, session.RunMeta{
		RunID: "child", AgentID: "child-agent", SessionID: parent.SessionID,
		ParentRunID: parent.RunID, Status: session.RunStatusCanceled,
	})

	refs, err := New(store).ResolvePromptRefs(t.Context(), parent.SessionID, parent.RunID)
	require.NoError(t, err)
	require.Empty(t, refs)
}

func TestResolvePromptRefsTraversesContinuationPredecessors(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	for _, meta := range []session.RunMeta{
		{RunID: "predecessor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning},
		{
			RunID: "predecessor-child", AgentID: "agent", SessionID: "session",
			ParentRunID: "predecessor", Status: session.RunStatusRunning,
		},
	} {
		admitRunForTest(t, store, meta)
	}
	appendPromptEvent(t, store, "predecessor", "predecessor-prompt", "v1")
	appendPromptEvent(t, store, "predecessor", "successor-prompt", "v2")
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "predecessor", session.RunSuspension{
		ID: "predecessor-suspension", Data: []byte(`{}`),
	}))
	admitContinuedRunForTest(t, store, session.RunMeta{
		RunID: "successor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	}, "predecessor")
	appendPromptEvent(t, store, "successor", "successor-prompt", "v2")
	appendPromptEvent(t, store, "predecessor-child", "child-prompt", "v3")
	refs, err := rt.ResolvePromptRefs(t.Context(), "session", "successor")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: "successor-prompt", Version: "v2"},
		{ID: "predecessor-prompt", Version: "v1"},
		{ID: "child-prompt", Version: "v3"},
	}, refs)
}

func TestResolvePromptRefsRejectsMalformedStartRecord(t *testing.T) {
	store := newTestStore()
	meta := session.RunMeta{
		RunID: "successor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	}
	admitRunForTest(t, store, meta)
	malformed := &runlog.Event{
		EventKey: "malformed-start", RunID: meta.RunID, AgentID: agent.Ident(meta.AgentID),
		SessionID: meta.SessionID, Type: hooks.RunStarted,
		Payload:   rawjson.Message(`{"parent_run_id":"","predecessor_run_id":"predecessor","extra":true}`),
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	rt := New(&promptRefsStore{
		Store: store,
		recordPages: map[string]map[string]runlog.Page{
			meta.RunID: {"": {Events: []*runlog.Event{malformed}}},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "successor")
	require.ErrorContains(t, err, `unknown field "extra"`)
}

func TestResolvePromptRefsRejectsCorruptContinuationRelationship(t *testing.T) {
	tests := []struct {
		name        string
		predecessor session.RunMeta
	}{
		{
			name: "different session",
			predecessor: session.RunMeta{
				RunID: "predecessor", AgentID: "agent", SessionID: "other-session",
				Status: session.RunStatusSuspended,
			},
		},
		{
			name: "different agent",
			predecessor: session.RunMeta{
				RunID: "predecessor", AgentID: "other-agent", SessionID: "session",
				Status: session.RunStatusSuspended,
			},
		},
		{
			name: "different parent",
			predecessor: session.RunMeta{
				RunID: "predecessor", AgentID: "agent", SessionID: "session",
				ParentRunID: "other-parent", Status: session.RunStatusSuspended,
			},
		},
		{
			name: "predecessor did not suspend",
			predecessor: session.RunMeta{
				RunID: "predecessor", AgentID: "agent", SessionID: "session",
				Status: session.RunStatusCompleted,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore()
			admitRunForTest(t, store, session.RunMeta{
				RunID: "predecessor", AgentID: "agent", SessionID: "session", Status: session.RunStatusSuspended,
			})
			admitContinuedRunForTest(t, store, session.RunMeta{
				RunID: "successor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
			}, "predecessor")
			rt := New(&promptRefsStore{
				Store: store,
				runOverrides: map[string]session.RunMeta{
					"predecessor": test.predecessor,
				},
			})

			_, err := rt.ResolvePromptRefs(t.Context(), "session", "successor")
			require.ErrorIs(t, err, errPromptRefsCorrupt)
			require.ErrorContains(t, err, "invalid predecessor identity or status")
		})
	}
}

func TestResolvePromptRefsRejectsMissingContinuationPredecessor(t *testing.T) {
	store := newTestStore()
	meta := session.RunMeta{
		RunID: "successor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	}
	admitRunForTest(t, store, meta)
	rt := New(&promptRefsStore{
		Store: store,
		recordPages: map[string]map[string]runlog.Page{
			meta.RunID: {"": {Events: []*runlog.Event{
				runStartedRecord(t, meta, "missing", "continued-start"),
			}}},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "successor")
	require.ErrorIs(t, err, errPromptRefsCorrupt)
}

func TestResolvePromptRefsRejectsSelfContinuationPredecessor(t *testing.T) {
	store := newTestStore()
	meta := session.RunMeta{
		RunID: "run", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	}
	admitRunForTest(t, store, meta)
	rt := New(&promptRefsStore{
		Store: store,
		recordPages: map[string]map[string]runlog.Page{
			meta.RunID: {"": {Events: []*runlog.Event{runStartedRecord(t, meta, meta.RunID, "self-start")}}},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "run")
	require.ErrorIs(t, err, errPromptRefsCorrupt)
	require.ErrorContains(t, err, "own continuation predecessor")
}

func TestResolvePromptRefsRejectsContinuationCycle(t *testing.T) {
	store := newTestStore()
	first := session.RunMeta{
		RunID: "first", AgentID: "agent", SessionID: "session", Status: session.RunStatusSuspended,
	}
	second := session.RunMeta{
		RunID: "second", AgentID: "agent", SessionID: "session", Status: session.RunStatusSuspended,
	}
	admitRunForTest(t, store, first)
	admitRunForTest(t, store, second)
	rt := New(&promptRefsStore{
		Store: store,
		recordPages: map[string]map[string]runlog.Page{
			first.RunID: {"": {Events: []*runlog.Event{
				runStartedRecord(t, first, second.RunID, "first-start"),
			}}},
			second.RunID: {"": {Events: []*runlog.Event{
				runStartedRecord(t, second, first.RunID, "second-start"),
			}}},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "first")
	require.ErrorIs(t, err, errPromptRefsCorrupt)
	require.ErrorContains(t, err, "relationship cycle")
}

func TestResolvePromptRefsRejectsMultipleStartRecordsAcrossPages(t *testing.T) {
	store := newTestStore()
	meta := session.RunMeta{
		RunID: "successor", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning,
	}
	admitRunForTest(t, store, meta)
	rt := New(&promptRefsStore{
		Store: store,
		recordPages: map[string]map[string]runlog.Page{
			meta.RunID: {
				"": {
					Events:     []*runlog.Event{runStartedRecord(t, meta, "predecessor-1", "start-1")},
					NextCursor: "next",
				},
				"next": {
					Events: []*runlog.Event{runStartedRecord(t, meta, "predecessor-2", "start-2")},
				},
			},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "successor")
	require.ErrorIs(t, err, errPromptRefsCorrupt)
	require.ErrorContains(t, err, "more than one start record")
}

func TestResolvePromptRefsAllowsAcyclicConvergence(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	for _, meta := range []session.RunMeta{
		{RunID: "root", AgentID: "parent-agent", SessionID: "session", Status: session.RunStatusRunning},
		{
			RunID: "predecessor", AgentID: "child-agent", SessionID: "session",
			ParentRunID: "root", Status: session.RunStatusRunning,
		},
	} {
		admitRunForTest(t, store, meta)
	}
	appendPromptEvent(t, store, "root", "root-prompt", "v1")
	appendPromptEvent(t, store, "predecessor", "predecessor-prompt", "v1")
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "predecessor", session.RunSuspension{
		ID: "predecessor-suspension", Data: []byte(`{}`),
	}))
	for _, meta := range []session.RunMeta{
		{
			RunID: "child-1", AgentID: "child-agent", SessionID: "session",
			ParentRunID: "root", Status: session.RunStatusRunning,
		},
		{
			RunID: "child-2", AgentID: "child-agent", SessionID: "session",
			ParentRunID: "root", Status: session.RunStatusRunning,
		},
	} {
		admitContinuedRunForTest(t, store, meta, "predecessor")
	}
	appendPromptEvent(t, store, "child-1", "child-1-prompt", "v1")
	appendPromptEvent(t, store, "child-2", "child-2-prompt", "v1")
	tracked := &promptRefsStore{
		Store:       store,
		runReads:    make(map[string]int),
		recordReads: make(map[string]int),
	}
	rt.Store = tracked

	refs, err := rt.ResolvePromptRefs(t.Context(), "session", "root")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: "root-prompt", Version: "v1"},
		{ID: "predecessor-prompt", Version: "v1"},
		{ID: "child-1-prompt", Version: "v1"},
		{ID: "child-2-prompt", Version: "v1"},
	}, refs)
	require.Equal(t, 1, tracked.runReads["predecessor"])
	require.Equal(t, 1, tracked.recordReads["predecessor"])
}

func TestResolvePromptRefsRejectsCorruptChildRelationship(t *testing.T) {
	tests := []struct {
		name  string
		child session.RunMeta
	}{
		{
			name: "different session",
			child: session.RunMeta{
				RunID: "child", AgentID: "child-agent", SessionID: "other-session",
				ParentRunID: "parent", Status: session.RunStatusRunning,
			},
		},
		{
			name: "different agent",
			child: session.RunMeta{
				RunID: "child", AgentID: "other-agent", SessionID: "session",
				ParentRunID: "parent", Status: session.RunStatusRunning,
			},
		},
		{
			name: "different parent",
			child: session.RunMeta{
				RunID: "child", AgentID: "child-agent", SessionID: "session",
				ParentRunID: "other-parent", Status: session.RunStatusRunning,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore()
			admitRunForTest(t, store, session.RunMeta{
				RunID: "parent", AgentID: "parent-agent", SessionID: "session", Status: session.RunStatusRunning,
			})
			admitRunForTest(t, store, session.RunMeta{
				RunID: "child", AgentID: "child-agent", SessionID: "session",
				ParentRunID: "parent", Status: session.RunStatusRunning,
			})
			rt := New(&promptRefsStore{
				Store: store,
				runOverrides: map[string]session.RunMeta{
					"child": test.child,
				},
			})

			_, err := rt.ResolvePromptRefs(t.Context(), "session", "parent")
			require.ErrorIs(t, err, errPromptRefsCorrupt)
		})
	}
}

func TestResolvePromptRefsRejectsMissingRoot(t *testing.T) {
	rt := New(newTestStore())
	_, err := rt.ResolvePromptRefs(t.Context(), "session", "missing")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func appendPromptEvent(t *testing.T, store storage.Store, runID string, promptID prompt.Ident, version string) {
	t.Helper()
	meta, err := store.LoadRun(t.Context(), runID)
	require.NoError(t, err)
	event := hooks.NewPromptRenderedEvent(
		runID, agent.Ident(meta.AgentID), meta.SessionID, promptID, version, prompt.Scope{},
	)
	suffix := string(promptID) + "-" + version
	record, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
		EventKey:    "event-" + event.RunID() + "-" + suffix,
		TimestampMS: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	_, err = store.AppendRunRecord(t.Context(), &runlog.Event{
		EventKey: record.EventKey, RunID: record.RunID, AgentID: record.AgentID,
		SessionID: record.SessionID, TurnID: record.TurnID, Type: record.Type,
		Payload: record.Payload, Timestamp: time.UnixMilli(record.TimestampMS).UTC(),
	})
	require.NoError(t, err)
}

func startStoppedRunForTest(t *testing.T, store storage.Store, meta session.RunMeta) {
	t.Helper()
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	start := session.RunStart{
		AgentID: meta.AgentID, RunID: meta.RunID, SessionID: meta.SessionID,
		ParentRunID: meta.ParentRunID, StartedAt: startedAt, Labels: meta.Labels,
	}
	started := testHookRecord(t, hooks.NewRunStartedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		meta.ParentRunID,
		"",
		meta.Labels,
	), "start", startedAt)
	canceled := testHookRecord(t, hooks.NewRunCompletedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		"canceled",
		agentrun.PhaseCanceled,
		meta.Labels,
		context.Canceled,
		&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
	), "stopped", startedAt)
	if meta.ParentRunID == "" {
		result, err := store.StartRootRun(t.Context(), storage.RootRunStart{
			Run: start, Started: started, Canceled: canceled,
		})
		require.NoError(t, err)
		require.Equal(t, session.RunStartStop, result.Outcome)
		return
	}
	parent, err := store.LoadRun(t.Context(), meta.ParentRunID)
	require.NoError(t, err)
	linked := testHookRecord(t, hooks.NewChildRunLinkedEvent(
		meta.ParentRunID,
		agent.Ident(parent.AgentID),
		meta.SessionID,
		"test.child",
		"call-"+meta.RunID,
		meta.RunID,
		agent.Ident(meta.AgentID),
	), "child-link-"+meta.RunID, startedAt)
	result, err := store.StartChildRun(t.Context(), storage.ChildRunStart{
		Run: start, ParentLinked: linked, Started: started, Canceled: canceled,
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartStop, result.Outcome)
}

func runStartedRecord(
	t *testing.T,
	meta session.RunMeta,
	predecessorRunID, eventKey string,
) *runlog.Event {
	t.Helper()
	return testHookRecord(t, hooks.NewRunStartedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		meta.ParentRunID,
		predecessorRunID,
		meta.Labels,
	), eventKey, time.Now().UTC().Truncate(time.Millisecond))
}

// LoadRun replaces selected stored identities so tests can prove that prompt
// traversal rejects corrupt database results.
func (s *promptRefsStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	if s.runReads != nil {
		s.runReads[runID]++
	}
	if run, ok := s.runOverrides[runID]; ok {
		return run, nil
	}
	return s.Store.LoadRun(ctx, runID)
}

// ListRunRecords counts reads when a test needs to prove that converging paths
// share one traversal of the related run.
func (s *promptRefsStore) ListRunRecords(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	if s.recordReads != nil {
		s.recordReads[runID]++
	}
	if pages, ok := s.recordPages[runID]; ok {
		if page, found := pages[cursor]; found {
			return page, nil
		}
	}
	return s.Store.ListRunRecords(ctx, runID, cursor, limit)
}
