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
	seedEndID, err := publishLiteralHistory(t.Context(), store, storage.SeedDeclaration{
		AgentID: parent.AgentID, RunID: parent.RunID, Kind: storage.SeedLiteral,
	}, nil)
	require.NoError(t, err)
	parent.SeedEndID = seedEndID
	parentStarted := testHookRecord(t, hooks.NewRunStartedEvent(
		parent.RunID,
		agent.Ident(parent.AgentID),
		"",
		"",
		"",
		nil,
	), "parent-start", startedAt)
	_, err = store.StartOneShotRun(t.Context(), storage.OneShotRunStart{RequestDigest: [32]byte{1},
		Run: parent, Started: parentStarted,
	})
	require.NoError(t, err)
	child := session.RunStart{
		AgentID:     "child-agent",
		RunID:       "child",
		ParentRunID: parent.RunID,
		StartedAt:   startedAt,
	}
	child.SeedEndID, err = publishLiteralHistory(t.Context(), store, storage.SeedDeclaration{
		AgentID: child.AgentID, RunID: child.RunID, Kind: storage.SeedLiteral,
	}, nil)
	require.NoError(t, err)
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
	_, err = store.StartOneShotChildRun(t.Context(), storage.OneShotChildRunStart{RequestDigest: [32]byte{1},
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
	require.Equal(t, session.RunStatusRunning, stored.Status)
	finished := stored.StartedAt.Add(time.Second)
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: meta.RunID, Status: session.RunStatusCanceled,
		Record: testHookRecord(t, hooks.NewRunCompletedEvent(meta.RunID, agent.Ident(meta.AgentID), meta.SessionID, "canceled", agentrun.PhaseCanceled, stored.Labels, context.Canceled, &agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded}), "completed", finished),
	})
	require.NoError(t, err)
	refs, err = rt.ResolvePromptRefs(t.Context(), "session", meta.RunID)
	require.NoError(t, err)
	require.Empty(t, refs)
	stored, err = store.LoadRun(t.Context(), meta.RunID)
	require.NoError(t, err)
	for _, recordType := range []runlog.Type{
		hooks.RunStarted,
		hooks.PromptRendered,
		hooks.ChildRunLinked,
	} {
		t.Run(string(recordType), func(t *testing.T) {
			record := runStartedRecord(t, store, stored, "", "invalid-record")
			record.Type = recordType
			if recordType == hooks.RunStarted {
				record.Timestamp = stored.StartedAt.Add(time.Millisecond)
			}
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
				), "stopped", meta.StartedAt.Add(time.Second))
			},
		},
		{
			name: "reason",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"canceled", agentrun.PhaseCanceled, meta.Labels, context.Canceled,
					&agentrun.Cancellation{Reason: agentrun.CancellationReasonUserRequested},
				), "stopped", meta.StartedAt.Add(time.Second))
			},
		},
		{
			name: "labels",
			completion: func(meta session.RunMeta) *runlog.Event {
				return testHookRecord(t, hooks.NewRunCompletedEvent(
					meta.RunID, agent.Ident(meta.AgentID), meta.SessionID,
					"canceled", agentrun.PhaseCanceled, map[string]string{"site": "other"}, context.Canceled,
					&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
				), "stopped", meta.StartedAt.Add(time.Second))
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
			finished := meta.StartedAt.Add(time.Second)
			_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
				RunID: meta.RunID, Status: session.RunStatusCanceled,
				Record: testHookRecord(t, hooks.NewRunCompletedEvent(meta.RunID, agent.Ident(meta.AgentID), meta.SessionID, "canceled", agentrun.PhaseCanceled, meta.Labels, context.Canceled, &agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded}), "completed", finished),
			})
			require.NoError(t, err)
			meta, err = store.LoadRun(t.Context(), "stopped")
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
	for _, stoppedParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped-parent-%t", stoppedParent), func(t *testing.T) {
			store := newTestStore()
			parent := session.RunMeta{
				RunID: "parent", AgentID: "parent-agent", SessionID: "session",
				Status: session.RunStatusRunning,
			}
			if stoppedParent {
				_, err := store.CreateSession(t.Context(), parent.SessionID, time.Now().UTC())
				require.NoError(t, err)
				_, err = store.EndSession(t.Context(), parent.SessionID, time.Now().UTC())
				require.NoError(t, err)
				startStoppedRunForTest(t, store, parent)
			} else {
				admitRunForTest(t, store, parent)
				_, err := store.EndSession(t.Context(), parent.SessionID, time.Now().UTC())
				require.NoError(t, err)
			}
			startStoppedRunForTest(t, store, session.RunMeta{
				RunID: "child", AgentID: "child-agent", SessionID: parent.SessionID,
				ParentRunID: parent.RunID, Status: session.RunStatusCanceled,
			})

			refs, err := New(store).ResolvePromptRefs(t.Context(), parent.SessionID, parent.RunID)
			require.NoError(t, err)
			require.Empty(t, refs)
		})
	}
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

// A resumed child keeps the original call's prompt history even when its
// execution parent changes. An independent call is not a valid replacement.
func TestResolvePromptRefsContinuedChildKeepsExactCallAcrossParents(t *testing.T) {
	store := newTestStore()
	parent := session.RunMeta{RunID: "parent-0", AgentID: "parent-agent", SessionID: "session", Status: session.RunStatusRunning}
	admitRunForTest(t, store, parent)
	for _, runID := range []string{"child-0", "independent-child"} {
		admitRunForTest(t, store, session.RunMeta{
			RunID: runID, AgentID: "child-agent", SessionID: "session",
			ParentRunID: parent.RunID, Status: session.RunStatusRunning,
		})
		appendPromptEvent(t, store, runID, prompt.Ident(runID+"-prompt"), "v1")
		require.NoError(t, storeSuspensionForTest(t.Context(), store, runID, session.RunSuspension{
			ID: runID + "-suspension", Data: []byte(`{}`),
		}))
	}
	require.NoError(t, storeSuspensionForTest(t.Context(), store, parent.RunID, session.RunSuspension{
		ID: "parent-suspension", Data: []byte(`{}`),
	}))
	admitContinuedRunForTest(t, store, session.RunMeta{
		RunID: "parent-1", AgentID: parent.AgentID, SessionID: parent.SessionID, Status: session.RunStatusRunning,
	}, parent.RunID)
	child := session.RunMeta{
		RunID: "child-1", AgentID: "child-agent", SessionID: "session",
		ParentRunID: "parent-1", Status: session.RunStatusRunning,
	}
	admitPromptChildContinuationForTest(t, store, child, "child-0", "call-child-0")
	appendPromptEvent(t, store, child.RunID, "continued-prompt", "v2")
	tracked := &promptRefsStore{Store: store, runReads: make(map[string]int), recordReads: make(map[string]int)}
	rt := New(tracked)

	refs, err := rt.ResolvePromptRefs(t.Context(), child.SessionID, child.RunID)
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: "continued-prompt", Version: "v2"},
		{ID: "child-0-prompt", Version: "v1"},
	}, refs)
	require.Equal(t, map[string]int{"child-1": 1, "child-0": 1}, tracked.recordReads)
	require.Equal(t, map[string]int{"child-1": 1, "child-0": 1}, tracked.runReads)

	// Both old children have the same Session, agent, and execution parent.
	// Replacing the predecessor still contradicts this child's accepted seed.
	tracked.recordPages = map[string]map[string]runlog.Page{
		child.RunID: {"": {Events: []*runlog.Event{
			runStartedRecord(t, store, child, "independent-child", "start"),
		}}},
	}
	_, err = rt.ResolvePromptRefs(t.Context(), child.SessionID, child.RunID)
	require.ErrorIs(t, err, errPromptRefsCorrupt)
	require.ErrorContains(t, err, "accepted history does not match predecessor")
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
				runStartedRecord(t, store, meta, "missing", "continued-start"),
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
			meta.RunID: {"": {Events: []*runlog.Event{runStartedRecord(t, store, meta, meta.RunID, "self-start")}}},
		},
	})

	_, err := rt.ResolvePromptRefs(t.Context(), "session", "run")
	require.ErrorIs(t, err, errPromptRefsCorrupt)
	require.ErrorContains(t, err, "predecessor run id must differ")
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
				runStartedRecord(t, store, first, second.RunID, "first-start"),
			}}},
			second.RunID: {"": {Events: []*runlog.Event{
				runStartedRecord(t, store, second, first.RunID, "second-start"),
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
					Events:     []*runlog.Event{runStartedRecord(t, store, meta, "predecessor-1", "start-1")},
					NextCursor: "next",
				},
				"next": {
					Events: []*runlog.Event{runStartedRecord(t, store, meta, "predecessor-2", "start-2")},
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
	} {
		admitPromptChildContinuationForTest(t, store, meta, "predecessor", "call-predecessor")
	}
	appendPromptEvent(t, store, "child-1", "child-1-prompt", "v1")
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

// admitPromptChildContinuationForTest publishes one small fixture's saved
// history and admits its child through the real store. The supplied call ID
// keeps the predecessor's original parent call instead of creating a new call.
func admitPromptChildContinuationForTest(t *testing.T, store storage.Store, meta session.RunMeta, predecessorRunID, callID string) {
	t.Helper()
	page, err := store.ListRunRecords(t.Context(), predecessorRunID, "", 500)
	require.NoError(t, err)
	require.Empty(t, page.NextCursor)
	require.NotEmpty(t, page.Events)
	declaration := storage.SeedDeclaration{
		AgentID: meta.AgentID, RunID: meta.RunID, SessionID: meta.SessionID,
		CommandID: meta.RunID, AttemptID: meta.RunID, Kind: storage.SeedContinuation,
		SourceRunID: predecessorRunID, SourceEndID: page.Events[len(page.Events)-1].ID,
	}
	seed, err := store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err)
	writer := initialHistoryWriter{store: store, runID: meta.RunID, attemptID: meta.RunID, endID: storage.EmptySeedEndID}
	require.NoError(t, writer.appendPrefix(t.Context(), seed.Source))
	seedEndID := writer.endID
	require.NoError(t, writer.publish(t.Context(), []byte(`{}`)))
	parent, err := store.LoadRun(t.Context(), meta.ParentRunID)
	require.NoError(t, err)
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	start := session.RunStart{
		AgentID: meta.AgentID, RunID: meta.RunID, SessionID: meta.SessionID,
		ParentRunID: meta.ParentRunID, PredecessorRunID: predecessorRunID,
		SeedEndID: seedEndID, StartedAt: startedAt, Labels: meta.Labels,
	}
	started := testHookRecord(t, hooks.NewRunStartedEvent(
		meta.RunID, agent.Ident(meta.AgentID), meta.SessionID, meta.ParentRunID, predecessorRunID, meta.Labels,
	), "start", startedAt)
	linked := testHookRecord(t, hooks.NewChildRunLinkedEvent(
		parent.RunID, agent.Ident(parent.AgentID), meta.SessionID, "test.child", callID, meta.RunID, agent.Ident(meta.AgentID),
	), "child-link-"+meta.RunID, startedAt)
	canceled := testStartCancellationRecord(t, start)
	result, err := store.StartChildRun(t.Context(), storage.ChildRunStart{
		RequestDigest: [32]byte{1}, Run: start, ParentLinked: linked, Started: started, Cancellation: canceled,
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, result.Outcome)
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
	seedEndID, err := publishLiteralHistory(t.Context(), store, storage.SeedDeclaration{
		AgentID: meta.AgentID, RunID: meta.RunID, SessionID: meta.SessionID, Kind: storage.SeedLiteral,
	}, nil)
	require.NoError(t, err)
	start.SeedEndID = seedEndID
	started := testHookRecord(t, hooks.NewRunStartedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		meta.ParentRunID,
		"",
		meta.Labels,
	), "start", startedAt)
	canceled := testStartCancellationRecord(t, start)
	if meta.ParentRunID == "" {
		result, err := store.StartRootRun(t.Context(), storage.RootRunStart{RequestDigest: [32]byte{1},
			Run: start, Started: started, Cancellation: canceled,
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
	result, err := store.StartChildRun(t.Context(), storage.ChildRunStart{RequestDigest: [32]byte{1},
		Run: start, ParentLinked: linked, Started: started, Cancellation: canceled,
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartStop, result.Outcome)
}

// runStartedRecord preserves the admitted timestamp while a test changes
// relationships or identity, so validation reaches the intended corruption.
func runStartedRecord(
	t *testing.T,
	store storage.Store,
	meta session.RunMeta,
	predecessorRunID, eventKey string,
) *runlog.Event {
	t.Helper()
	stored, err := store.LoadRun(t.Context(), meta.RunID)
	require.NoError(t, err)
	return testHookRecord(t, hooks.NewRunStartedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		meta.ParentRunID,
		predecessorRunID,
		meta.Labels,
	), eventKey, stored.StartedAt)
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
