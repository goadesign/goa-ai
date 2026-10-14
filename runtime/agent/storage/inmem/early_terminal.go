// Package inmem stores an execution's first failure or cancellation when no Run
// exists. The same mutex orders this write with preparation and Run creation;
// a stored terminal outcome prevents late work from publishing or starting.
package inmem

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RecordUnstartedExecutionTerminal validates the execution owner's command,
// then stores its first outcome or returns the exact already stored decision.
func (s *Store) RecordUnstartedExecutionTerminal(ctx context.Context, command genrunterminal.UnstartedExecutionTerminal) (genrunterminal.UnstartedExecutionTerminalResult, error) {
	if err := ctx.Err(); err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	encoded, err := genrunterminal.EncodeUnstartedExecutionTerminal(&command)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(err)
	}
	if bytes.Equal(command.RequestDigest, make([]byte, 32)) {
		return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(errors.New("accepted request digest is missing"))
	}
	selected, err := genrunterminal.DecodeUnstartedExecutionTerminal(encoded)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	sessionID := ""
	if selected.SessionID != nil {
		sessionID = *selected.SessionID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.seedOwnerExistsLocked(sessionID); err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(err)
	}
	if meta, exists := s.runs[selected.RunID]; exists {
		if !reflect.DeepEqual(meta.Subject, *selected.Subject) || meta.SessionID != sessionID || !reflect.DeepEqual(meta.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(session.ErrRunConflict)
		}
		if err := s.checkStartDigestLocked(selected.RunID, [32]byte(selected.RequestDigest)); err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(err)
		}
		return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionStarted(&genrunterminal.StartedExecution{})}, nil
	}
	if prior, exists := s.unstartedTerminals[selected.RunID]; exists {
		if !reflect.DeepEqual(prior.Subject, selected.Subject) || !reflect.DeepEqual(prior.SessionID, selected.SessionID) || !reflect.DeepEqual(prior.Origin, selected.Origin) || !bytes.Equal(prior.RequestDigest, selected.RequestDigest) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(session.ErrRunConflict)
		}
		saved, err := genrunterminal.EncodeUnstartedExecutionTerminal(&prior)
		if err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, err
		}
		winner, err := genrunterminal.DecodeUnstartedExecutionTerminal(saved)
		if err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, err
		}
		if bytes.Equal(saved, encoded) {
			return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionReplayed(winner)}, nil
		}
		return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionExisting(winner)}, nil
	}
	if preparation, exists := s.preparationOperations[selected.RunID]; exists {
		if !reflect.DeepEqual(preparation.Operation.Subject, *selected.Subject) || preparation.Operation.SessionID != sessionID || !reflect.DeepEqual(preparation.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(session.ErrRunConflict)
		}
	} else {
		root, isRoot := selected.Origin.Invocation.AsRoot()
		if !isRoot || root.RunID != selected.RunID {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(storage.ErrSeedConflict)
		}
	}
	if seed, exists := s.seeds[selected.RunID]; exists && seed.seed.Declaration.PredecessorRunID != "" {
		d := seed.seed.Declaration
		predecessor, exists := s.runs[d.PredecessorRunID]
		if !seed.published || !exists || predecessor.Status != session.RunStatusSuspended || predecessor.SessionID != sessionID || !reflect.DeepEqual(predecessor.Subject, *selected.Subject) || !reflect.DeepEqual(predecessor.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(session.ErrRunConflict)
		}
		// An execution that never started owns only its failure or cancellation.
		// Only StartRun can transfer the predecessor's saved work to a successor.
	}
	s.unstartedTerminals[selected.RunID] = *selected
	// Decode the retained bytes again so callers cannot mutate the stored winner.
	returned, err := genrunterminal.DecodeUnstartedExecutionTerminal(encoded)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionStored(returned)}, nil
}
