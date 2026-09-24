package inmem

// Seed mutations share the session/run lock with start and purge. A private
// declaration reserves the run ID without making an accepted workflow visible.

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	preparationKey struct {
		runID     string
		attemptID string
	}

	seedState struct {
		seed          storage.RunSeed
		records       []storage.SeedRecord
		keys          map[string]int
		endID         string
		preparedBytes int64
		published     bool
		abandoned     bool
		hasSource     bool
	}
)

// BeginRunSeed reserves the destination and captures the source under the same
// lock as lifecycle writes. Failed declarations do not reserve anything.
func (s *Store) BeginRunSeed(ctx context.Context, declaration storage.SeedDeclaration) (storage.RunSeed, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunSeed{}, err
	}
	if err := storage.ValidateSeedDeclaration(declaration); err != nil {
		return contractResult(storage.RunSeed{}, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.seedOwnerExistsLocked(declaration.SessionID); err != nil {
		return contractResult(storage.RunSeed{}, err)
	}
	operation := storage.PreparationOperation{AgentID: declaration.AgentID, RunID: declaration.RunID, SessionID: declaration.SessionID, CommandID: declaration.CommandID}
	if existing, exists := s.preparationOperations[declaration.RunID]; exists && existing != operation {
		return contractResult(storage.RunSeed{}, storage.ErrSeedConflict)
	}
	key := preparationKey{declaration.RunID, declaration.AttemptID}
	if existing, ok := s.preparations[key]; ok {
		if existing.abandoned {
			return contractResult(storage.RunSeed{}, storage.ErrPreparationAbandoned)
		}
		if !storage.EqualSeedDeclarations(existing.seed.Declaration, declaration) {
			return contractResult(storage.RunSeed{}, storage.ErrSeedConflict)
		}
		return cloneSeed(existing.seed), nil
	}
	if _, accepted := s.seeds[declaration.RunID]; accepted {
		return contractResult(storage.RunSeed{}, storage.ErrSeedConflict)
	}
	if _, exists := s.runs[declaration.RunID]; exists {
		return contractResult(storage.RunSeed{}, session.ErrRunConflict)
	}
	source, err := s.seedSourceLocked(declaration)
	if err != nil {
		return contractResult(storage.RunSeed{}, err)
	}
	seed := cloneSeed(storage.RunSeed{
		Declaration: declaration,
		Source:      source,
		EndID:       storage.EmptySeedEndID,
	})
	s.preparationOperations[declaration.RunID] = operation
	s.preparations[key] = &seedState{
		seed: seed, keys: make(map[string]int), endID: storage.EmptySeedEndID,
	}
	return cloneSeed(seed), nil
}

// AppendRunSeed accepts one bounded literal or the declared source exactly
// once. A lost response can be retried using the same key, position and bytes.
func (s *Store) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := storage.ValidateSeedAppend(command); err != nil {
		return contractResult("", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.preparationLocked(command.RunID, command.AttemptID)
	if err != nil {
		return contractResult("", err)
	}
	record := command.Record
	if index, exists := state.keys[record.Key]; exists {
		stored := state.records[index]
		record.ID = stored.ID
		if !reflect.DeepEqual(stored, record) {
			return contractResult("", storage.ErrSeedConflict)
		}
		return stored.ID, nil
	}
	if state.published || record.PreviousID != state.endID {
		return contractResult("", storage.ErrSeedConflict)
	}
	if state.literalOpen() && record.LiteralPart == nil {
		return contractResult("", storage.ErrSeedConflict)
	}
	if state.preparedBytes > 0 && len(record.Prepared) == 0 {
		return contractResult("", storage.ErrSeedConflict)
	}
	if err := s.seedOwnerExistsLocked(state.seed.Declaration.SessionID); err != nil {
		return contractResult("", err)
	}
	if record.Prefix != nil {
		if state.hasSource || !reflect.DeepEqual(record.Prefix, state.seed.Source) {
			return contractResult("", storage.ErrSeedConflict)
		}
	}
	record.ID = strconv.Itoa(len(state.records) + 1)
	if err := storage.ValidateSeedPageSize(storage.SeedPage{
		Records: []storage.SeedRecord{record}, NextCursor: record.ID,
	}); err != nil {
		return contractResult("", err)
	}
	if record.Prefix != nil {
		state.hasSource = true
	}
	state.keys[record.Key] = len(state.records)
	state.records = append(state.records, cloneSeedRecord(record))
	state.endID = record.ID
	if len(record.Prepared) > 0 {
		state.preparedBytes += int64(len(record.Prepared))
	} else {
		state.seed.EndID = record.ID
	}
	return record.ID, nil
}

// PublishRunSeed seals only the full accepted chain. A referenced source must
// occur exactly once; no literal prefix may accidentally become executable.
func (s *Store) PublishRunSeed(ctx context.Context, publication storage.SeedPublication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.preparationLocked(publication.RunID, publication.AttemptID)
	if err != nil {
		return storage.NewContractError(err)
	}
	if err := s.seedOwnerExistsLocked(state.seed.Declaration.SessionID); err != nil {
		return storage.NewContractError(err)
	}
	if publication.SeedEndID != state.seed.EndID || publication.EndID != state.endID ||
		publication.PreparedBytes <= 0 || publication.PreparedBytes != state.preparedBytes ||
		(state.seed.Source != nil && !state.hasSource) || state.literalOpen() {
		return storage.NewContractError(storage.ErrSeedConflict)
	}
	if accepted, exists := s.seeds[publication.RunID]; exists && accepted != state {
		return storage.NewContractError(storage.ErrSeedConflict)
	}
	state.published = true
	s.seeds[publication.RunID] = state
	return nil
}

// FindRunPreparation reads only the accepted value for the original command.
// Absence does not stop any upload that is still in progress.
func (s *Store) FindRunPreparation(ctx context.Context, operation storage.PreparationOperation) (storage.RunPreparation, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunPreparation{}, false, err
	}
	if err := storage.ValidatePreparationOperation(operation); err != nil {
		return storage.RunPreparation{}, false, storage.NewContractError(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acceptedPreparationLocked(operation)
}

// SettleRunPreparation and publication hold the same lock. A missing attempt
// is recorded as abandoned so a late first begin cannot reopen it.
func (s *Store) SettleRunPreparation(ctx context.Context, attempt storage.PreparationAttempt) (storage.RunPreparation, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunPreparation{}, false, err
	}
	if err := storage.ValidatePreparationAttempt(attempt); err != nil {
		return storage.RunPreparation{}, false, storage.NewContractError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accepted, found, err := s.acceptedPreparationLocked(attempt.Operation)
	if err != nil || found {
		return accepted, found, err
	}
	s.preparationOperations[attempt.Operation.RunID] = attempt.Operation
	key := preparationKey{attempt.Operation.RunID, attempt.AttemptID}
	state, exists := s.preparations[key]
	if exists && !preparationMatches(state, attempt.Operation) {
		return storage.RunPreparation{}, false, storage.NewContractError(storage.ErrSeedConflict)
	}
	if !exists {
		state = &seedState{seed: storage.RunSeed{Declaration: storage.SeedDeclaration{
			AgentID: attempt.Operation.AgentID, RunID: attempt.Operation.RunID,
			SessionID: attempt.Operation.SessionID, CommandID: attempt.Operation.CommandID,
			AttemptID: attempt.AttemptID,
		}}}
		s.preparations[key] = state
	}
	state.abandoned = true
	return storage.RunPreparation{}, false, nil
}

// LoadRunSeed exposes a publication only while its owning session exists.
func (s *Store) LoadRunSeed(ctx context.Context, runID, endID string) (storage.RunSeed, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunSeed{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, err := s.publishedSeedLocked(runID, endID)
	if err != nil {
		return contractResult(storage.RunSeed{}, err)
	}
	return cloneSeed(state.seed), nil
}

// ListRunSeedRecords returns at most 512 records within the logical page budget.
// Cursor validation and copying occur under the same lock as owner deletion.
func (s *Store) ListRunSeedRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.SeedPage{}, err
	}
	if limit <= 0 {
		return contractResult(storage.SeedPage{}, storage.ErrInvalidTranscriptPosition)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, err := s.publishedSeedLocked(runID, endID)
	if err != nil {
		return contractResult(storage.SeedPage{}, err)
	}
	end, err := strconv.Atoi(state.seed.EndID)
	if err != nil {
		return contractResult(storage.SeedPage{}, err)
	}
	return preparationPage(state, 0, end, afterID, limit)
}

// ListRunPreparationRecords returns only the compiled-start suffix of the
// accepted body. History bytes are not copied into the recovery value.
func (s *Store) ListRunPreparationRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.SeedPage{}, err
	}
	if limit <= 0 {
		return contractResult(storage.SeedPage{}, storage.ErrInvalidTranscriptPosition)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, err := s.seedLocked(runID)
	if err != nil {
		return contractResult(storage.SeedPage{}, err)
	}
	if !state.published || state.endID != endID {
		return contractResult(storage.SeedPage{}, storage.ErrSeedConflict)
	}
	start, err := strconv.Atoi(state.seed.EndID)
	if err != nil {
		return contractResult(storage.SeedPage{}, err)
	}
	return preparationPage(state, start, len(state.records), afterID, limit)
}

// preparationPage applies the accepted history or compiled-start range before
// copying any records. Each continuation names the last returned record.
func preparationPage(state *seedState, start, end int, afterID string, limit int) (storage.SeedPage, error) {
	after := start
	if afterID != "" {
		parsed, err := strconv.Atoi(afterID)
		if err != nil || parsed <= start || parsed > end || strconv.Itoa(parsed) != afterID {
			return contractResult(storage.SeedPage{}, storage.ErrInvalidTranscriptPosition)
		}
		after = parsed
	}
	limit = min(limit, transcriptPageMaxRecords)
	page := storage.SeedPage{}
	for index := after; index < end; index++ {
		record := state.records[index]
		if len(page.Records) == limit {
			page.NextCursor = page.Records[len(page.Records)-1].ID
			break
		}
		page.Records = append(page.Records, record)
		// Count the complete response, including a potential next cursor.
		page.NextCursor = record.ID
		if err := storage.ValidateSeedPageSize(page); err != nil {
			page.Records = page.Records[:len(page.Records)-1]
			if len(page.Records) == 0 {
				return contractResult(storage.SeedPage{}, err)
			}
			page.NextCursor = page.Records[len(page.Records)-1].ID
			break
		}
		page.Records[len(page.Records)-1] = cloneSeedRecord(record)
		page.NextCursor = ""
	}
	return page, nil
}

func preparationMatches(state *seedState, operation storage.PreparationOperation) bool {
	d := state.seed.Declaration
	return d.RunID == operation.RunID && d.AgentID == operation.AgentID &&
		d.SessionID == operation.SessionID && d.CommandID == operation.CommandID
}

func (s *Store) acceptedPreparationLocked(operation storage.PreparationOperation) (storage.RunPreparation, bool, error) {
	if err := s.seedOwnerExistsLocked(operation.SessionID); err != nil {
		return storage.RunPreparation{}, false, storage.NewContractError(err)
	}
	if original, exists := s.preparationOperations[operation.RunID]; exists && original != operation {
		return storage.RunPreparation{}, false, storage.NewContractError(storage.ErrSeedConflict)
	}
	state, found := s.seeds[operation.RunID]
	if !found {
		return storage.RunPreparation{}, false, nil
	}
	if !preparationMatches(state, operation) {
		return storage.RunPreparation{}, false, storage.NewContractError(storage.ErrSeedConflict)
	}
	return storage.RunPreparation{
		Seed: cloneSeed(state.seed), EndID: state.endID, PreparedBytes: state.preparedBytes,
	}, true, nil
}

func (s *Store) preparationLocked(runID, attemptID string) (*seedState, error) {
	state, found := s.preparations[preparationKey{runID, attemptID}]
	if !found {
		return nil, storage.ErrSeedNotFound
	}
	if err := s.seedOwnerExistsLocked(state.seed.Declaration.SessionID); err != nil {
		return nil, err
	}
	if state.abandoned {
		return nil, storage.ErrPreparationAbandoned
	}
	return state, nil
}

// seedSourceLocked proves that a reference belongs to the same agent/session
// and captures the successful completion position without caller inference.
func (s *Store) seedSourceLocked(d storage.SeedDeclaration) (*storage.HistoryPrefix, error) {
	if d.Kind == storage.SeedLiteral {
		return nil, nil
	}
	source, ok := s.runs[d.SourceRunID]
	if !ok {
		return nil, session.ErrRunNotFound
	}
	if source.SessionID != d.SessionID || source.AgentID != d.AgentID {
		return nil, storage.ErrRunRecordOwnerMismatch
	}
	expected := session.RunStatusCompleted
	if d.Kind == storage.SeedContinuation {
		expected = session.RunStatusSuspended
	}
	if source.Status != expected {
		return nil, fmt.Errorf("history source %q has status %q, want %q", d.SourceRunID, source.Status, expected)
	}
	endID := d.SourceEndID
	if d.Kind == storage.SeedNextTurn {
		terminal := s.recordsByKey[d.SourceRunID][s.lifecycle[d.SourceRunID].terminal]
		if terminal == nil {
			return nil, fmt.Errorf("completed history source %q has no terminal record", d.SourceRunID)
		}
		endID = terminal.ID
	} else {
		position, err := strconv.Atoi(endID)
		records := s.records[d.SourceRunID]
		if err != nil || position <= 0 || position > len(records) || records[position-1].ID != endID {
			return nil, storage.ErrInvalidTranscriptPosition
		}
	}
	return &storage.HistoryPrefix{
		RunID: d.SourceRunID, EndID: endID,
		ExcludeSystem: d.Kind == storage.SeedNextTurn, ExcludeReasoning: d.ExcludeReasoning,
	}, nil
}

func (s *Store) seedOwnerExistsLocked(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if _, purged := s.purged[sessionID]; purged {
		return session.ErrSessionPurged
	}
	if _, exists := s.sessions[sessionID]; !exists {
		return session.ErrSessionNotFound
	}
	return nil
}

func (s *Store) seedLocked(runID string) (*seedState, error) {
	state, ok := s.seeds[runID]
	if !ok {
		return nil, storage.ErrSeedNotFound
	}
	if err := s.seedOwnerExistsLocked(state.seed.Declaration.SessionID); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Store) publishedSeedLocked(runID, endID string) (*seedState, error) {
	state, err := s.seedLocked(runID)
	if err != nil {
		return nil, err
	}
	if !state.published {
		return nil, storage.ErrSeedNotPublished
	}
	if endID != state.seed.EndID {
		return nil, storage.ErrSeedConflict
	}
	return state, nil
}

// validateStartSeedLocked rejects any start that does not attach the exact
// published initial history reserved for this agent and session.
func (s *Store) validateStartSeedLocked(start session.RunStart) error {
	state, err := s.publishedSeedLocked(start.RunID, start.SeedEndID)
	if err != nil {
		return err
	}
	d := state.seed.Declaration
	if d.AgentID != start.AgentID || d.SessionID != start.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	if (d.Kind == storage.SeedContinuation) != (start.PredecessorRunID != "") ||
		(d.Kind == storage.SeedContinuation && d.SourceRunID != start.PredecessorRunID) {
		return storage.ErrSeedConflict
	}
	return nil
}

func cloneSeed(seed storage.RunSeed) storage.RunSeed {
	seed.Declaration.RenderedPrompts = slices.Clone(seed.Declaration.RenderedPrompts)
	for index := range seed.Declaration.RenderedPrompts {
		event := &seed.Declaration.RenderedPrompts[index]
		event.Scope.Labels = maps.Clone(event.Scope.Labels)
	}
	if seed.Source != nil {
		source := *seed.Source
		seed.Source = &source
	}
	return seed
}

func cloneSeedRecord(record storage.SeedRecord) storage.SeedRecord {
	record.Messages = bytes.Clone(record.Messages)
	record.Prepared = bytes.Clone(record.Prepared)
	if record.LiteralPart != nil {
		part := *record.LiteralPart
		part.Data = bytes.Clone(part.Data)
		record.LiteralPart = &part
	}
	if record.Prefix != nil {
		prefix := *record.Prefix
		record.Prefix = &prefix
	}
	return record
}

// literalOpen derives incomplete byte framing from the last accepted record.
func (s *seedState) literalOpen() bool {
	if len(s.records) == 0 {
		return false
	}
	part := s.records[len(s.records)-1].LiteralPart
	return part != nil && !part.Final
}
