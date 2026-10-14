// Package runtime closes a saved root continuation without starting another
// workflow only after decoding its exact current checkpoint and proving there
// is no retained child or remote operation to cancel. Historical bytes remain
// unchanged; the Store arbitrates closure against concurrent continuation.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// relinquishEmptyRootSuspension handles one root's permanent closure. Child
// suspensions and saved external work stay on the parent-owned cleanup path.
// A racing admitted successor updates target and returns false so durable
// cancellation routes through the actual admitted execution.
func (r *Runtime) relinquishEmptyRootSuspension(ctx context.Context, request CancelRequest, target *session.RunMeta) (settled bool, resultErr error) {
	if target.ParentRunID != "" {
		return false, nil
	}
	switch target.SuspensionResolution.(type) {
	case session.RelinquishedSuspension:
		return true, nil
	case session.ContinuedSuspension:
		return false, nil
	case nil:
	default:
		return false, storage.NewContractError(errors.New("stored suspension has an invalid resolution"))
	}
	ctx, span := r.tracer.Start(ctx, "runtime.relinquish_empty_suspension")
	defer func() {
		span.SetAttributes(attribute.String("run.id", target.RunID), attribute.Bool("suspension.relinquished", settled))
		if resultErr != nil {
			span.RecordError(resultErr)
			span.SetStatus(codes.Error, "suspension closure failed")
		}
		span.End()
	}()
	stored, err := r.Store.LoadRunSuspension(ctx, target.RunID)
	if err != nil {
		return false, err
	}
	suspension, err := validateStoredRunSuspension(stored, *target)
	if err != nil {
		return false, storage.NewContractError(fmt.Errorf("%w: %w", ErrRunSuspensionCorrupt, err))
	}
	// Service-tool roots can close an unanswered confirmation, but a retained
	// Task or remote input still requires its ordinary cancellation operation.
	if _, service := target.Subject.Executor.AsServiceTool(); service {
		checkpoint, err := decodeRegisteredRootCheckpoint(suspension)
		if err != nil {
			return false, storage.NewContractError(err)
		}
		if checkpoint.Task != nil || checkpoint.Pending != nil {
			return false, nil
		}
	} else {
		checkpoint, err := decodeWorkflowCheckpointState(suspension)
		if err != nil {
			return false, storage.NewContractError(err)
		}
		// Completed synchronous calls have no retained cancellation handle.
		// ScheduleRequired requests event publication, not another execution;
		// RequiresResume requests planner consumption, not remote cleanup.
		for _, record := range checkpoint.Batch.Records {
			if record.Task != nil || record.ChildSuspension != nil || record.MCPPending != nil {
				return false, nil
			}
		}
		for _, pending := range checkpoint.Pending {
			if pending.Child != nil || pending.MCP != nil {
				return false, nil
			}
		}
	}
	resolution, err := r.Store.RelinquishRunSuspension(ctx, storage.RunSuspensionRelinquishment{
		RunID: target.RunID, SuspensionID: stored.ID, Reason: request.Reason,
	})
	if errors.Is(err, session.ErrRunCancellationConflict) {
		// The root may have closed or admitted a successor after the conflict.
		// Read the owner once to distinguish either outcome from unresolved intent.
		current, readErr := r.Store.LoadRun(ctx, target.RunID)
		if readErr != nil {
			return false, readErr
		}
		if current.RunID != target.RunID || !reflect.DeepEqual(current.Subject, target.Subject) || !reflect.DeepEqual(current.Origin, target.Origin) ||
			current.SessionID != target.SessionID || current.ParentRunID != "" ||
			current.Status != session.RunStatusSuspended {
			return false, storage.NewContractError(errors.New("suspension resolution read does not match the suspended root owner"))
		}
		switch resolved := current.SuspensionResolution.(type) {
		case session.RelinquishedSuspension:
			target.SuspensionResolution = resolved
			span.AddEvent("competing cancellation permanently relinquished continuation")
			return true, nil
		case session.ContinuedSuspension:
			target.SuspensionResolution = resolved
			span.AddEvent("continuation admission won after cancellation conflict")
			return false, nil
		case nil:
			return false, &CancellationReasonConflictError{RunID: request.RunID, Reason: request.Reason}
		default:
			return false, storage.NewContractError(errors.New("stored suspension has an invalid resolution"))
		}
	}
	if err != nil {
		return false, err
	}
	target.SuspensionResolution = resolution
	switch selected := resolution.(type) {
	case session.RelinquishedSuspension:
		if selected.Reason != request.Reason {
			return false, storage.NewContractError(session.ErrRunCancellationConflict)
		}
		span.AddEvent("continuation permanently relinquished")
		return true, nil
	case session.ContinuedSuspension:
		span.AddEvent("continuation admission won")
		return false, nil
	default:
		return false, storage.NewContractError(errors.New("suspension closure returned no permanent resolution"))
	}
}
