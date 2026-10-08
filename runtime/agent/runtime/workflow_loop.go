package runtime

// workflow_loop.go owns the planner state, tool activity settings, and deadlines
// used by one running agent workflow. Loop methods use workflow time and engine
// operations so replay follows the same calls and transitions. Before publishing
// a host question, the loop retains accepted Tasks and suspended children. The
// workflow finalizer settles that work on failure or cancellation; a successful
// suspension transfers it through the stored checkpoint to the next workflow.

import (
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// workflowConversation holds the original messages and derived summary
	// between activities of this workflow. The summary never replaces Messages
	// and is not included in the session transcript or suspension checkpoint.
	workflowConversation struct {
		Messages         []*model.Message
		HistoryEndID     string
		RunContext       run.Context
		HistoryContext   *api.HistoryContext
		providerRecovery *providerRecoveryBudget
		providerControl  *providerRecoveryActor
	}

	workflowLoop struct {
		r *Runtime

		wfCtx engine.WorkflowContext
		reg   AgentRegistration

		input *RunInput
		base  *workflowConversation
		st    *runLoopState

		turnID        string
		parentTracker *childTracker
		deadlines     runDeadlines
		resumeOpts    engine.ActivityOptions
		toolOpts      engine.ActivityOptions

		// Unfinished records remain owned here until execution accepts them or
		// suspension storage transfers them to the next workflow.
		unfinishedBatch   *stepBatch
		unfinishedPending []checkpointPendingInput
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
	base *workflowConversation,
	st *runLoopState,
	turnID string,
	parentTracker *childTracker,
	deadlines runDeadlines,
	resumeOpts engine.ActivityOptions,
	toolOpts engine.ActivityOptions,
) *workflowLoop {
	loop := &workflowLoop{
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
	if base.providerControl != nil {
		base.providerControl.budget = &loop.deadlines.Budget
		base.providerControl.hard = &loop.deadlines.Hard
	}
	return loop
}

// shouldFinalize reports whether it is too late to schedule new work and the runtime
// should move to finalization immediately.
func (d runDeadlines) shouldFinalize(now time.Time) bool {
	return !d.Budget.IsZero() && !now.Before(d.Budget)
}

func (l *workflowLoop) run() (*RunOutput, error) {
	ctx := l.wfCtx.Context()
	for {
		pending, _ := toolRecovery(l.st.PendingRecovery)
		if recovery := modelOutputRecovery(l.st.PendingCorrection); recovery != nil {
			out, err := l.resumePlanner(pending, false, recovery, nil, true)
			if err != nil {
				return nil, err
			}
			if out != nil {
				return out, nil
			}
			continue
		}
		if recovery := modelInvocationRecovery(l.st.PendingCorrection); recovery != nil {
			out, err := l.resumePlanner(pending, false, nil, recovery, true)
			if err != nil {
				return nil, err
			}
			if out != nil {
				return out, nil
			}
			continue
		}
		_, recoveryCatalog := toolRecovery(l.st.PendingRecovery)
		if err := l.r.rewriteRecoveryCatalogToolCalls(recoveryCatalog, l.st.Result); err != nil {
			return nil, err
		}
		if err := l.r.validateCompletionToolPlanResult(l.st.Result, completionTool(l.input)); err != nil {
			return nil, err
		}
		program, err := l.r.normalizePlanResultContract(l.st.Result, l.base.RunContext, l.input.AgentID)
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
