package evidence

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestContinuationSeparatesInvocationsFromObservedCompletions(t *testing.T) {
	first := collectEvents(t, NewCollector(),
		toolStart("first", "svc.ask", "ask-1", ""),
		runStreamEnd("first"),
	)
	next, err := NewContinuationCollector(first, "next")
	require.NoError(t, err)
	result := collectEvents(t, next,
		workflowEvent("next", "started", nil),
		toolEnd("next", "svc.ask", "ask-1", "", `{"answer":"vessel"}`, nil),
		toolStart("next", "svc.read", "read-1", ""),
		toolEnd("next", "svc.read", "read-1", "", `{"pressure":12}`, nil),
	)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "read-1", result.ToolCalls[0].ToolCallID)
	assert.Empty(t, result.Calls("svc.ask"))
	require.Len(t, result.ToolCompletions, 2)
	assert.Equal(t, "first", result.ToolCompletions[0].InvocationRootRunID)
	assert.Equal(t, "ask-1", result.ToolCompletions[0].Call.ToolCallID)
	assert.Equal(t, "next", result.ToolCompletions[1].InvocationRootRunID)
	assert.Equal(t, "read-1", result.ToolCompletions[1].Call.ToolCallID)
	assert.JSONEq(t, `{"answer":"vessel"}`, string(result.ToolCompletions[0].Call.Result))
	assert.False(t, first.ToolCalls[0].Completed)
	assert.Empty(t, first.ToolCompletions)

	for _, exact := range []bool{false, true} {
		expect := Expect{
			Exact:       exact,
			Tools:       []Tool{{Name: "svc.read", RequireAllAttemptsSuccessful: true}},
			ForbidTools: []tools.Ident{"svc.ask"},
		}
		assert.Empty(t, expect.evaluateTrajectory(result))
		assert.Empty(t, expect.evaluateForbiddenTools(result))
	}

	// A new invocation of the earlier tool remains visible to every policy.
	require.NoError(t, next.Consume(toolStart("next", "svc.ask", "ask-2", "")))
	require.NoError(t, next.Consume(toolEnd("next", "svc.ask", "ask-2", "", "", &planner.ToolFailure{
		Kind: planner.FailureInvalidCall, Error: planner.NewToolError("invalid question"),
	})))
	require.NoError(t, next.Consume(workflowEvent("next", "completed", nil)))
	require.NoError(t, next.Consume(runStreamEnd("next")))
	result, err = next.Finish()
	require.NoError(t, err)
	assert.Len(t, result.Calls("svc.ask"), 1)
	assert.NotEmpty(t, (Expect{Exact: true, Tools: []Tool{{Name: "svc.read"}}}).evaluateTrajectory(result))
	assert.NotEmpty(t, (Expect{ForbidTools: []tools.Ident{"svc.ask"}}).evaluateForbiddenTools(result))
	assert.NotEmpty(t, (Expect{Tools: []Tool{{
		Name: "svc.ask", RequireAllAttemptsSuccessful: true,
	}}}).evaluateTrajectory(result))
	assert.NotEmpty(t, (Expect{Tools: []Tool{{
		Name: "svc.ask", ForbidFailureKinds: []planner.FailureKind{planner.FailureInvalidCall},
	}}}).evaluateTrajectory(result))
}

func TestContinuationRetainsPendingCallsAcrossMultipleAcceptedRoots(t *testing.T) {
	first := collectEvents(t, NewCollector(),
		toolStart("first", "svc.summary", "summary", ""),
		toolStart("first", "svc.ask", "ask", ""),
		runStreamEnd("first"),
	)
	second, err := NewContinuationCollector(first, "second")
	require.NoError(t, err)
	middle := collectEvents(t, second,
		workflowEvent("second", "started", nil),
		toolStart("second", "svc.read", "read", ""),
		toolEnd("second", "svc.read", "read", "", `{}`, nil),
		toolEnd("second", "svc.summary", "summary", "", `{"cursor":"C"}`, nil),
		toolStart("second", "svc.continue", "continue", ""),
		toolEnd("second", "svc.continue", "continue", "", `{"cursor":"C","complete":true}`, nil),
		runStreamEnd("second"),
	)
	require.Len(t, middle.ToolCompletions, 3)
	assert.Equal(t, []string{"read", "summary", "continue"}, []string{
		middle.ToolCompletions[0].Call.ToolCallID,
		middle.ToolCompletions[1].Call.ToolCallID,
		middle.ToolCompletions[2].Call.ToolCallID,
	})
	third, err := NewContinuationCollector(middle, "third")
	require.NoError(t, err)
	last := collectEvents(t, third,
		workflowEvent("third", "started", nil),
		toolEnd("third", "svc.ask", "ask", "", `{"answer":"yes"}`, nil),
		runStreamEnd("third"),
	)
	assert.Empty(t, last.ToolCalls)
	require.Len(t, last.ToolCompletions, 1)
	assert.Equal(t, "first", last.ToolCompletions[0].InvocationRootRunID)
	// Completed invocations cannot be imported again at a later boundary.
	require.ErrorContains(t, third.Consume(toolEnd("third", "svc.summary", "summary", "", `{}`, nil)), "unknown tool call")
	fourth, err := NewContinuationCollector(last, "fourth")
	require.NoError(t, err)
	empty := collectEvents(t, fourth, workflowEvent("fourth", "started", nil), runStreamEnd("fourth"))
	assert.Empty(t, empty.ToolCalls)
	assert.Empty(t, empty.ToolCompletions)
}

func TestContinuationUsesObservedPriorParentsWithoutInventingCalls(t *testing.T) {
	first := collectEvents(t, NewCollector(),
		toolStart("first", "svc.agent", "parent", ""),
		toolStart("child", "svc.ask", "pending", "parent"),
		toolEnd("first", "svc.agent", "parent", "", `{}`, nil),
		runStreamEnd("first"),
	)
	next, err := NewContinuationCollector(first, "next")
	require.NoError(t, err)
	result := collectEvents(t, next,
		workflowEvent("next", "started", nil),
		toolStart("child-next", "svc.agent", "new-parent", "parent"),
		toolStart("next", "svc.independent", "independent", ""),
		toolStart("grandchild", "svc.detail", "detail", "new-parent"),
		toolEnd("child-next", "svc.ask", "pending", "parent", `{}`, nil),
		runStreamEnd("next"),
	)
	require.Len(t, result.ToolCalls, 3)
	assert.Equal(t, "new-parent", result.ToolCalls[0].ToolCallID)
	assert.Equal(t, "detail", result.ToolCalls[1].ToolCallID)
	assert.Equal(t, "independent", result.ToolCalls[2].ToolCallID)
	assert.Equal(t, "first", result.ToolCompletions[0].InvocationRootRunID)
	require.ErrorContains(t, next.Consume(toolStart("next", "svc.agent", "parent", "")), "duplicate tool_start")
	// Pending children keep the same ancestry through another accepted link.
	last, err := NewContinuationCollector(result, "last")
	require.NoError(t, err)
	collectEvents(t, last,
		workflowEvent("last", "started", nil),
		toolEnd("grandchild", "svc.detail", "detail", "new-parent", `{}`, nil),
		toolEnd("child-next", "svc.agent", "new-parent", "parent", `{}`, nil),
		runStreamEnd("last"),
	)
}

func TestContinuationRejectsUnprovenOrChangedIdentity(t *testing.T) {
	pending := collectEvents(t, NewCollector(), toolStart("first", "svc.ask", "ask", ""))
	_, err := NewContinuationCollector(pending, "next")
	require.ErrorContains(t, err, "root stream boundary")
	first := collectEvents(t, NewCollector(), toolStart("first", "svc.ask", "ask", ""), runStreamEnd("first"))
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	var decoded Evidence
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	for _, unproven := range []*Evidence{nil, &decoded, {RunID: "first", SessionID: "session"}} {
		_, err := NewContinuationCollector(unproven, "next")
		require.ErrorContains(t, err, "root stream boundary")
	}
	for _, id := range []string{"", "first"} {
		_, err := NewContinuationCollector(first, id)
		require.ErrorContains(t, err, "distinct nonempty")
	}
	for _, field := range []string{"root", "session"} {
		changed := *first
		if field == "root" {
			changed.RunID = "different"
		} else {
			changed.SessionID = "different"
		}
		_, err := NewContinuationCollector(&changed, "next")
		require.ErrorContains(t, err, "differs from its observed")
	}
	for _, event := range []stream.Event{
		workflowEvent("wrong-root", "started", nil),
		stream.Workflow{Base: stream.NewBase(stream.EventWorkflow, "next", "wrong-session", nil)},
	} {
		next, err := NewContinuationCollector(first, "next")
		require.NoError(t, err)
		require.ErrorContains(t, next.Consume(event), "root/session")
		_, err = next.Finish()
		require.ErrorContains(t, err, "has not identified")
	}
	ordinary := NewCollector()
	require.NoError(t, ordinary.Consume(workflowEvent("unrelated", "started", nil)))
	require.ErrorContains(t, ordinary.Consume(toolEnd("unrelated", "svc.ask", "ask", "", `{}`, nil)), "unknown tool call")
}

func TestContinuationRejectsChangedAndRepeatedCompletions(t *testing.T) {
	first := collectEvents(t, NewCollector(), toolStart("first", "svc.ask", "ask", ""), runStreamEnd("first"))
	next, err := NewContinuationCollector(first, "next")
	require.NoError(t, err)
	require.NoError(t, next.Consume(workflowEvent("next", "started", nil)))
	for _, event := range []stream.Event{
		toolEnd("next", "svc.other", "ask", "", `{}`, nil),
		toolEnd("next", "svc.ask", "ask", "wrong-parent", `{}`, nil),
	} {
		require.ErrorContains(t, next.Consume(event), "name or parent")
	}
	require.ErrorContains(t, next.Consume(toolStart("next", "svc.ask", "ask", "")), "duplicate tool_start")
	require.NoError(t, next.Consume(toolEnd("next", "svc.ask", "ask", "", `{}`, nil)))
	require.ErrorContains(t, next.Consume(toolEnd("next", "svc.ask", "ask", "", `{}`, nil)), "duplicate tool_end")
}

func TestCollectorSnapshotsOwnPayloadsAndContinuationContext(t *testing.T) {
	start := toolStart("first", "svc.ask", "ask", "").(stream.ToolStart)
	start.Data.Payload = rawjson.Message(`{"question":"vessel?"}`)
	c := NewCollector()
	first := collectEvents(t, c, start, runStreamEnd("first"))
	start.Data.Payload[2] = 'X'
	first.ToolCalls[0].Args[2] = 'Y'
	first.ToolCalls[0].Name = "changed-tool"
	next, err := NewContinuationCollector(first, "next")
	require.NoError(t, err)
	require.NoError(t, next.Consume(workflowEvent("next", "started", nil)))
	total, cursor := 4, "cursor"
	end := toolEnd("next", "svc.ask", "ask", "", `{"answer":"vessel"}`, &planner.ToolFailure{
		Kind: planner.FailureInvalidCall, Error: planner.NewToolError("original"),
		Recovery: planner.RecoveryDirective{PriorInput: rawjson.Message(`{"question":"vessel?"}`)},
	}).(stream.ToolEnd)
	end.Data.Bounds = &agent.Bounds{Total: &total, NextCursor: &cursor}
	result := collectEvents(t, next, end)
	end.Data.Result[2] = 'X'
	*end.Data.Bounds.Total = 99
	end.Data.Failure.Error.Message = "changed-error"
	completed := &result.ToolCompletions[0].Call
	assert.JSONEq(t, `{"question":"vessel?"}`, string(completed.Args))
	assert.JSONEq(t, `{"answer":"vessel"}`, string(completed.Result))
	assert.Equal(t, 4, *completed.Bounds.Total)
	assert.Equal(t, "original", completed.Failure.Error.Message)
	completed.Args[2] = 'Z'
	completed.Result[2] = 'Z'
	*completed.Bounds.NextCursor = "changed-cursor"
	completed.Failure.Recovery.PriorInput[2] = 'Z'
	again, err := next.Finish()
	require.NoError(t, err)
	assert.JSONEq(t, `{"question":"vessel?"}`, string(again.ToolCompletions[0].Call.Args))
	assert.JSONEq(t, `{"answer":"vessel"}`, string(again.ToolCompletions[0].Call.Result))
	assert.Equal(t, "cursor", *again.ToolCompletions[0].Call.Bounds.NextCursor)
	assert.JSONEq(t, `{"question":"vessel?"}`, string(again.ToolCompletions[0].Call.Failure.Recovery.PriorInput))
}

// collectEvents consumes a complete fixture segment and fails at its first
// contract violation, preserving the event index in the test diagnostic.
func collectEvents(t *testing.T, collector *Collector, events ...stream.Event) *Evidence {
	t.Helper()
	for i, event := range events {
		require.NoError(t, collector.Consume(event), "event %d", i)
	}
	result, err := collector.Finish()
	require.NoError(t, err)
	return result
}
