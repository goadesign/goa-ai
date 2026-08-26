// Package inmem provides an in-memory implementation of runlog.Store.
//
// The in-memory store is intended for tests and local development. It is not
// durable and should not be used in production.
package inmem

import (
	"bytes"
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
		// per-run stable event identities.
		eventsByKey map[string]map[string]*runlog.Event
		// session IDs fixed by the first event for each run.
		runSessions map[string]string
		// per-session append order across runs.
		sessionEvents map[string][]*runlog.Event
	}
)

// New returns a new in-memory run log store.
func New() *Store {
	return &Store{
		nextSeq:       make(map[string]int64),
		events:        make(map[string][]*runlog.Event),
		eventsByKey:   make(map[string]map[string]*runlog.Event),
		runSessions:   make(map[string]string),
		sessionEvents: make(map[string][]*runlog.Event),
	}
}

// Append implements runlog.Store.
func (s *Store) Append(_ context.Context, e *runlog.Event) (runlog.AppendResult, error) {
	if e == nil {
		return runlog.AppendResult{}, fmt.Errorf("event is required")
	}
	if e.RunID == "" {
		return runlog.AppendResult{}, fmt.Errorf("run_id is required")
	}
	if e.EventKey == "" {
		return runlog.AppendResult{}, fmt.Errorf("event_key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.runSessions[e.RunID]; ok && existing != e.SessionID {
		return runlog.AppendResult{}, fmt.Errorf(
			"run_id %q belongs to session %q, cannot append an event for session %q",
			e.RunID,
			existing,
			e.SessionID,
		)
	}
	s.runSessions[e.RunID] = e.SessionID

	byKey := s.eventsByKey[e.RunID]
	if byKey == nil {
		byKey = make(map[string]*runlog.Event)
		s.eventsByKey[e.RunID] = byKey
	}
	if existing, ok := byKey[e.EventKey]; ok {
		if !sameEventBody(existing, e) {
			return runlog.AppendResult{}, fmt.Errorf("event_key %q conflicts with existing event body", e.EventKey)
		}
		e.ID = existing.ID
		return runlog.AppendResult{ID: existing.ID, Inserted: false}, nil
	}

	seq := s.nextSeq[e.RunID] + 1
	s.nextSeq[e.RunID] = seq

	e.ID = strconv.FormatInt(seq, 10)
	ev := *e
	s.events[e.RunID] = append(s.events[e.RunID], &ev)
	byKey[e.EventKey] = &ev
	if e.SessionID != "" {
		s.sessionEvents[e.SessionID] = append(s.sessionEvents[e.SessionID], &ev)
	}
	return runlog.AppendResult{ID: ev.ID, Inserted: true}, nil
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

// ListSession implements runlog.SessionReader.
func (s *Store) ListSession(_ context.Context, sessionID string, cursor string, limit int) (runlog.Page, error) {
	if sessionID == "" {
		return runlog.Page{}, fmt.Errorf("session_id is required")
	}
	if limit <= 0 {
		return runlog.Page{}, fmt.Errorf("limit must be > 0")
	}

	var start int
	if cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil {
			return runlog.Page{}, fmt.Errorf("invalid cursor %q: %w", cursor, err)
		}
		start = parsed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	all := s.sessionEvents[sessionID]
	if start >= len(all) {
		return runlog.Page{}, nil
	}
	end := min(start+limit, len(all))
	events := append([]*runlog.Event(nil), all[start:end]...)
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return runlog.Page{
		Events:     events,
		NextCursor: next,
	}, nil
}

// sameEventBody reports whether candidate represents the same immutable logical
// event as existing. It excludes the store-assigned ID and retry-attempt
// timestamp; the first successful append owns both values.
func sameEventBody(existing *runlog.Event, candidate *runlog.Event) bool {
	if existing == nil || candidate == nil {
		return false
	}
	return existing.EventKey == candidate.EventKey &&
		existing.RunID == candidate.RunID &&
		existing.AgentID == candidate.AgentID &&
		existing.SessionID == candidate.SessionID &&
		existing.TurnID == candidate.TurnID &&
		existing.Type == candidate.Type &&
		bytes.Equal(existing.Payload, candidate.Payload)
}
