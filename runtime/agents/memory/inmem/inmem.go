// Package inmem provides an in-memory implementation of memory.Store for
// testing and local development. Data is stored in process memory and is
// lost when the process exits. Production deployments should use a durable
// backend such as features/memory/mongo (MongoDB-backed implementation).
package inmem

import (
	"context"
	"sync"

	"goa.design/goa-ai/runtime/agents/memory"
)

// Store implements memory.Store using an in-process map keyed by agent ID
// and run ID. It is thread-safe and suitable for tests and local development.
// Data is not persisted across restarts.
//
// The store maintains a two-level map: agentID -> runID -> events, allowing
// efficient isolation between agents and runs. All operations defensively
// copy data to prevent external mutation.
type Store struct {
	mu   sync.RWMutex
	runs map[string]map[string][]memory.Event
}

// New returns a new in-memory store instance with no events. The store is
// ready to use immediately and requires no initialization or cleanup.
func New() *Store {
	return &Store{runs: make(map[string]map[string][]memory.Event)}
}

// LoadRun retrieves the snapshot for the given agent and run. Returns an empty
// snapshot (not an error) if the run doesn't exist, allowing callers to treat
// absence as empty history. The returned snapshot contains a defensive copy of
// events to prevent external mutation.
//
// Thread-safe: concurrent reads are allowed and do not block each other.
func (s *Store) LoadRun(_ context.Context, agentID, runID string) (memory.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runs := s.runs[agentID]
	if runs == nil {
		return memory.Snapshot{AgentID: agentID, RunID: runID, Meta: make(map[string]any)}, nil
	}
	events := runs[runID]
	cloned := make([]memory.Event, len(events))
	copy(cloned, events)
	return memory.Snapshot{AgentID: agentID, RunID: runID, Events: cloned, Meta: make(map[string]any)}, nil
}

// AppendEvents appends the provided events to the run history. Events are copied
// defensively to ensure callers cannot mutate the internal store. If events is
// empty, this is a no-op.
//
// Thread-safe: concurrent writes acquire an exclusive lock and serialize. Writes
// to different runs do not block each other but are still serialized within this
// implementation.
func (s *Store) AppendEvents(_ context.Context, agentID, runID string, events ...memory.Event) error {
	if len(events) == 0 {
		return nil
	}
	copied := make([]memory.Event, len(events))
	copy(copied, events)

	s.mu.Lock()
	defer s.mu.Unlock()
	runs := s.runs[agentID]
	if runs == nil {
		runs = make(map[string][]memory.Event)
		s.runs[agentID] = runs
	}
	runs[runID] = append(runs[runID], copied...)
	return nil
}

// Reset clears all stored events across all agents and runs. Primarily useful
// in tests to reset state between test cases. Not typically needed in production
// since inmem stores are ephemeral.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = make(map[string]map[string][]memory.Event)
}
