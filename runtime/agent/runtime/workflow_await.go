package runtime

// workflow_await.go contains workflow-side entry points for planner await results.
//
// The queued await implementation lives in workflow_await_queue.go; this file
// wires await-only turns into the shared queue handler.

import (
	"errors"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/planner"
)

// handleAwaitOnlyResult executes an await-only planner result (no tool calls).
//
// Return contract:
// - **out != nil**: the run finalized (e.g., policy enforcement).
// - **out == nil && err == nil**: await input was received and the workflow loop may continue.
func (r *Runtime) handleAwaitOnlyResult(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	ctrl *interrupt.Controller,
	deadlines *runDeadlines,
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
		deadlines,
		turnID,
		nil,
		st.Result.Await.Items,
		nil,
	)
}
