// Package runtime inspects engine-accepted runs before their first worker
// activity records StartRootRun. Storage LoadRun remains strictly for recorded
// runs; unstarted inspection reads the accepted execution identity and status
// from Engine.QueryRunCompletion without synthesizing a start event or time.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// acceptedRunSnapshotBase extracts the immutable owner, session, turn, and
// labels from an accepted workflow input using the runtime's existing input and
// continuation checkpoint restore rules.
func acceptedRunSnapshotBase(runID string, input *api.RunInput) (*run.Snapshot, error) {
	if input == nil || input.RunID != runID {
		return nil, errors.New("accepted workflow input is missing or does not match run id")
	}
	subject := input.Subject
	if _, err := genrunidentity.EncodeRunSubject(&subject); err != nil {
		return nil, fmt.Errorf("accepted run subject: %w", err)
	}
	if service, ok := subject.Executor.AsServiceTool(); ok && service != nil {
		restored, state, err := restoreRegisteredRoot(input)
		if err != nil {
			return nil, err
		}
		owner := *service
		subject.Executor = genrunidentity.NewRunExecutorServiceTool(&owner)
		return &run.Snapshot{
			RunID:     runID,
			Subject:   subject,
			SessionID: restored.SessionID,
			TurnID:    restored.TurnID,
			Labels:    cloneLabels(state.call.Labels),
		}, nil
	}
	if err := validateWorkflowRunInput(input); err != nil {
		return nil, err
	}
	copied := *input
	if copied.Continuation != nil {
		checkpoint, err := decodeWorkflowCheckpointState(copied.Continuation.Suspension)
		if err != nil {
			return nil, err
		}
		if err := restoreContinuationRunInput(&copied, checkpoint); err != nil {
			return nil, err
		}
	}
	return &run.Snapshot{
		RunID:     runID,
		Subject:   subject,
		SessionID: copied.SessionID,
		TurnID:    copied.TurnID,
		Labels:    cloneLabels(copied.Labels),
	}, nil
}

// loadAcceptedRunSnapshot returns a compact snapshot from the workflow engine's
// accepted execution record when StartRootRun has not yet committed to Store.
func (r *Runtime) loadAcceptedRunSnapshot(ctx context.Context, runID string) (*run.Snapshot, error) {
	if runID == "" || r.Engine == nil {
		return nil, session.ErrRunNotFound
	}
	completion, err := r.Engine.QueryRunCompletion(ctx, runID)
	if err != nil {
		if errors.Is(err, engine.ErrWorkflowNotFound) {
			return nil, session.ErrRunNotFound
		}
		return nil, err
	}
	if snapshot, found, err := r.loadRecordedRunSnapshotIfPresent(ctx, runID); err != nil || found {
		return snapshot, err
	}
	if completion.Input != nil && completion.Input.SessionID != "" {
		status, err := r.Store.LoadSessionStatus(ctx, completion.Input.SessionID)
		if err != nil {
			if errors.Is(err, session.ErrSessionPurged) || errors.Is(err, session.ErrSessionNotFound) {
				return nil, session.ErrRunNotFound
			}
			return nil, err
		}
		if status != session.StatusActive && status != session.StatusEnded {
			return nil, session.ErrRunNotFound
		}
	}
	base, err := acceptedRunSnapshotBase(runID, completion.Input)
	if err != nil {
		return nil, err
	}
	switch completion.Status {
	case engine.RunStatusPending, engine.RunStatusRunning:
		base.Status = run.StatusPending
		base.Phase = run.PhasePrompted
		return base, nil
	case engine.RunStatusPaused:
		base.Status = run.StatusPaused
		base.Phase = run.PhasePrompted
		return base, nil
	case engine.RunStatusFailed, engine.RunStatusTimedOut:
		base.Status = run.StatusFailed
		base.Phase = run.PhaseFailed
		base.UpdatedAt = completion.CompletedAt
		return base, nil
	case engine.RunStatusCanceled:
		base.Status = run.StatusCanceled
		base.Phase = run.PhaseCanceled
		base.UpdatedAt = completion.CompletedAt
		return base, nil
	case engine.RunStatusCompleted:
		return nil, fmt.Errorf("%w: unstarted run %q has completed engine status without stored run", ErrRunCompletionCorrupt, runID)
	default:
		return nil, fmt.Errorf("%w: unstarted run %q has unsupported engine status %q", ErrRunCompletionCorrupt, runID, completion.Status)
	}
}

// loadRecordedRunSnapshotIfPresent replays the canonical run log when the run
// was recorded in Store concurrently with an engine completion query.
func (r *Runtime) loadRecordedRunSnapshotIfPresent(ctx context.Context, runID string) (*run.Snapshot, bool, error) {
	events, err := r.loadRunEvents(ctx, runID)
	if err != nil {
		if errors.Is(err, session.ErrRunNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	metadata, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if _, err := genrunidentity.EncodeRunSubject(&metadata.Subject); err != nil {
		return nil, false, fmt.Errorf("saved run subject: %w", err)
	}
	parentToolCallID, err := r.snapshotParentToolCall(ctx, metadata)
	if err != nil {
		return nil, false, err
	}
	inherited, err := r.snapshotInheritedEvents(ctx, metadata)
	if err != nil {
		return nil, false, err
	}
	snapshot, err := newRunSnapshot(events, parentToolCallID, inherited)
	if err != nil {
		return nil, false, err
	}
	if !reflect.DeepEqual(snapshot.Subject, metadata.Subject) {
		return nil, false, errors.New("run snapshot subject differs from saved run metadata")
	}
	if _, isAgent := metadata.Subject.Executor.AsAgent(); !isAgent {
		return snapshot, true, nil
	}
	messages, err := transcript.BuildMessagesFromRunLogPrefix(ctx, r.Store, runID, events[len(events)-1].ID)
	if err != nil {
		return nil, false, err
	}
	setSnapshotTranscript(snapshot, messages)
	return snapshot, true, nil
}
