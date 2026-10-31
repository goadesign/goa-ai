// Package runtime inspects engine-accepted runs before their first worker
// activity records StartRootRun. Storage LoadRun remains strictly for recorded
// runs; unstarted inspection reads the accepted execution identity and status
// from engine.AcceptedRunQuerier without synthesizing a start event or time.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// loadAcceptedRunSnapshot returns a compact snapshot from the workflow engine's
// accepted execution record when StartRootRun has not yet committed to Store.
func (r *Runtime) loadAcceptedRunSnapshot(ctx context.Context, runID string) (*run.Snapshot, error) {
	if runID == "" || r.Engine == nil {
		return nil, session.ErrRunNotFound
	}
	querier, ok := r.Engine.(engine.AcceptedRunQuerier)
	if !ok || querier == nil {
		return nil, session.ErrRunNotFound
	}
	accepted, err := querier.QueryAcceptedRun(ctx, runID)
	if err != nil {
		if errors.Is(err, engine.ErrWorkflowNotFound) {
			if snapshot, found, recErr := r.loadRecordedRunSnapshotIfPresent(ctx, runID); recErr != nil || found {
				return snapshot, recErr
			}
			return nil, session.ErrRunNotFound
		}
		return nil, err
	}
	if snapshot, found, err := r.loadRecordedRunSnapshotIfPresent(ctx, runID); err != nil || found {
		return snapshot, err
	}
	if accepted.RunID != runID {
		return nil, errors.New("accepted workflow input run id does not match run id")
	}
	subject := accepted.Subject
	if _, err := genrunidentity.EncodeRunSubject(&subject); err != nil {
		return nil, fmt.Errorf("accepted run subject: %w", err)
	}
	if accepted.SessionID != "" {
		status, err := r.Store.LoadSessionStatus(ctx, accepted.SessionID)
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
	labels, err := r.resolveAcceptedRunLabels(ctx, accepted)
	if err != nil {
		return nil, err
	}
	base := &run.Snapshot{
		RunID:     runID,
		Subject:   subject,
		SessionID: accepted.SessionID,
		TurnID:    accepted.TurnID,
		Labels:    labels,
	}
	switch accepted.Status {
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
		base.UpdatedAt = accepted.CompletedAt
		return base, nil
	case engine.RunStatusCanceled:
		base.Status = run.StatusCanceled
		base.Phase = run.PhaseCanceled
		base.UpdatedAt = accepted.CompletedAt
		return base, nil
	case engine.RunStatusCompleted:
		return nil, fmt.Errorf("%w: unstarted run %q has completed engine status without stored run", ErrRunCompletionCorrupt, runID)
	default:
		return nil, fmt.Errorf("%w: unstarted run %q has unsupported engine status %q", ErrRunCompletionCorrupt, runID, accepted.Status)
	}
}

// resolveAcceptedRunLabels returns the accepted run's labels, inheriting
// predecessor checkpoint labels from the durable continuation seed and saved
// suspension when a continuation run has not yet executed its start step.
func (r *Runtime) resolveAcceptedRunLabels(ctx context.Context, accepted engine.AcceptedRun) (map[string]string, error) {
	if len(accepted.Labels) > 0 {
		return cloneLabels(accepted.Labels), nil
	}
	if accepted.SeedEndID == "" {
		return nil, nil
	}
	seed, err := r.Store.LoadRunSeed(ctx, accepted.RunID, accepted.SeedEndID)
	if err != nil {
		return nil, err
	}
	if seed.Declaration.PredecessorRunID == "" {
		return nil, nil
	}
	suspension, err := r.LoadRunSuspension(ctx, seed.Declaration.PredecessorRunID)
	if err != nil {
		if errors.Is(err, session.ErrRunSuspensionNotFound) {
			predecessor, loadErr := r.Store.LoadRun(ctx, seed.Declaration.PredecessorRunID)
			if loadErr != nil {
				return nil, loadErr
			}
			return cloneLabels(predecessor.Labels), nil
		}
		return nil, err
	}
	if _, serviceTool := accepted.Subject.Executor.AsServiceTool(); serviceTool {
		checkpoint, err := decodeRegisteredRootCheckpoint(suspension)
		if err != nil {
			return nil, err
		}
		return cloneLabels(checkpoint.Input.Labels), nil
	}
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	return cloneLabels(checkpoint.Context.Labels), nil
}

// loadRecordedRunSnapshotIfPresent replays the canonical run log when the run
// was recorded in Store concurrently with an engine query.
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
