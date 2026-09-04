package runtime

// workflow_state.go defines the mutable state threaded through the workflow plan loop.
//
// Contract:
// - The workflow loop has a small set of values that evolve over time (caps,
//   attempt, aggregated usage, canonical transcript, and the current planner
//   result).
// - Helpers mutate this state in place to keep function signatures compact and
//   to make state transitions explicit at call sites.

import (
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
)

type (
	runLoopState struct {
		// Caps is the current tool-call and recovery-turn budget.
		Caps policy.CapsState

		// NextAttempt is the attempt number to stamp on the next planner activity request.
		NextAttempt int

		// AggUsage is the aggregated token usage across plan/resume iterations and tool turns.
		AggUsage model.TokenUsage

		// Result is the current planner result being processed by the loop.
		Result *PlanResult

		// Transcript is the provider transcript for the current planner result.
		Transcript []*model.Message

		// ResponseID identifies the planner activity that produced Result and
		// Transcript. It becomes the stable identity of any assistant text saved
		// from that response.
		ResponseID string

		// ResponseCommitted reports whether Transcript or its planner-authored
		// equivalent has been persisted for the current result.
		ResponseCommitted bool

		// ToolEvents are the accumulated tool results emitted over the lifetime of this run.
		ToolEvents []*planner.ToolResult

		// ToolOutputs is the accumulated executed tool-call history emitted over
		// the lifetime of this run.
		ToolOutputs []*planner.ToolOutput

		// PendingRecovery is either failed tool work or one rejected model
		// answer. The concrete type determines the next planner input.
		PendingRecovery pendingPlannerRecovery
	}
)
