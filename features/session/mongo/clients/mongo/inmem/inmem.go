package inmem

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/agents/runtime/session"
)

// Store provides an in-memory implementation of session.Store for tests and local tooling.
type Store struct {
	mu   sync.RWMutex
	runs map[string]session.Run
}

// New returns a Store with no recorded runs.
func New() *Store {
	return &Store{runs: make(map[string]session.Run)}
}

// Upsert inserts or updates the run metadata.
func (s *Store) Upsert(ctx context.Context, run session.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.runs[run.RunID]
	if ok {
		if run.StartedAt.IsZero() {
			run.StartedAt = existing.StartedAt
		}
	} else if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now()
	}
	copied := run
	copied.Labels = cloneLabels(run.Labels)
	copied.Metadata = cloneMetadata(run.Metadata)
	s.runs[run.RunID] = copied
	return nil
}

// Load returns the stored run metadata. Missing runs return zero Run and no error.
func (s *Store) Load(ctx context.Context, runID string) (session.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return session.Run{}, nil
	}
	run.Labels = cloneLabels(run.Labels)
	run.Metadata = cloneMetadata(run.Metadata)
	return run, nil
}

// Reset clears all stored metadata (useful in tests).
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = make(map[string]session.Run)
}

func cloneLabels(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
