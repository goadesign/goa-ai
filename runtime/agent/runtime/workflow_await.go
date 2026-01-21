package runtime

// workflow_await.go contains workflow-side entry points for planner await results.
//
// The queued await implementation lives in workflow_await_queue.go; this file
// keeps the workflow loop hooks that convert timeouts into deterministic
// finalization and delegates await-only turns into the shared queue handler.

import (
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/planner"
)

// handleAwaitOnlyResult executes an await-only planner result (no tool calls).
//
// Return contract:
// - **out != nil**: the run finalized (e.g., await timed out).
// - **out == nil && err == nil**: await input was received and the workflow loop may continue.
func (r *Runtime) handleAwaitOnlyResult(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	ctrl *interrupt.Controller,
	budgetDeadline time.Time,
	hardDeadline time.Time,
	turnID string,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	r.logger.Info(ctx, "PlanResult has Await, handling await queue")
	if st == nil || st.Result == nil || st.Result.Await == nil {
		return nil, errors.New("await: missing await payload")
	}
	return r.handleAwaitQueue(
		wfCtx,
		reg,
		input,
		base,
		st,
		resumeOpts,
		engine.ActivityOptions{},
		0,
		nil,
		ctrl,
		budgetDeadline,
		hardDeadline,
		0,
		turnID,
		nil,
		st.Result.Await.Items,
		nil,
	)
}

// finalizeAwaitTimeout converts an expired await into a deterministic RunResumedEvent
// and then requests finalization from the planner.
func (r *Runtime) finalizeAwaitTimeout(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	hardDeadline time.Time,
	reason string,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		"await_timeout",
		"runtime",
		map[string]string{
			"resumed_by": "await_timeout",
			"await":      reason,
		},
		0,
	), turnID); err != nil {
		return nil, err
	}
	return r.finalizeWithPlanner(
		wfCtx,
		reg,
		input,
		base,
		st.ToolEvents,
		st.AggUsage,
		st.NextAttempt,
		turnID,
		planner.TerminationReasonAwaitTimeout,
		hardDeadline,
	)
}
