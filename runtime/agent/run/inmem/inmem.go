// Package inmem provides an in-memory implementation of run.Store for testing
// and local development. The store holds run metadata in a map, keyed by RunID,
// with no persistence across process restarts. Use this for unit tests or
// prototyping; production deployments should use a durable backend such as
// features/run/mongo (MongoDB-backed implementation).
package inmem

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/run"
)

// Store implements run.Store in memory with no durability. All operations are
// thread-safe via sync.RWMutex. Records are defensively copied on read and write
// to prevent accidental mutation of stored data. This implementation is suitable
// for tests and local tooling but should not be used in production where run
// metadata needs to survive process restarts.
type Store struct {
	mu      sync.RWMutex
	records map[string]run.Record
}

// New constructs an empty Store with no recorded runs. The returned store is
// immediately ready for use and requires no additional configuration.
func New() *Store {
	return &Store{records: make(map[string]run.Record)}
}

// Upsert inserts a new run record or updates an existing one, keyed by r.RunID.
// If the record already exists and r.StartedAt is zero, the original StartedAt
// timestamp is preserved. Otherwise, StartedAt defaults to time.Now() for new
// records. UpdatedAt is always set to time.Now() if zero. Labels and Metadata
// are defensively copied to prevent external mutation of the stored record.
//
// This method is thread-safe and will never return an error (the error return
// exists only to satisfy the run.Store interface).
func (s *Store) Upsert(_ context.Context, r run.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.records[r.RunID]
	if ok {
		if r.StartedAt.IsZero() {
			r.StartedAt = existing.StartedAt
		}
	} else if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now()
	}
	copied := r
	copied.Labels = cloneLabels(r.Labels)
	copied.Metadata = cloneMetadata(r.Metadata)
	s.records[r.RunID] = copied
	return nil
}

// Load retrieves the run record for the given runID. If the run does not exist,
// returns an empty run.Record with no error (callers should check r.RunID == "").
// The returned record is a defensive copy; mutations to Labels or Metadata will
// not affect the stored record.
//
// This method is thread-safe and will never return an error (the error return
// exists only to satisfy the run.Store interface).
func (s *Store) Load(_ context.Context, runID string) (run.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[runID]
	if !ok {
		return run.Record{}, nil
	}
	r.Labels = cloneLabels(r.Labels)
	r.Metadata = cloneMetadata(r.Metadata)
	return r, nil
}

// Reset clears all stored records, resetting the store to an empty state. This
// is useful in tests to ensure isolation between test cases. This method is
// thread-safe but is not part of the run.Store interface.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]run.Record)
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
