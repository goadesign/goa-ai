// Package runtime records continuation relationships and resolves every prompt
// version that contributed to one run through earlier runs and child agents.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

type (
	promptRunRelation struct {
		kind         promptRunRelationKind
		runID        string
		childAgentID string
	}

	promptRunRelationKind uint8
)

const (
	continuationPromptRunRelation promptRunRelationKind = 1
	childPromptRunRelation        promptRunRelationKind = 2
)

var (
	// errPromptRefsCorrupt indicates that stored run relationships do not match
	// the immutable identity of the runs they connect.
	errPromptRefsCorrupt = errors.New("prompt run relationships corrupt")
)

// ResolvePromptRefs returns prompt refs for the run, its continuation history,
// and all child runs reachable from that history.
//
// The run must belong to sessionID. A run in another session is reported as
// missing so callers cannot use this method to discover runs outside their
// requested session.
//
// Continuation predecessors, child links, and prompt versions are derived from
// the stored run log instead of duplicated in mutable session metadata.
func (r *Runtime) ResolvePromptRefs(ctx context.Context, sessionID, runID string) ([]prompt.PromptRef, error) {
	root, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if root.SessionID != sessionID {
		return nil, session.ErrRunNotFound
	}
	if root.RunID != runID {
		return nil, fmt.Errorf("%w: requested run %q loaded as %q", errPromptRefsCorrupt, runID, root.RunID)
	}
	queue := []session.RunMeta{root}
	loadedRuns := map[string]session.RunMeta{runID: root}
	seenRefs := make(map[prompt.PromptRef]struct{})
	relatedRunsByRun := make(map[string][]string)
	var refs []prompt.PromptRef
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		runRefs, relatedRuns, err := r.listRunPromptData(ctx, current)
		if err != nil {
			return nil, err
		}
		for _, ref := range runRefs {
			if _, seen := seenRefs[ref]; seen {
				continue
			}
			seenRefs[ref] = struct{}{}
			refs = append(refs, ref)
		}
		for _, relation := range relatedRuns {
			_, seen := loadedRuns[relation.runID]
			related, err := r.loadPromptRelatedRun(ctx, current, relation, loadedRuns)
			if err != nil {
				return nil, err
			}
			relatedRunsByRun[current.RunID] = append(relatedRunsByRun[current.RunID], related.RunID)
			if seen {
				continue
			}
			queue = append(queue, related)
		}
	}
	if err := validatePromptRunPaths(runID, relatedRunsByRun); err != nil {
		return nil, err
	}
	return refs, nil
}

// listRunPromptData pages one reachable run and decodes only the prompt,
// start, and child-link records needed by ResolvePromptRefs. Malformed
// records belonging to unrelated runs are never loaded.
func (r *Runtime) listRunPromptData(ctx context.Context, current session.RunMeta) ([]prompt.PromptRef, []promptRunRelation, error) {
	if current.StartOutcome != session.RunStartProceed && current.StartOutcome != session.RunStartStop {
		return nil, nil, fmt.Errorf(
			"%w: run %q has unknown start outcome %q",
			errPromptRefsCorrupt,
			current.RunID,
			current.StartOutcome,
		)
	}
	var refs []prompt.PromptRef
	var relatedRuns []promptRunRelation
	runStartedSeen := false
	runCompletedSeen := false
	cursor := ""
	for {
		page, err := r.Store.ListRunRecords(ctx, current.RunID, cursor, 500)
		if err != nil {
			return nil, nil, err
		}
		for _, record := range page.Events {
			if !runStartedSeen && record.Type != hooks.RunStarted {
				return nil, nil, fmt.Errorf(
					"%w: run %q first record is %q instead of a start record",
					errPromptRefsCorrupt,
					current.RunID,
					record.Type,
				)
			}
			if record.Type == hooks.RunStarted {
				if runStartedSeen {
					return nil, nil, fmt.Errorf(
						"%w: run %q has more than one start record",
						errPromptRefsCorrupt,
						current.RunID,
					)
				}
				runStartedSeen = true
				if err := validatePromptRecordOwner(record, current); err != nil {
					return nil, nil, err
				}
				event, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
					Type: record.Type, RunID: record.RunID, AgentID: record.AgentID,
					SessionID: record.SessionID, TurnID: record.TurnID,
					TimestampMS: record.Timestamp.UnixMilli(), Payload: record.Payload,
				})
				if err != nil {
					return nil, nil, fmt.Errorf("decode run %q start record: %w", current.RunID, err)
				}
				started := event.(*hooks.RunStartedEvent)
				if started.ParentRunID != current.ParentRunID {
					return nil, nil, fmt.Errorf(
						"%w: run %q start record has a different parent",
						errPromptRefsCorrupt,
						current.RunID,
					)
				}
				if started.PredecessorRunID != "" {
					relatedRuns = append(relatedRuns, promptRunRelation{
						kind:  continuationPromptRunRelation,
						runID: started.PredecessorRunID,
					})
				}
				continue
			}
			if current.StartOutcome == session.RunStartStop {
				if record.Type != hooks.RunCompleted {
					return nil, nil, fmt.Errorf(
						"%w: stopped run %q has record type %q",
						errPromptRefsCorrupt,
						current.RunID,
						record.Type,
					)
				}
				if runCompletedSeen {
					return nil, nil, fmt.Errorf(
						"%w: stopped run %q has more than one completion record",
						errPromptRefsCorrupt,
						current.RunID,
					)
				}
				if err := validatePromptRecordOwner(record, current); err != nil {
					return nil, nil, err
				}
				if !record.Timestamp.Equal(current.StartedAt) {
					return nil, nil, fmt.Errorf(
						"%w: stopped run %q completion time does not match its start time",
						errPromptRefsCorrupt,
						current.RunID,
					)
				}
				if err := lifecycle.ValidateRunTerminal(storage.RunTerminal{
					RunID:  current.RunID,
					Status: session.RunStatusCanceled,
					Record: record,
				}, current); err != nil {
					return nil, nil, fmt.Errorf(
						"%w: stopped run %q has invalid completion record: %w",
						errPromptRefsCorrupt,
						current.RunID,
						err,
					)
				}
				runCompletedSeen = true
				continue
			}
			if record.Type != hooks.PromptRendered && record.Type != hooks.ChildRunLinked {
				continue
			}
			if err := validatePromptRecordOwner(record, current); err != nil {
				return nil, nil, err
			}
			event, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
				Type: record.Type, RunID: record.RunID, AgentID: record.AgentID,
				SessionID: record.SessionID, TurnID: record.TurnID,
				TimestampMS: record.Timestamp.UnixMilli(), Payload: record.Payload,
			})
			if err != nil {
				return nil, nil, err
			}
			switch event := event.(type) {
			case *hooks.PromptRenderedEvent:
				refs = append(refs, prompt.PromptRef{ID: event.PromptID, Version: event.Version})
			case *hooks.ChildRunLinkedEvent:
				relatedRuns = append(relatedRuns, promptRunRelation{
					kind:         childPromptRunRelation,
					runID:        event.ChildRunID,
					childAgentID: string(event.ChildAgentID),
				})
			}
		}
		if page.NextCursor == "" {
			if !runStartedSeen {
				return nil, nil, fmt.Errorf(
					"%w: run %q has no start record",
					errPromptRefsCorrupt,
					current.RunID,
				)
			}
			if current.StartOutcome == session.RunStartStop {
				if !runCompletedSeen {
					return nil, nil, fmt.Errorf(
						"%w: stopped run %q has no completion record",
						errPromptRefsCorrupt,
						current.RunID,
					)
				}
				return nil, relatedRuns, nil
			}
			return refs, relatedRuns, nil
		}
		cursor = page.NextCursor
	}
}

// loadPromptRelatedRun checks the immutable identity required by one stored
// continuation or child relationship before its records enter the traversal.
func (r *Runtime) loadPromptRelatedRun(
	ctx context.Context,
	current session.RunMeta,
	relation promptRunRelation,
	loadedRuns map[string]session.RunMeta,
) (session.RunMeta, error) {
	if relation.kind == continuationPromptRunRelation && relation.runID == current.RunID {
		return session.RunMeta{}, fmt.Errorf(
			"%w: run %q is its own continuation predecessor",
			errPromptRefsCorrupt,
			current.RunID,
		)
	}
	related, loaded := loadedRuns[relation.runID]
	if !loaded {
		var err error
		related, err = r.Store.LoadRun(ctx, relation.runID)
		if err != nil {
			if errors.Is(err, session.ErrRunNotFound) {
				return session.RunMeta{}, fmt.Errorf(
					"%w: run %q links missing run %q",
					errPromptRefsCorrupt,
					current.RunID,
					relation.runID,
				)
			}
			return session.RunMeta{}, fmt.Errorf("load related run %q: %w", relation.runID, err)
		}
		if related.RunID != relation.runID {
			return session.RunMeta{}, fmt.Errorf(
				"%w: requested related run %q loaded as %q",
				errPromptRefsCorrupt,
				relation.runID,
				related.RunID,
			)
		}
		loadedRuns[relation.runID] = related
	}
	switch relation.kind {
	case continuationPromptRunRelation:
		if related.SessionID != current.SessionID || related.AgentID != current.AgentID ||
			related.ParentRunID != current.ParentRunID || related.Status != session.RunStatusSuspended {
			return session.RunMeta{}, fmt.Errorf(
				"%w: continuation from run %q to run %q has invalid predecessor identity or status",
				errPromptRefsCorrupt,
				current.RunID,
				related.RunID,
			)
		}
	case childPromptRunRelation:
		if related.SessionID != current.SessionID || related.AgentID != relation.childAgentID ||
			related.ParentRunID != current.RunID {
			return session.RunMeta{}, fmt.Errorf(
				"%w: child link from run %q does not match run %q identity",
				errPromptRefsCorrupt,
				current.RunID,
				related.RunID,
			)
		}
	default:
		return session.RunMeta{}, fmt.Errorf(
			"%w: run %q has an unknown relationship to run %q",
			errPromptRefsCorrupt,
			current.RunID,
			related.RunID,
		)
	}
	return related, nil
}

// validatePromptRunPaths rejects a cycle while allowing separate valid paths
// to reach the same run. The traversal has already loaded each run once.
func validatePromptRunPaths(rootRunID string, relatedRunsByRun map[string][]string) error {
	return visitPromptRunPath(
		rootRunID,
		relatedRunsByRun,
		make(map[string]struct{}),
		make(map[string]struct{}),
	)
}

// visitPromptRunPath keeps the runs on the current path separate from runs
// whose descendants were already checked through another path.
func visitPromptRunPath(
	runID string,
	relatedRunsByRun map[string][]string,
	ancestors map[string]struct{},
	checked map[string]struct{},
) error {
	if _, found := ancestors[runID]; found {
		return fmt.Errorf("%w: relationship cycle reaches run %q", errPromptRefsCorrupt, runID)
	}
	if _, found := checked[runID]; found {
		return nil
	}
	ancestors[runID] = struct{}{}
	for _, relatedRunID := range relatedRunsByRun[runID] {
		if err := visitPromptRunPath(relatedRunID, relatedRunsByRun, ancestors, checked); err != nil {
			return err
		}
	}
	delete(ancestors, runID)
	checked[runID] = struct{}{}
	return nil
}

// validatePromptRecordOwner checks that a selected record belongs to the run
// whose prompt relationships are being read.
func validatePromptRecordOwner(record *runlog.Event, current session.RunMeta) error {
	if record.RunID != current.RunID || record.SessionID != current.SessionID ||
		string(record.AgentID) != current.AgentID {
		return fmt.Errorf(
			"%w: record %q does not belong to run %q",
			errPromptRefsCorrupt,
			record.EventKey,
			current.RunID,
		)
	}
	return nil
}
