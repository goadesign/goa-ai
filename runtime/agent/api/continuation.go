package api

// Continuation availability is read outside workflow code. The workflow sends
// exact history and current output references so the recorded answer can select
// ordinary recovery or finalization without carrying saved tool data.

import (
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ContinuationActivityInput selects the saved work and eligibility rules for
	// one continuation-availability decision. New results require a new decision.
	ContinuationActivityInput struct {
		// AgentID owns the run and the currently registered tool contracts.
		AgentID agent.Ident
		// RunID owns the selected saved history.
		RunID string
		// SessionID must own every referenced run and supplied output.
		SessionID string
		// HistoryEndID bounds the selected run's committed history.
		HistoryEndID string
		// ToolOutputs names current completed work, including results restored from
		// a checkpoint. These references do not grant access to another owner.
		ToolOutputs []*ToolOutputRef
		// TextOnly excludes continuations that require interactive output.
		TextOnly bool
		// RestrictToTool retains the caller's existing tool restriction.
		RestrictToTool tools.Ident
		// TagClauses retains the caller's existing tool tag restrictions.
		TagClauses []TagPolicyClause
	}
)
