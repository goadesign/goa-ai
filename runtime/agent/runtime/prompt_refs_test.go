package runtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestResolvePromptRefsTraversesReachableRunsBreadthFirst(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	for _, meta := range []session.RunMeta{
		{RunID: "root", AgentID: "agent", SessionID: "session", Status: session.RunStatusRunning},
		{RunID: "child-1", AgentID: "agent", SessionID: "session", ParentRunID: "root", Status: session.RunStatusRunning},
		{RunID: "child-2", AgentID: "agent", SessionID: "session", ParentRunID: "root", Status: session.RunStatusRunning},
		{RunID: "grandchild", AgentID: "agent", SessionID: "session", ParentRunID: "child-1", Status: session.RunStatusRunning},
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
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)

	refs, err := rt.ResolvePromptRefs(t.Context(), "root")
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
			Type: "irrelevant", Payload: rawjson.Message(`{}`), Timestamp: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	appendPromptEvent(t, store, "root", "second-page", "v1")

	refs, err := rt.ResolvePromptRefs(t.Context(), "root")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{{ID: "second-page", Version: "v1"}}, refs)
}

func TestResolvePromptRefsRejectsMissingRoot(t *testing.T) {
	rt := New(newTestStore())
	_, err := rt.ResolvePromptRefs(t.Context(), "missing")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func appendPromptEvent(t *testing.T, store storage.Store, runID string, promptID prompt.Ident, version string) {
	t.Helper()
	event := hooks.NewPromptRenderedEvent(
		runID, agent.Ident("agent"), "session", promptID, version, prompt.Scope{},
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
