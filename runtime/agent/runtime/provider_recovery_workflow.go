package runtime

// The runtime wraps its workflow context to observe accepted child and tool
// lifetimes. All three child paths use StartChildWorkflow and the returned Get,
// so direct calls and continuations use the same measured clock as tool batches.
// The wrapper does not change engine identity, cancellation, or planner inputs.

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

type (
	providerRecoveryWorkflowContext struct {
		engine.WorkflowContext
		actor    *providerRecoveryActor
		detached bool
		limit    *providerRecoveryWaitLimit
	}

	providerRecoveryWaitLimit struct {
		deadline time.Time
		elapsed  time.Duration
	}

	providerRecoveryChildHandle struct {
		engine.ChildWorkflowHandle
		workflow *providerRecoveryWorkflowContext
	}
)

func (w *providerRecoveryWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.WorkflowContext.Context(), w)
}

func (w *providerRecoveryWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	derived, cancel := w.WorkflowContext.WithCancel()
	return &providerRecoveryWorkflowContext{
		WorkflowContext: derived, actor: w.actor, detached: w.detached, limit: w.limit,
	}, cancel
}

func (w *providerRecoveryWorkflowContext) Detached() engine.WorkflowContext {
	return &providerRecoveryWorkflowContext{
		WorkflowContext: w.WorkflowContext.Detached(), actor: w.actor, detached: true, limit: w.limit,
	}
}

func (w *providerRecoveryWorkflowContext) StartChildWorkflow(ctx context.Context, request engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	if _, exists := w.actor.children[request.ID]; exists {
		// The engine owns duplicate-ID rejection. Keep the original child's
		// clock binding while it returns that rejection.
		return w.WorkflowContext.StartChildWorkflow(ctx, request)
	}
	branch := w.actor.clock.branch(w.Now())
	w.actor.children[request.ID] = branch
	child, err := w.WorkflowContext.StartChildWorkflow(ctx, request)
	if err != nil {
		branch.end = w.Now()
		return nil, err
	}
	w.actor.work = append(w.actor.work, providerRecoveryWork{branch: branch, ready: child.IsReady})
	return &providerRecoveryChildHandle{ChildWorkflowHandle: child, workflow: w}, nil
}

func (w *providerRecoveryWorkflowContext) ExecuteToolActivityAsync(call engine.ToolActivityCall) (engine.Future[*ToolOutput], error) {
	branch := w.actor.clock.branch(w.Now())
	future, err := w.WorkflowContext.ExecuteToolActivityAsync(call)
	if err != nil {
		branch.end = w.Now()
		return nil, err
	}
	w.actor.work = append(w.actor.work, providerRecoveryWork{branch: branch, ready: future.IsReady})
	return future, nil
}

// Get waits through the runtime clock before reading the ready child result.
// A detached context is reserved for joining cancellation and terminal storage;
// it does not reopen admission or extend a deadline that already won.
func (h *providerRecoveryChildHandle) Get(ctx context.Context) (*RunOutput, error) {
	w := h.workflow
	if caller, ok := engine.WorkflowContextFromContext(ctx).(*providerRecoveryWorkflowContext); ok {
		w = caller
	}
	return awaitAgentChild(w, h.ChildWorkflowHandle, ctx)
}

// awaitAgentChild waits for a result through the caller's workflow context.
// If that wait stops, it cancels and joins the accepted child before returning,
// preserving terminal child storage for native and runtime-owned callers alike.
func awaitAgentChild(w engine.WorkflowContext, child engine.ChildWorkflowHandle, ctx context.Context) (*RunOutput, error) {
	err := w.Await(func() bool { return ctx.Err() != nil || child.IsReady() })
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if cancelErr := child.Cancel(w.Detached().Context()); cancelErr != nil {
			return nil, errors.Join(err, cancelErr)
		}
		if _, joinErr := child.Get(w.Detached().Context()); joinErr != nil && !temporalerrors.CancellationOnly(joinErr) {
			return nil, errors.Join(err, joinErr)
		}
		return nil, err
	}
	return child.Get(ctx)
}

// Await wakes for ordinary results, accepted control, or the current active
// deadline. Clock changes rebuild only the deadline timer; issued recovery
// permissions keep their original expiry. Predicates never mutate clock state.
func (w *providerRecoveryWorkflowContext) Await(condition func() bool) error {
	if w.detached {
		return w.WorkflowContext.Await(condition)
	}
	a := w.actor
	for {
		if err := w.Context().Err(); err != nil {
			if a.workflowContext.Err() != nil {
				a.clock.closed = true
			}
			return err
		}
		if err := a.collect(w.WorkflowContext); err != nil {
			return err
		}
		if err := a.observe(w.WorkflowContext); err != nil {
			return err
		}
		if condition() {
			return nil
		}
		var deadline time.Time
		if w.limit != nil && !w.limit.deadline.IsZero() {
			deadline = w.limit.deadline.Add(a.elapsed - w.limit.elapsed)
		} else if a.budget != nil && !a.clock.closed {
			deadline = *a.budget
		}
		if !deadline.IsZero() && !w.Now().Before(deadline) {
			if a.owner != nil {
				before := a.elapsed
				if err := a.apply(w.WorkflowContext, a.owner.observations()); err != nil {
					return err
				}
				if err := a.collect(w.WorkflowContext); err != nil {
					return err
				}
				if err := a.observe(w.WorkflowContext); err != nil {
					return err
				}
				if a.elapsed != before {
					continue
				}
			}
			// Apply only evidence already observed. A report arriving after
			// this decision may update accounting but cannot revive the run.
			a.clock.closed = true
			if a.owner != nil {
				if err := a.apply(w.WorkflowContext, a.owner.stop(engine.ErrPlannerActivityDeadlineExceeded)); err != nil {
					return err
				}
			}
			return engine.ErrPlannerActivityDeadlineExceeded
		}
		var timer engine.Future[time.Time]
		timerCtx, cancelTimer := w.WorkflowContext.WithCancel()
		if !deadline.IsZero() {
			var err error
			timer, err = timerCtx.NewTimer(timerCtx.Context(), deadline.Sub(w.Now()))
			if err != nil {
				cancelTimer()
				return err
			}
		}
		version := a.version
		a.clock.beginWait(w.Now())
		err := w.WorkflowContext.Await(func() bool {
			return condition() || a.version != version || a.deliveryReady() ||
				a.workReady() || timer != nil && timer.IsReady()
		})
		a.clock.endWait(w.Now())
		if err != nil && a.workflowContext.Err() != nil {
			a.clock.closed = true
		}
		if timer != nil && timer.IsReady() && a.current != nil {
			if observeErr := a.current.observeWait(w.WorkflowContext); observeErr != nil {
				cancelTimer()
				return observeErr
			}
		}
		cancelTimer()
		if observeErr := a.observe(w.WorkflowContext); observeErr != nil {
			return observeErr
		}
		if err != nil {
			return err
		}
	}
}

func (a *providerRecoveryActor) workReady() bool {
	for _, work := range a.work {
		if work.branch.end.IsZero() && work.ready() {
			return true
		}
	}
	return false
}
