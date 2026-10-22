package api

// Continuation availability is read outside workflow code. The workflow sends
// exact history and output references with its accepted run context so the
// recorded answer selects ordinary recovery or finalization without saved results.

import (
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ContinuationActivityInput selects the saved work and eligibility rules for
	// one continuation-availability decision. New results require a new decision.
	ContinuationActivityInput struct {
		// AgentID owns the run and the currently registered tool contracts.
		AgentID agent.Ident
		// RunContext retains the workflow's accepted invocation, operation, labels
		// and text-only mode so catalog selection receives the same values as planning.
		// Its run and session must own every referenced history record.
		RunContext run.Context
		// HistoryEndID bounds the selected run's committed history.
		HistoryEndID string
		// ToolOutputs names current completed work, including results restored from
		// a checkpoint. These references do not grant access to another owner.
		ToolOutputs []*ToolOutputRef
		// RestrictToTool retains the caller's existing tool restriction.
		RestrictToTool tools.Ident
		// TagClauses retains the caller's existing tool tag restrictions.
		TagClauses []TagPolicyClause
	}
)
