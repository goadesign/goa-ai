package runtime

// workflow_state.go defines the mutable state threaded through the workflow plan loop.
//
// Contract:
// - The workflow loop has a small set of values that evolve over time (caps, attempt,
//   aggregated usage, transcript/ledger, and the current planner result).
// - Helpers mutate this state in place to keep function signatures compact and
//   to make state transitions explicit at call sites.

import (
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	runLoopState struct {
		// Caps is the current runtime policy cap state (remaining tool budget, failure budget, etc.).
		Caps policy.CapsState

		// NextAttempt is the attempt number to stamp on the next planner activity request.
		NextAttempt int

		// AggUsage is the aggregated token usage across plan/resume iterations and tool turns.
		AggUsage model.TokenUsage

		// Result is the current planner result being processed by the loop.
		Result *planner.PlanResult

		// Transcript is the provider transcript for the current planner result.
		Transcript []*model.Message

		// Ledger is the provider transcript ledger used to merge tool_use/tool_result into messages.
		Ledger *transcript.Ledger

		// ToolEvents are the accumulated tool results emitted over the lifetime of this run.
		ToolEvents []*planner.ToolResult
	}
)

func newRunLoopState(result *planner.PlanResult, transcriptMsgs []*model.Message, usage model.TokenUsage, caps policy.CapsState, nextAttempt int) *runLoopState {
	return &runLoopState{
		Caps:        caps,
		NextAttempt: nextAttempt,
		AggUsage:    usage,
		Result:      result,
		Transcript:  transcriptMsgs,
		Ledger:      transcript.FromModelMessages(transcriptMsgs),
	}
}
