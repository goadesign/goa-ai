package apitypes

import (
    "testing"

    "goa.design/goa-ai/agents/runtime/planner"
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
    if err != nil {
        t.Fatalf("ToRuntimeRunInput: %v", err)
    }
    if rin.AgentID != in.AgentID || rin.RunID != in.RunID ||
        rin.SessionID != in.SessionID || rin.TurnID != in.TurnID {
        t.Fatalf("mismatch ids: %+v vs %+v", rin, in)
    }
    if len(rin.Messages) != len(in.Messages) {
        t.Fatalf("messages len: got %d want %d", len(rin.Messages), len(in.Messages))
    }
    back := FromRuntimeRunInput(rin)
    if back.AgentID != in.AgentID || back.RunID != in.RunID ||
        back.SessionID != in.SessionID || back.TurnID != in.TurnID {
        t.Fatalf("roundtrip ids mismatch: %+v vs %+v", back, in)
    }
    if len(back.Messages) != len(in.Messages) || back.Messages[1].Content != "Hello" {
        t.Fatalf("roundtrip messages mismatch: %+v", back.Messages)
    }
}

func TestRunOutputConversionRoundTrip(t *testing.T) {
    out := &RunOutput{
        AgentID: "svc.agent",
        RunID:   "run-1",
        Final:   &AgentMessage{Role: "assistant", Content: "Hi"},
        ToolEvents: []*ToolResult{
            {
                Name:    "svc.ts.tool",
                Payload: map[string]any{"ok": true},
                Error:   &ToolError{Message: "", Cause: nil},
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
    if rout.AgentID != out.AgentID || rout.RunID != out.RunID {
        t.Fatalf("ids mismatch: %+v vs %+v", rout, out)
    }
    if rout.Final.Role != "assistant" || rout.Final.Content != "Hi" {
        t.Fatalf("final mismatch: %+v", rout.Final)
    }
    // Check types of converted slices
    if _, ok := any(rout.ToolEvents).([]planner.ToolResult); !ok || len(rout.ToolEvents) != 1 {
        t.Fatalf("tool events mismatch: %+v", rout.ToolEvents)
    }
    back := FromRuntimeRunOutput(rout)
    if back.AgentID != out.AgentID || back.RunID != out.RunID {
        t.Fatalf("roundtrip ids mismatch: %+v vs %+v", back, out)
    }
    if back.Final == nil || back.Final.Content != "Hi" {
        t.Fatalf("roundtrip final mismatch: %+v", back.Final)
    }
}

func TestToRuntimeRunOutputEmpty(t *testing.T) {
    if r := ToRuntimeRunOutput(nil); r.AgentID != "" || r.RunID != "" || len(r.ToolEvents) != 0 || len(r.Notes) != 0 {
        t.Fatalf("expected zero-ish output, got %+v", r)
    }
}
