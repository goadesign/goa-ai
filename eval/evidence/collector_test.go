package evidence

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
)

func TestCollectorProjectsRunTreeIntoEvidence(t *testing.T) {
	// A root run calls one direct tool and one agent-as-tool whose child run
	// calls a nested tool; child events interleave with the root's.
	c := NewCollector()
	events := []stream.Event{
		workflowEvent("root", "started", nil),
		toolStart("root", "svc.read.list", "call-1", ""),
		toolStart("root", "svc.agents.helper", "call-2", ""),
		assistantReply("root", "The answer "),
		toolStart("child", "svc.read.detail", "call-2a", "call-2"),
		toolEnd("child", "svc.read.detail", "call-2a", `{"detail":true}`, nil),
		toolEnd("root", "svc.read.list", "call-1", `{"items":[1,2]}`, nil),
		toolEnd("root", "svc.agents.helper", "call-2", `{"ok":true}`, nil),
		assistantReply("child", "child narration is excluded"),
		assistantReply("root", "is 42."),
		workflowEvent("root", "completed", nil),
		runStreamEnd("child"),
		runStreamEnd("root"),
	}
	for _, event := range events[:len(events)-1] {
		require.NoError(t, c.Consume(event))
		assert.False(t, c.Done())
	}
	require.NoError(t, c.Consume(events[len(events)-1]))
	assert.True(t, c.Done())

	evidence, err := c.Finish()
	require.NoError(t, err)
	assert.Equal(t, "root", evidence.RunID)
	assert.Equal(t, "session", evidence.SessionID)
	assert.Equal(t, "The answer is 42.", evidence.Answer)
	assert.Equal(t, run.PhaseCompleted, evidence.TerminalPhase)

	// Causal order groups the nested call immediately after its parent.
	names := make([]string, len(evidence.ToolCalls))
	for i, call := range evidence.ToolCalls {
		names[i] = string(call.Name)
	}
	assert.Equal(t, []string{"svc.read.list", "svc.agents.helper", "svc.read.detail"}, names)
	for _, call := range evidence.ToolCalls {
		assert.True(t, call.Completed)
		assert.Nil(t, call.Failure)
	}
	assert.JSONEq(t, `{"items":[1,2]}`, string(evidence.Calls("svc.read.list")[0].Result))
}

func TestCollectorRecordsFailureAndTerminalFailure(t *testing.T) {
	c := NewCollector()
	failure := &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.NewToolError("bad arguments"),
	}
	require.NoError(t, c.Consume(toolStart("root", "svc.read.list", "call-1", "")))
	require.NoError(t, c.Consume(toolEnd("root", "svc.read.list", "call-1", "", failure)))
	require.NoError(t, c.Consume(workflowEvent("root", "failed", &run.Failure{Message: "boom"})))

	evidence, err := c.Finish()
	require.NoError(t, err)
	call := evidence.ToolCalls[0]
	require.NotNil(t, call.Failure)
	assert.Equal(t, planner.FailureInvalidCall, call.Failure.Kind)
	assert.Equal(t, run.PhaseFailed, evidence.TerminalPhase)
	require.NotNil(t, evidence.TerminalFailure)
	assert.Equal(t, "boom", evidence.TerminalFailure.Message)
}

func TestCollectorRecordsPendingConfirmation(t *testing.T) {
	c := NewCollector()
	require.NoError(t, c.Consume(stream.AwaitConfirmation{
		Base: stream.NewBase(stream.EventAwaitConfirmation, "root", "session", nil),
		Data: stream.AwaitConfirmationPayload{
			ToolName:   "svc.write.update",
			ToolCallID: "call-9",
			Prompt:     "Apply the change?",
			Payload:    rawjson.Message(`{"value":7}`),
		},
	}))
	evidence, err := c.Finish()
	require.NoError(t, err)
	require.NotNil(t, evidence.Confirmation)
	assert.EqualValues(t, "svc.write.update", evidence.Confirmation.ToolName)
	assert.Equal(t, "Apply the change?", evidence.Confirmation.Prompt)
}

func TestCollectorIgnoresOtherRunsLifecycle(t *testing.T) {
	c := NewCollector()
	require.NoError(t, c.Consume(workflowEvent("root", "started", nil)))
	require.NoError(t, c.Consume(workflowEvent("child", "failed", &run.Failure{Message: "child boom"})))
	require.NoError(t, c.Consume(runStreamEnd("child")))
	assert.False(t, c.Done())

	evidence, err := c.Finish()
	require.NoError(t, err)
	assert.Empty(t, evidence.TerminalPhase)
	assert.Nil(t, evidence.TerminalFailure)
}

func TestCollectorRejectsContractViolations(t *testing.T) {
	t.Run("duplicate tool start", func(t *testing.T) {
		c := NewCollector()
		require.NoError(t, c.Consume(toolStart("root", "svc.read.list", "call-1", "")))
		assert.ErrorContains(t, c.Consume(toolStart("root", "svc.read.list", "call-1", "")), "duplicate tool_start")
	})
	t.Run("tool end without start", func(t *testing.T) {
		c := NewCollector()
		assert.ErrorContains(t, c.Consume(toolEnd("root", "svc.read.list", "call-1", "{}", nil)), "unknown tool call")
	})
	t.Run("orphaned parent at finish", func(t *testing.T) {
		c := NewCollector()
		require.NoError(t, c.Consume(toolStart("child", "svc.read.detail", "call-2a", "missing-parent")))
		_, err := c.Finish()
		assert.ErrorContains(t, err, "orphaned parent tool call IDs")
	})
}

// toolStart builds a synthetic tool_start event for tests.
func toolStart(runID, tool, callID, parentCallID string) stream.Event {
	payload := stream.ToolStartPayload{
		ToolCallID:       callID,
		ToolName:         tool,
		Payload:          rawjson.Message(`{}`),
		ParentToolCallID: parentCallID,
	}
	return stream.ToolStart{
		Base: stream.NewBase(stream.EventToolStart, runID, "session", payload),
		Data: payload,
	}
}

// toolEnd builds a synthetic tool_end event for tests.
func toolEnd(runID, tool, callID, result string, failure *planner.ToolFailure) stream.Event {
	payload := stream.ToolEndPayload{
		ToolCallID: callID,
		ToolName:   tool,
		Failure:    failure,
	}
	if result != "" {
		payload.Result = rawjson.Message(result)
	}
	return stream.ToolEnd{
		Base: stream.NewBase(stream.EventToolEnd, runID, "session", payload),
		Data: payload,
	}
}

// assistantReply builds a synthetic assistant_reply event for tests.
func assistantReply(runID, text string) stream.Event {
	payload := stream.AssistantReplyPayload{Text: text}
	return stream.AssistantReply{
		Base: stream.NewBase(stream.EventAssistantReply, runID, "session", payload),
		Data: payload,
	}
}

// workflowEvent builds a synthetic workflow lifecycle event for tests.
func workflowEvent(runID, phase string, failure *run.Failure) stream.Event {
	payload := stream.WorkflowPayload{Phase: phase, Failure: failure}
	return stream.Workflow{
		Base: stream.NewBase(stream.EventWorkflow, runID, "session", payload),
		Data: payload,
	}
}

// runStreamEnd builds a synthetic run_stream_end marker for tests.
func runStreamEnd(runID string) stream.Event {
	return stream.RunStreamEnd{
		Base: stream.NewBase(stream.EventRunStreamEnd, runID, "session", stream.RunStreamEndPayload{}),
		Data: stream.RunStreamEndPayload{},
	}
}
