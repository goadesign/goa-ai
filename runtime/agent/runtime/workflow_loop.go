package runtime

// workflow_loop.go defines the internal loop context used by the durable plan/tool
// workflow implementation.
//
// The original workflow implementation threaded many values (workflow context,
// registration, run input/base, deadlines, activity options,
// parent tracking, etc.) through long helper signatures. That style is brittle:
// it is easy to mis-thread values (e.g., budget vs hard deadline) and hard to
// evolve without propagating parameters everywhere.
//
// Contract:
// - workflowLoop is used only from within workflow execution (ExecuteWorkflow/runLoop).
// - It owns the shared, immutable context for a run iteration and provides helpers
//   that use workflow time for replay safety.
// - Mutable per-run state is held in runLoopState and is intentionally mutated in
//   place by loop methods.

import (
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	workflowLoop struct {
		r *Runtime

		wfCtx engine.WorkflowContext
		reg   AgentRegistration

		input *RunInput
		base  *planner.PlanInput
		st    *runLoopState

		turnID        string
		parentTracker *childTracker
		deadlines     runDeadlines
		resumeOpts    engine.ActivityOptions
		toolOpts      engine.ActivityOptions
	}

	runDeadlines struct {
		// Budget bounds active planner and budgeted tool work.
		Budget time.Time

		// Hard bounds final planner work and completion-owned bookkeeping after
		// Budget expires. Terminal hook persistence has its own completion context.
		Hard time.Time
	}
)

func newWorkflowLoop(
	r *Runtime,
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	parentTracker *childTracker,
	deadlines runDeadlines,
	resumeOpts engine.ActivityOptions,
	toolOpts engine.ActivityOptions,
) *workflowLoop {
	return &workflowLoop{
		r:             r,
		wfCtx:         wfCtx,
		reg:           reg,
		input:         input,
		base:          base,
		st:            st,
		turnID:        turnID,
		parentTracker: parentTracker,
		deadlines:     deadlines,
		resumeOpts:    resumeOpts,
		toolOpts:      toolOpts,
	}
}

// shouldFinalize reports whether it is too late to schedule new work and the runtime
// should move to finalization immediately.
func (d runDeadlines) shouldFinalize(now time.Time) bool {
	return !d.Budget.IsZero() && !now.Before(d.Budget)
}

func (l *workflowLoop) run() (*RunOutput, error) {
	ctx := l.wfCtx.Context()
	for {
		if err := l.r.rewriteRecoveryCatalogToolCalls(l.st.PendingRecoveryCatalog, l.st.Result); err != nil {
			return nil, err
		}
		if err := l.r.validateCompletionToolPlanResult(l.st.Result, completionTool(l.input)); err != nil {
			return nil, err
		}
		program, err := l.r.normalizePlanResultContract(l.st.Result)
		if err != nil {
			return nil, err
		}
		l.r.logger.Info(ctx, "Running workflow step", "kind", program.kind.String(), "tool_calls", len(program.calls), "await_items", len(program.awaitItems))
		out, err := l.runStep(program)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out, nil
		}
	}
}

