// Package runtime records why a run was canceled before its workflow stops.
// The workflow stores the first accepted reason so its terminal record does not
// have to infer intent from a bare context.Canceled error.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

const (
	cancellationIntentEventKey string = "runtime/cancellation-intent"
)

const (
	workflowOpen uint32 = iota
	workflowCancellationInProgress
	workflowCancellationAccepted
	workflowFinalizationInProgress
	workflowFinalizationFinished
)

type (
	// CancelRequest describes an explicit runtime-owned cancellation request.
	//
	// Contract:
	// - RunID and Reason are required.
	// - Reason should use the canonical run.CancellationReason* constants.
	CancelRequest struct {
		// RunID identifies the run to cancel.
		RunID string
		// Reason records who or what initiated the cancellation.
		Reason string
	}

	// CancellationReasonConflictError reports a later cancellation request whose
	// reason differs from the first durable request.
	CancellationReasonConflictError struct {
		// RunID identifies the run whose first cancellation reason won.
		RunID string
		// Reason is the later reason that was rejected.
		Reason string
	}

	cancellationIntentPayload struct {
		Reason string `json:"reason"`
	}

	// workflowFinalizationState makes cancellation storage and terminal storage
	// choose one order. A cancellation that starts first records its reason before
	// the terminal result. Once terminal storage starts, later cancellation waits
	// for that storage and reports that the workflow already completed.
	workflowFinalizationState struct {
		phase atomic.Uint32
	}
)

// Error implements error.
func (e *CancellationReasonConflictError) Error() string {
	return fmt.Sprintf("run %q already has a different cancellation reason; rejected %q", e.RunID, e.Reason)
}

// handleWorkflowCancellation records one cancellation only while the workflow
// still accepts it. The finalizer and this handler cannot write lifecycle state
// in the opposite order.
func (r *Runtime) handleWorkflowCancellation(
	state *workflowFinalizationState,
	cancelCtx engine.WorkflowContext,
	input *RunInput,
	startCommand *api.StorageActivityCommand,
	request engine.CancellationRequest,
) (err error) {
	if err := state.beginCancellation(cancelCtx.Detached()); err != nil {
		return err
	}
	defer func() {
		state.finishCancellation(err == nil)
	}()
	if request.RunID != input.RunID {
		return fmt.Errorf("runtime: cancellation run id %q does not match workflow run id %q", request.RunID, input.RunID)
	}
	output, err := r.executeStorageWithRetry(cancelCtx.Detached().Context(), startCommand)
	if err != nil {
		return err
	}
	start := runStartStorageResult(input, output)
	if start.Outcome == session.RunStartStop {
		if start.CancellationReason != request.Reason {
			return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
		}
		return nil
	}
	return r.publishRunCancellation(cancelCtx, input, request)
}

// publishRunCancellation stores the reason from a workflow cancellation
// command. The engine waits for this activity before it stops the workflow.
func (r *Runtime) publishRunCancellation(wfCtx engine.WorkflowContext, input *RunInput, req engine.CancellationRequest) error {
	payload, err := json.Marshal(cancellationIntentPayload{Reason: req.Reason})
	if err != nil {
		return err
	}
	output, err := r.executeStorageWithRetry(wfCtx.Detached().Context(), &api.StorageActivityCommand{
		Cancellation: &api.RunCancellationCommand{
			Record: &RecordActivityInput{
				Type:        storage.CancellationRecordType,
				EventKey:    cancellationIntentEventKey,
				RunID:       input.RunID,
				AgentID:     input.AgentID,
				SessionID:   input.SessionID,
				TurnID:      input.TurnID,
				TimestampMS: wfCtx.Now().UnixMilli(),
				Payload:     rawjson.Message(payload),
			},
		},
	})
	if err != nil {
		return err
	}
	if output.Cancellation.Outcome == api.RunCancellationConflict {
		return &engine.CancellationConflictError{RunID: req.RunID, Reason: req.Reason}
	}
	return nil
}

// loadRunCancellation loads the stored cancellation provenance for the run when
// one was recorded before cancellation.
func (r *Runtime) loadRunCancellation(ctx context.Context, runID string) (*run.Cancellation, error) {
	meta, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if meta.CancellationReason == "" {
		return nil, nil
	}
	return &run.Cancellation{Reason: meta.CancellationReason}, nil
}

// beginCancellation claims lifecycle storage for one accepted cancellation.
// A command that arrives after finalization starts waits for the final record
// so callers never observe a temporary active state beside a completed error.
func (s *workflowFinalizationState) beginCancellation(wfCtx engine.WorkflowContext) error {
	for {
		switch phase := s.phase.Load(); phase {
		case workflowOpen:
			if s.phase.CompareAndSwap(workflowOpen, workflowCancellationInProgress) {
				return nil
			}
		case workflowFinalizationInProgress:
			if err := wfCtx.Await(func() bool {
				return s.phase.Load() == workflowFinalizationFinished
			}); err != nil {
				return err
			}
			return engine.ErrWorkflowCompleted
		case workflowFinalizationFinished:
			return engine.ErrWorkflowCompleted
		case workflowCancellationInProgress, workflowCancellationAccepted:
			panic("runtime: engine invoked the cancellation handler concurrently")
		default:
			panic(fmt.Sprintf("runtime: unknown workflow finalization phase %d", phase))
		}
	}
}

// finishCancellation lets finalization continue after cancellation storage
// succeeds. A failed storage command reopens admission so the engine can retry
// the same request.
func (s *workflowFinalizationState) finishCancellation(accepted bool) {
	if accepted {
		s.phase.Store(workflowCancellationAccepted)
		return
	}
	s.phase.Store(workflowOpen)
}

// beginFinalization waits for an earlier cancellation write, then prevents any
// later request from reaching storage while the terminal record is written. It
// reports whether an accepted cancellation must replace a successful return.
func (s *workflowFinalizationState) beginFinalization(wfCtx engine.WorkflowContext) (bool, error) {
	for {
		switch phase := s.phase.Load(); phase {
		case workflowOpen:
			if s.phase.CompareAndSwap(workflowOpen, workflowFinalizationInProgress) {
				return false, nil
			}
		case workflowCancellationInProgress:
			if err := wfCtx.Await(func() bool {
				return s.phase.Load() != workflowCancellationInProgress
			}); err != nil {
				return false, err
			}
		case workflowCancellationAccepted:
			if s.phase.CompareAndSwap(workflowCancellationAccepted, workflowFinalizationInProgress) {
				return true, nil
			}
		case workflowFinalizationInProgress, workflowFinalizationFinished:
			panic("runtime: workflow finalization started more than once")
		default:
			panic(fmt.Sprintf("runtime: unknown workflow finalization phase %d", phase))
		}
	}
}

// finishFinalization releases cancellation requests that arrived while the
// terminal record was being stored.
func (s *workflowFinalizationState) finishFinalization() {
	s.phase.Store(workflowFinalizationFinished)
}
