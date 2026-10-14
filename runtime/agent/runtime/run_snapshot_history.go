// Package runtime loads the completed event logs of suspended executions
// continued by a run. A continuation's conversation cutoff can precede its tool
// schedules, so execution state uses the whole suspended log. New conversation
// turns do not inherit unfinished execution state.
package runtime

import (
	"context"
	"fmt"
	"reflect"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// snapshotInheritedEvents follows immutable continuation declarations through
// the owning store and returns their suspended event logs oldest first. It
// verifies the saved conversation position without truncating later tool events
// and rejects changed owners, unfinished predecessors, and repeated runs before
// a snapshot can treat an earlier schedule as unfinished work in this run.
func (r *Runtime) snapshotInheritedEvents(ctx context.Context, current session.RunMeta) ([]*runlog.Event, error) {
	if _, err := genrunidentity.EncodeRunSubject(&current.Subject); err != nil {
		return nil, storage.NewContractError(fmt.Errorf("snapshot run subject: %w", err))
	}
	seen := map[string]struct{}{current.RunID: {}}
	var histories [][]*runlog.Event
	for current.SeedEndID != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seed, err := r.Store.LoadRunSeed(ctx, current.RunID, current.SeedEndID)
		if err != nil {
			return nil, err
		}
		d := seed.Declaration
		if d.RunID != current.RunID || !reflect.DeepEqual(d.Subject, current.Subject) || d.SessionID != current.SessionID || seed.EndID != current.SeedEndID {
			return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
		}
		if d.PredecessorRunID == "" {
			break
		}
		if err := storage.ValidateSeedDeclaration(d); err != nil {
			return nil, storage.NewContractError(err)
		}
		source := seed.Source
		if d.Kind == storage.SeedLiteral && source != nil {
			return nil, storage.NewContractError(storage.ErrSeedConflict)
		}
		if d.Kind == storage.SeedContinuation && (source == nil || source.RunID != d.SourceRunID || source.EndID != d.SourceEndID || source.ExcludeSystem || source.ExcludeReasoning) {
			return nil, storage.NewContractError(storage.ErrSeedConflict)
		}
		if _, repeated := seen[d.PredecessorRunID]; repeated {
			return nil, storage.NewContractError(fmt.Errorf("snapshot continuation history repeats run %q", d.PredecessorRunID))
		}
		seen[d.PredecessorRunID] = struct{}{}
		predecessor, err := r.Store.LoadRun(ctx, d.PredecessorRunID)
		if err != nil {
			return nil, err
		}
		if predecessor.RunID != d.PredecessorRunID || !reflect.DeepEqual(predecessor.Subject, current.Subject) || predecessor.SessionID != current.SessionID {
			return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
		}
		if predecessor.Status != session.RunStatusSuspended {
			return nil, storage.NewContractError(fmt.Errorf("snapshot continuation source %q is not suspended", d.PredecessorRunID))
		}
		events, err := r.loadRunEvents(ctx, d.PredecessorRunID)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event == nil || !reflect.DeepEqual(event.Subject, predecessor.Subject) || event.SessionID != predecessor.SessionID {
				return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
			}
		}
		// Suspension closes ordinary writes in the store. Its final event seals
		// all schedules and results, including those after the last message.
		if len(events) == 0 || events[len(events)-1].Type != hooks.RunSuspended {
			return nil, storage.NewContractError(fmt.Errorf("snapshot continuation source %q has no final suspension record", d.PredecessorRunID))
		}
		if _, err := hooks.DecodeRunlogEvent(events[len(events)-1]); err != nil {
			return nil, err
		}
		foundPosition := d.Kind == storage.SeedLiteral
		for _, event := range events {
			if source != nil && event.ID == source.EndID {
				foundPosition = true
				break
			}
		}
		if !foundPosition {
			return nil, storage.NewContractError(storage.ErrInvalidTranscriptPosition)
		}
		histories = append(histories, events)
		current = predecessor
	}
	var inherited []*runlog.Event
	for index := len(histories) - 1; index >= 0; index-- {
		inherited = append(inherited, histories[index]...)
	}
	return inherited, nil
}
