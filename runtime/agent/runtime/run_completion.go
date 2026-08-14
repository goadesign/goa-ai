// Package runtime coordinates lazy workflow-handle waiting and terminal hook
// repair so durable runs still emit one canonical terminal event without
// forcing every starter process to block on workflow completion.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.temporal.io/sdk/temporal"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

type (
	observedWorkflowHandle struct {
		inner     engine.WorkflowHandle
		runtime   *Runtime
		runID     string
		agentID   agent.Ident
		sessionID string
		turnID    string
		labels    map[string]string

		waitOnce  sync.Once
		waitDone  chan struct{}
		out       *api.RunOutput
		err       error
		repairErr error
	}

	// runCompletionIdentity carries the durable run identity required to
	// synthesize a canonical RunCompleted event when no in-process observed
	// handle remains (for example, after a starter restart).
	runCompletionIdentity struct {
		AgentID   agent.Ident
		SessionID string
		TurnID    string
		Labels    map[string]string
	}
)

// newObservedWorkflowHandle wraps an engine handle so explicit Wait callers and
// on-demand snapshot repair share one underlying Wait call.
func newObservedWorkflowHandle(runtime *Runtime, input *RunInput, labels map[string]string, inner engine.WorkflowHandle) *observedWorkflowHandle {
	return &observedWorkflowHandle{
		inner:     inner,
		runtime:   runtime,
		runID:     input.RunID,
		agentID:   input.AgentID,
		sessionID: input.SessionID,
		turnID:    input.TurnID,
		labels:    cloneLabels(labels),
		waitDone:  make(chan struct{}),
	}
}

func (h *observedWorkflowHandle) Wait(ctx context.Context) (*api.RunOutput, error) {
	if err := h.Repair(ctx); err != nil {
		return nil, err
	}
	return h.out, h.err
}

func (h *observedWorkflowHandle) Cancel(ctx context.Context) error {
	return h.inner.Cancel(ctx)
}

// Repair waits for the shared workflow completion path, then converges the
// canonical terminal hook without surfacing the run's terminal error.
// Runtime snapshot/event readers use this after the engine reports the
// workflow is already closed.
func (h *observedWorkflowHandle) Repair(ctx context.Context) error {
	if err := h.waitForWaitResult(ctx); err != nil {
		return err
	}
	return h.repairErr
}

// waitForWaitResult blocks until the shared underlying Wait call completes or
// the caller cancels. It does not publish terminal hooks; the shared repair
// helper below owns convergence so Wait() and lazy repair cannot diverge.
func (h *observedWorkflowHandle) waitForWaitResult(ctx context.Context) error {
	h.ensureWait()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.waitDone:
		return nil
	}
}

func (h *observedWorkflowHandle) ensureWait() {
	h.waitOnce.Do(func() {
		h.runtime.storeWorkflowHandle(h.runID, h)
		go h.awaitCompletion()
	})
}

// awaitCompletion owns the single underlying Wait call. Terminal hook
// convergence happens here via the shared repair helper before any waiter is
// released so explicit Wait calls remain the canonical source of terminal
// metadata whenever a local observed handle exists.
func (h *observedWorkflowHandle) awaitCompletion() {
	h.out, h.err = h.inner.Wait(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	h.repairErr = h.runtime.repairObservedTerminalRunCompletion(ctx, h.runID, h.agentID, h.sessionID, h.turnID, h.labels, h.out, h.err)
	cancel()
	if h.repairErr != nil {
		h.runtime.logWarn(context.Background(), "run completion repair failed", h.repairErr, "run_id", h.runID, "agent_id", h.agentID)
	}
	close(h.waitDone)
	h.runtime.storeWorkflowHandle(h.runID, nil)
}

// buildRunCompletedEvent constructs the canonical terminal hook payload for a
// run, including persisted cancellation provenance when the run was canceled.
func (r *Runtime) buildRunCompletedEvent(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID, status string,
	phase run.Phase,
	labels map[string]string,
	err error,
) (*hooks.RunCompletedEvent, error) {
	var cancellation *run.Cancellation
	if status == runStatusCanceled {
		loadCancellation, loadErr := r.loadRunCancellation(ctx, runID)
		if loadErr != nil {
			return nil, fmt.Errorf("load cancellation provenance: %w", loadErr)
		}
		cancellation = loadCancellation
	}
	return hooks.NewRunCompletedEvent(runID, agentID, sessionID, status, phase, labels, err, cancellation), nil
}

// repairObservedTerminalRunCompletion publishes the canonical terminal hook for
// a workflow handle that has already completed locally. It shares the same
// serialized repair gate as lazy no-handle repair so only one terminal event
// can be appended per run.
func (r *Runtime) repairObservedTerminalRunCompletion(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID, turnID string,
	labels map[string]string,
	out *api.RunOutput,
	waitErr error,
) error {
	if waitErr == nil {
		if err := validateWorkflowOutput(out, agentID, runID); err != nil {
			return err
		}
	}
	if out != nil && out.Suspension != nil {
		if waitErr != nil {
			return errors.New("runtime: suspended workflow returned an error")
		}
		evt := hooks.NewRunSuspendedEvent(
			runID,
			agentID,
			sessionID,
			out.Suspension.ID,
			out.Suspension.Version,
			len(out.Suspension.Pending),
			out.Suspension.RequiredTools,
		)
		return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
			return r.publishHookErr(ctx, evt, turnID)
		})
	}
	status := terminalRunStatusForError(waitErr)
	phase := terminalRunPhaseForStatus(status)
	evt, err := r.buildRunCompletedEvent(ctx, runID, agentID, sessionID, status, phase, labels, waitErr)
	if err != nil {
		return err
	}
	return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
		return r.publishHookErr(ctx, evt, turnID)
	})
}

// repairTerminalRunCompletion blocks only when the workflow is already terminal
// in the engine but the canonical run log still lacks a terminal event. This keeps
// repair lazy for long-lived runs while still converging snapshots on demand.
func (r *Runtime) repairTerminalRunCompletion(ctx context.Context, runID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	terminal, err := r.runHasTerminalSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	if terminal {
		return nil
	}
	if r.Engine == nil {
		return nil
	}
	status, err := r.Engine.QueryRunStatus(ctx, runID)
	if err != nil {
		if errors.Is(err, engine.ErrWorkflowNotFound) {
			return nil
		}
		return err
	}
	if !isTerminalRunStatus(status) {
		return nil
	}
	if handle, ok := r.workflowHandle(runID); ok {
		if observed, ok := handle.(*observedWorkflowHandle); ok {
			repairCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return observed.Repair(repairCtx)
		}
	}
	return r.repairQueriedTerminalRunCompletion(ctx, runID)
}

// withSerializedTerminalRepair runs repair at most once for a run by checking
// the canonical snapshot while holding the shared repair mutex. Callers should
// only invoke this after they have independent evidence that the workflow is
// already terminal.
func (r *Runtime) withSerializedTerminalRepair(ctx context.Context, runID string, repair func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.completionRepairMu.Lock()
	defer r.completionRepairMu.Unlock()
	terminal, err := r.runHasTerminalSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	if terminal {
		return nil
	}
	return repair(ctx)
}

// runHasTerminalSnapshot reports whether the canonical run log already contains
// a terminal completion or suspension event for the given run.
func (r *Runtime) runHasTerminalSnapshot(ctx context.Context, runID string) (bool, error) {
	snapshot, err := r.loadRunSnapshot(ctx, runID)
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch snapshot.Status {
	case run.StatusCompleted, run.StatusFailed, run.StatusCanceled, run.StatusSuspended:
		return true, nil
	case run.StatusPending, run.StatusRunning, run.StatusPaused:
		return false, nil
	}
	panic("runtime: unsupported run snapshot status for terminal detection: " + string(snapshot.Status))
}

// repairQueriedTerminalRunCompletion rebuilds the terminal hook from the
// engine's durable terminal output/error when no local observed handle remains.
func (r *Runtime) repairQueriedTerminalRunCompletion(ctx context.Context, runID string) error {
	return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
		out, waitErr := r.Engine.QueryRunCompletion(ctx, runID)
		if errors.Is(waitErr, engine.ErrWorkflowNotFound) {
			return waitErr
		}
		identity, err := r.runCompletionMetadata(ctx, runID)
		if err != nil {
			return err
		}
		if waitErr == nil {
			if err := validateWorkflowOutput(out, identity.AgentID, runID); err != nil {
				return err
			}
		}
		if out != nil && out.Suspension != nil {
			if waitErr != nil {
				return errors.New("runtime: suspended workflow returned an error")
			}
			return r.publishHookErr(
				ctx,
				hooks.NewRunSuspendedEvent(
					runID,
					identity.AgentID,
					identity.SessionID,
					out.Suspension.ID,
					out.Suspension.Version,
					len(out.Suspension.Pending),
					out.Suspension.RequiredTools,
				),
				identity.TurnID,
			)
		}
		status := terminalRunStatusForError(waitErr)
		evt, err := r.buildRunCompletedEvent(
			ctx,
			runID,
			identity.AgentID,
			identity.SessionID,
			status,
			terminalRunPhaseForStatus(status),
			identity.Labels,
			waitErr,
		)
		if err != nil {
			return err
		}
		return r.publishHookErr(
			ctx,
			evt,
			identity.TurnID,
		)
	})
}

// runCompletionMetadata recovers the durable run identity needed to synthesize
// a canonical RunCompleted event after a restart. Session metadata supplies the
// agent/session mapping and run labels while the earliest run-log event
// preserves the original turn ID (and, for sessionless runs, the labels
// recorded on RunStarted) when available.
func (r *Runtime) runCompletionMetadata(ctx context.Context, runID string) (runCompletionIdentity, error) {
	var identity runCompletionIdentity
	if r.SessionStore != nil {
		meta, err := r.SessionStore.LoadRun(ctx, runID)
		if err == nil {
			identity.AgentID = agent.Ident(meta.AgentID)
			identity.SessionID = meta.SessionID
			identity.Labels = meta.Labels
		} else if !errors.Is(err, session.ErrRunNotFound) {
			return runCompletionIdentity{}, err
		}
	}
	if r.RunEventStore != nil {
		page, err := r.RunEventStore.List(ctx, runID, "", 1)
		if err != nil {
			return runCompletionIdentity{}, err
		}
		if len(page.Events) > 0 {
			ev := page.Events[0]
			if identity.AgentID == "" {
				identity.AgentID = ev.AgentID
			}
			if identity.SessionID == "" {
				identity.SessionID = ev.SessionID
			}
			identity.TurnID = ev.TurnID
			if len(identity.Labels) == 0 && ev.Type == hooks.RunStarted {
				var p hooks.RunStartedEvent
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					return runCompletionIdentity{}, fmt.Errorf("decode %s payload: %w", hooks.RunStarted, err)
				}
				identity.Labels = p.RunContext.Labels
			}
		}
	}
	if identity.AgentID == "" {
		return runCompletionIdentity{}, run.ErrNotFound
	}
	return identity, nil
}

// terminalRunStatusForError maps workflow completion errors onto the public
// runtime status contract. Timeouts are failures, explicit cancellations stay
// canceled, and everything else is a generic failure.
func terminalRunStatusForError(err error) string {
	switch {
	case err == nil:
		return runStatusSuccess
	case isRunTimeoutError(err):
		return runStatusFailed
	case isRunCancellationError(err):
		return runStatusCanceled
	default:
		return runStatusFailed
	}
}

// terminalRunPhaseForStatus keeps the terminal phase aligned with the status
// emitted in RunCompleted events.
func terminalRunPhaseForStatus(status string) run.Phase {
	switch status {
	case runStatusSuccess:
		return run.PhaseCompleted
	case runStatusCanceled:
		return run.PhaseCanceled
	case runStatusFailed:
		return run.PhaseFailed
	default:
		return run.PhaseCompleted
	}
}

// isRunTimeoutError recognizes engine timeout closures that should surface as
// failed runs with timeout-facing public errors.
func isRunTimeoutError(err error) bool {
	var timeoutErr *temporal.TimeoutError
	return errors.As(err, &timeoutErr) || errors.Is(err, context.DeadlineExceeded)
}

// isRunCancellationError recognizes operator-initiated or engine-propagated
// cancellations that should surface as canceled runs.
func isRunCancellationError(err error) bool {
	var canceledErr *temporal.CanceledError
	return errors.As(err, &canceledErr) || errors.Is(err, context.Canceled)
}
