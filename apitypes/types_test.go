package apitypes

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestRunInputConversionRoundTrip(t *testing.T) {
	in := &RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Messages: []*AgentMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
		Labels:   map[string]string{"env": "dev"},
		Metadata: map[string]any{"priority": "p1"},
		WorkflowOptions: &WorkflowOptions{
			Memo:             map[string]any{"k": "v"},
			SearchAttributes: map[string]any{"SessionID": "sess-1"},
			TaskQueue:        "q",
			RetryPolicy:      &RetryPolicy{MaxAttempts: 3, InitialInterval: "1s", BackoffCoefficient: 2},
		},
	}

	rin, err := ToRuntimeRunInput(in)
	require.NoError(t, err)
	require.Equal(t, in.AgentID, rin.AgentID)
	require.Equal(t, in.RunID, rin.RunID)
	require.Equal(t, in.SessionID, rin.SessionID)
	require.Equal(t, in.TurnID, rin.TurnID)
	require.Len(t, rin.Messages, len(in.Messages))
	back := FromRuntimeRunInput(rin)
	require.Equal(t, in.AgentID, back.AgentID)
	require.Equal(t, in.RunID, back.RunID)
	require.Equal(t, in.SessionID, back.SessionID)
	require.Equal(t, in.TurnID, back.TurnID)
	require.Len(t, back.Messages, len(in.Messages))
	require.Equal(t, "Hello", back.Messages[1].Content)
}

func TestRunOutputConversionRoundTrip(t *testing.T) {
	out := &RunOutput{
		AgentID: "svc.agent",
		RunID:   "run-1",
		Final:   &AgentMessage{Role: "assistant", Content: "Hi"},
		ToolEvents: []*ToolResult{
			{
				Name:   "svc.ts.tool",
				Result: map[string]any{"ok": true},
				Error:  &ToolError{Message: "", Cause: nil},
				RetryHint: &RetryHint{
					Reason: "invalid_arguments",
					Tool:   "svc.ts.tool",
				},
				Telemetry: &ToolTelemetry{DurationMs: 10, TokensUsed: 1, Model: "m"},
			},
		},
		Notes: []*PlannerAnnotation{{Text: "note", Labels: map[string]string{"k": "v"}}},
	}

	rout := ToRuntimeRunOutput(out)
	require.Equal(t, out.AgentID, rout.AgentID)
	require.Equal(t, out.RunID, rout.RunID)
	require.Equal(t, "assistant", rout.Final.Role)
	require.Equal(t, "Hi", rout.Final.Content)
	// Check types of converted slices
	_, ok := any(rout.ToolEvents).([]planner.ToolResult)
	require.True(t, ok)
	require.Len(t, rout.ToolEvents, 1)
	back := FromRuntimeRunOutput(rout)
	require.Equal(t, out.AgentID, back.AgentID)
	require.Equal(t, out.RunID, back.RunID)
	require.NotNil(t, back.Final)
	require.Equal(t, "Hi", back.Final.Content)
}

func TestToRuntimeRunOutputEmpty(t *testing.T) {
	r := ToRuntimeRunOutput(nil)
	require.Empty(t, r.AgentID)
	require.Empty(t, r.RunID)
	require.Empty(t, r.ToolEvents)
	require.Empty(t, r.Notes)
}
