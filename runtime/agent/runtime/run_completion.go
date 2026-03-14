// run_completion.go coordinates lazy workflow-handle waiting and
// terminal hook repair so durable runs still emit one canonical RunCompleted
// event without forcing every starter process to block on workflow completion.
package runtime

import (
	"context"
	"errors"
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

type observedWorkflowHandle struct {
	inner     engine.WorkflowHandle
	runtime   *Runtime
	runID     string
	agentID   agent.Ident
	sessionID string
	turnID    string

	waitOnce sync.Once
	waitDone chan struct{}
	out      *api.RunOutput
	err      error
}

// newObservedWorkflowHandle wraps an engine handle so explicit Wait callers and
// on-demand snapshot repair share one underlying Wait call.
func newObservedWorkflowHandle(runtime *Runtime, input *RunInput, inner engine.WorkflowHandle) *observedWorkflowHandle {
	return &observedWorkflowHandle{
		inner:     inner,
		runtime:   runtime,
		runID:     input.RunID,
		agentID:   input.AgentID,
		sessionID: input.SessionID,
		turnID:    input.TurnID,
		waitDone:  make(chan struct{}),
	}
}

func (h *observedWorkflowHandle) Wait(ctx context.Context) (*api.RunOutput, error) {
	if err := h.Repair(ctx); err != nil {
		return nil, err
	}
	return h.out, h.err
}

func (h *observedWorkflowHandle) Signal(ctx context.Context, name string, payload any) error {
	return h.inner.Signal(ctx, name, payload)
}

func (h *observedWorkflowHandle) Cancel(ctx context.Context) error {
	return h.inner.Cancel(ctx)
}

// Repair waits for the shared workflow completion path, then converges the
// canonical RunCompleted hook without surfacing the run's terminal error.
// Runtime snapshot/event readers use this after the engine reports the
// workflow is already closed.
func (h *observedWorkflowHandle) Repair(ctx context.Context) error {
	return h.waitForWaitResult(ctx)
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
	err := h.runtime.repairObservedTerminalRunCompletion(ctx, h.runID, h.agentID, h.sessionID, h.turnID, h.err)
	cancel()
	if err != nil {
		h.runtime.logWarn(context.Background(), "run completion repair failed", err, "run_id", h.runID, "agent_id", h.agentID)
	}
	close(h.waitDone)
	h.runtime.storeWorkflowHandle(h.runID, nil)
}

// repairObservedTerminalRunCompletion publishes the canonical terminal hook for
// a workflow handle that has already completed locally. It shares the same
// serialized repair gate as lazy no-handle repair so only one RunCompleted
// event can be appended per run.
func (r *Runtime) repairObservedTerminalRunCompletion(ctx context.Context, runID string, agentID agent.Ident, sessionID, turnID string, waitErr error) error {
	status := terminalRunStatusForError(waitErr)
	phase := terminalRunPhaseForStatus(status)
	return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
		return r.publishHookErr(
			ctx,
			hooks.NewRunCompletedEvent(runID, agentID, sessionID, status, phase, waitErr),
			turnID,
		)
	})
}

// repairTerminalRunCompletion blocks only when the workflow is already terminal
// in the engine but the canonical run log still lacks RunCompleted. This keeps
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
	if querier, ok := r.Engine.(engine.CompletionQuerier); ok {
		return r.repairQueriedTerminalRunCompletion(ctx, runID, querier)
	}
	return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
		return r.synthesizeTerminalRunCompletion(ctx, runID, status)
	})
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

// runHasTerminalSnapshot reports whether the canonical run log already contains a
// terminal RunCompleted event for the given run.
func (r *Runtime) runHasTerminalSnapshot(ctx context.Context, runID string) (bool, error) {
	snapshot, err := r.loadRunSnapshot(ctx, runID)
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch snapshot.Status {
	case run.StatusCompleted, run.StatusFailed, run.StatusCanceled:
		return true, nil
	default:
		return false, nil
	}
}

// synthesizeTerminalRunCompletion publishes a canonical RunCompleted event using
// the durable engine status when no in-process observed workflow handle remains
// to surface the original wait error.
func (r *Runtime) synthesizeTerminalRunCompletion(ctx context.Context, runID string, status engine.RunStatus) error {
	agentID, sessionID, turnID, err := r.runCompletionMetadata(ctx, runID)
	if err != nil {
		return err
	}
	publicStatus := terminalRunStatusForEngineStatus(status)
	return r.publishHookErr(
		ctx,
		hooks.NewRunCompletedEvent(
			runID,
			agentID,
			sessionID,
			publicStatus,
			terminalRunPhaseForStatus(publicStatus),
			terminalRunErrorForStatus(status),
		),
		turnID,
	)
}

// repairQueriedTerminalRunCompletion rebuilds the terminal hook from the
// engine's durable terminal output/error when no local observed handle remains.
func (r *Runtime) repairQueriedTerminalRunCompletion(ctx context.Context, runID string, querier engine.CompletionQuerier) error {
	return r.withSerializedTerminalRepair(ctx, runID, func(ctx context.Context) error {
		_, waitErr := querier.QueryRunCompletion(ctx, runID)
		if errors.Is(waitErr, engine.ErrWorkflowNotFound) {
			return waitErr
		}
		agentID, sessionID, turnID, err := r.runCompletionMetadata(ctx, runID)
		if err != nil {
			return err
		}
		status := terminalRunStatusForError(waitErr)
		return r.publishHookErr(
			ctx,
			hooks.NewRunCompletedEvent(
				runID,
				agentID,
				sessionID,
				status,
				terminalRunPhaseForStatus(status),
				waitErr,
			),
			turnID,
		)
	})
}

// runCompletionMetadata recovers the identifiers needed to synthesize a
// canonical RunCompleted event after a restart. Session metadata supplies the
// durable agent/session mapping while the earliest run-log event preserves the
// original turn ID when available.
func (r *Runtime) runCompletionMetadata(ctx context.Context, runID string) (agent.Ident, string, string, error) {
	var (
		agentID   agent.Ident
		sessionID string
		turnID    string
	)
	if r.SessionStore != nil {
		meta, err := r.SessionStore.LoadRun(ctx, runID)
		if err == nil {
			agentID = agent.Ident(meta.AgentID)
			sessionID = meta.SessionID
		} else if !errors.Is(err, session.ErrRunNotFound) {
			return "", "", "", err
		}
	}
	if r.RunEventStore != nil {
		page, err := r.RunEventStore.List(ctx, runID, "", 1)
		if err != nil {
			return "", "", "", err
		}
		if len(page.Events) > 0 {
			ev := page.Events[0]
			if agentID == "" {
				agentID = ev.AgentID
			}
			if sessionID == "" {
				sessionID = ev.SessionID
			}
			turnID = ev.TurnID
		}
	}
	if agentID == "" {
		return "", "", "", run.ErrNotFound
	}
	return agentID, sessionID, turnID, nil
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

func terminalRunStatusForEngineStatus(status engine.RunStatus) string {
	switch status {
	case engine.RunStatusCompleted:
		return runStatusSuccess
	case engine.RunStatusTimedOut:
		return runStatusFailed
	case engine.RunStatusFailed:
		return runStatusFailed
	case engine.RunStatusCanceled:
		return runStatusCanceled
	default:
		panic("runtime: unexpected engine run status for terminal repair: " + string(status))
	}
}

func terminalRunErrorForStatus(status engine.RunStatus) error {
	switch status {
	case engine.RunStatusCompleted:
		return nil
	case engine.RunStatusTimedOut:
		return context.DeadlineExceeded
	case engine.RunStatusFailed:
		return errors.New("workflow failed before runtime emitted RunCompleted")
	case engine.RunStatusCanceled:
		return context.Canceled
	default:
		panic("runtime: unexpected engine run status for terminal error mapping: " + string(status))
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
