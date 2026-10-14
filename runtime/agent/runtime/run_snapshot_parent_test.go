// These tests preserve local schedule validation while accepting a child run's
// exact outside parent call only after its saved parent schedule and link agree.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestParentToolCallForChildRequiresAcceptedLink(t *testing.T) {
	t.Parallel()
	child := session.RunMeta{RunID: "child", ParentRunID: "parent", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent("worker"))}, SessionID: "session"}
	schedule := hooks.NewToolCallScheduledEvent("parent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("caller"))}, "session", "worker.lookup", "parent-call", nil, "", "", 0)
	link := hooks.NewChildRunLinkedEvent("parent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("caller"))}, "session", "worker.lookup", "parent-call", "child", "worker")
	result := hooks.NewToolResultReceivedEvent("parent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("caller"))}, "session", "parent", "worker.lookup", "parent-call", "", nil, nil, nil, "", nil, 0, nil, nil)
	cases := []struct {
		name   string
		events []hooks.Event
		child  session.RunMeta
		error  string
	}{
		{"accepted child after completed parent", []hooks.Event{schedule, link, result}, child, ""},
		{"missing link", []hooks.Event{schedule}, child, "not linked"},
		{"missing schedule", []hooks.Event{link}, child, "no matching active"},
		{"completed before link", []hooks.Event{schedule, result, link}, child, "no matching active"},
		{"wrong call", []hooks.Event{schedule, hooks.NewChildRunLinkedEvent("parent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("caller"))}, "session", "worker.lookup", "other-call", "child", "worker")}, child, "no matching active"},
		{"wrong operation", []hooks.Event{schedule, hooks.NewChildRunLinkedEvent("parent", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("caller"))}, "session", "worker.other", "parent-call", "child", "worker")}, child, "no matching active"},
		{"wrong worker", []hooks.Event{schedule, link}, session.RunMeta{RunID: "child", ParentRunID: "parent", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("other-worker"))}, SessionID: "session"}, "no matching active"},
		{"wrong session", []hooks.Event{schedule, link}, session.RunMeta{RunID: "child", ParentRunID: "parent", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("worker"))}, SessionID: "other-session"}, "no matching active"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := make([]*runlog.Event, len(test.events))
			for index, event := range test.events {
				events[index] = snapshotRunlogEvent(t, event, index+1)
			}
			parentID, err := parentToolCallForChild(events, test.child)
			if test.error != "" {
				require.ErrorContains(t, err, test.error)
				assert.Empty(t, parentID)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "parent-call", parentID)
		})
	}
}

func TestNewRunSnapshotAcceptsOnlyVerifiedOutsideParent(t *testing.T) {
	t.Parallel()
	events := []*runlog.Event{
		snapshotRunlogEvent(t, hooks.NewToolCallScheduledEvent("child", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", "child-call", nil, "", "parent-call", 0), 1),
		snapshotRunlogEvent(t, hooks.NewToolResultReceivedEvent("child", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "child", "records.lookup", "child-call", "parent-call", nil, nil, nil, "", nil, 0, nil, nil), 2),
	}
	for _, test := range []struct {
		name   string
		parent string
		valid  bool
	}{
		{"verified outside parent", "parent-call", true},
		{"root has no outside parent", "", false},
		{"different outside parent", "other-call", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot, err := newRunSnapshot(events, test.parent, nil)
			if !test.valid {
				assert.ErrorContains(t, err, "requires active parent schedule")
				return
			}
			require.NoError(t, err)
			require.Len(t, snapshot.ToolCalls, 1)
			assert.Equal(t, "parent-call", snapshot.ToolCalls[0].ParentToolCallID)
			assert.Equal(t, 0, snapshot.ToolCalls[0].ObservedChildrenTotal)
			assert.False(t, snapshot.ToolCalls[0].CompletedAt.IsZero())
		})
	}
}

func TestParentToolCallForChildRejectsServiceChild(t *testing.T) {
	t.Parallel()
	child := session.RunMeta{RunID: "child", ParentRunID: "parent", Subject: genrunidentity.RunSubject{
		Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"}),
	}}
	_, err := parentToolCallForChild(nil, child)
	assert.ErrorContains(t, err, "child link requires an Agent subject")
}
