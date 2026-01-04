// Package inmem provides an in-memory implementation of runlog.Store.
//
// The in-memory store is intended for tests and local development. It is not
// durable and should not be used in production.
package inmem

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"goa.design/goa-ai/runtime/agent/runlog"
)

type (
	// Store implements runlog.Store in memory.
	Store struct {
		mu sync.Mutex
		// per-run monotonically increasing sequence.
		nextSeq map[string]int64
		// per-run ordered events.
		events map[string][]*runlog.Event
	}
)

// New returns a new in-memory run log store.
func New() *Store {
	return &Store{
		nextSeq: make(map[string]int64),
		events:  make(map[string][]*runlog.Event),
	}
}

// Append implements runlog.Store.
func (s *Store) Append(_ context.Context, e *runlog.Event) error {
	if e == nil {
		return fmt.Errorf("event is required")
	}
	if e.RunID == "" {
		return fmt.Errorf("run_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seq := s.nextSeq[e.RunID] + 1
	s.nextSeq[e.RunID] = seq

	e.ID = strconv.FormatInt(seq, 10)
	ev := *e
	s.events[e.RunID] = append(s.events[e.RunID], &ev)
	return nil
}

// List implements runlog.Store.
func (s *Store) List(_ context.Context, runID string, cursor string, limit int) (runlog.Page, error) {
	if runID == "" {
		return runlog.Page{}, fmt.Errorf("run_id is required")
	}
	if limit <= 0 {
		return runlog.Page{}, fmt.Errorf("limit must be > 0")
	}

	var after int64
	if cursor != "" {
		id, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return runlog.Page{}, fmt.Errorf("invalid cursor %q: %w", cursor, err)
		}
		after = id
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	all := s.events[runID]
	if len(all) == 0 {
		return runlog.Page{}, nil
	}

	start := 0
	if after > 0 {
		// IDs are 1-based sequence numbers, so start at index == after.
		start = int(after)
		if start >= len(all) {
			return runlog.Page{}, nil
		}
	}

	end := start + limit
	if end > len(all) {
		end = len(all)
	}

	events := append([]*runlog.Event(nil), all[start:end]...)
	var next string
	if end < len(all) {
		next = events[len(events)-1].ID
	}

	return runlog.Page{
		Events:     events,
		NextCursor: next,
	}, nil
}
