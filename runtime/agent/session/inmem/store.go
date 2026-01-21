// Package inmem provides an in-memory implementation of session.Store.
//
// It is intended for tests and local development. Production deployments should
// use a durable implementation (for example features/session/mongo).
package inmem

import (
	"context"
	"errors"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/session"
)

type (
	// Store is an in-memory implementation of session.Store.
	// It is safe for concurrent use.
	Store struct {
		mu       sync.RWMutex
		sessions map[string]session.Session
		runs     map[string]session.RunMeta
	}
)

// New returns an empty Store.
func New() *Store {
	return &Store{
		sessions: make(map[string]session.Session),
		runs:     make(map[string]session.RunMeta),
	}
}

// CreateSession implements session.Store.
func (s *Store) CreateSession(_ context.Context, sessionID string, createdAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if createdAt.IsZero() {
		return session.Session{}, errors.New("created_at is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.sessions[sessionID]
	if ok {
		if existing.Status == session.StatusEnded {
			return session.Session{}, session.ErrSessionEnded
		}
		return cloneSession(existing), nil
	}

	out := session.Session{
		ID:        sessionID,
		Status:    session.StatusActive,
		CreatedAt: createdAt.UTC(),
		EndedAt:   nil,
	}
	s.sessions[sessionID] = out
	return cloneSession(out), nil
}

// LoadSession implements session.Store.
func (s *Store) LoadSession(_ context.Context, sessionID string) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	existing, ok := s.sessions[sessionID]
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	return cloneSession(existing), nil
}

// EndSession implements session.Store.
func (s *Store) EndSession(_ context.Context, sessionID string, endedAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if endedAt.IsZero() {
		return session.Session{}, errors.New("ended_at is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.sessions[sessionID]
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if existing.Status == session.StatusEnded {
		return cloneSession(existing), nil
	}
	at := endedAt.UTC()
	existing.Status = session.StatusEnded
	existing.EndedAt = &at
	s.sessions[sessionID] = existing
	return cloneSession(existing), nil
}

// UpsertRun implements session.Store.
func (s *Store) UpsertRun(_ context.Context, run session.RunMeta) error {
	if run.RunID == "" {
		return errors.New("run id is required")
	}
	if run.AgentID == "" {
		return errors.New("agent id is required")
	}
	if run.SessionID == "" {
		return errors.New("session id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	existing, ok := s.runs[run.RunID]
	if ok && !existing.StartedAt.IsZero() {
		if run.StartedAt.IsZero() {
			run.StartedAt = existing.StartedAt
		} else if !run.StartedAt.Equal(existing.StartedAt) {
			return errors.New("started_at is immutable")
		}
	} else if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.UpdatedAt = now

	s.runs[run.RunID] = cloneRunMeta(run)
	return nil
}

// LoadRun implements session.Store.
func (s *Store) LoadRun(_ context.Context, runID string) (session.RunMeta, error) {
	if runID == "" {
		return session.RunMeta{}, errors.New("run id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return session.RunMeta{}, session.ErrRunNotFound
	}
	return cloneRunMeta(run), nil
}

// ListRunsBySession implements session.Store.
func (s *Store) ListRunsBySession(_ context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error) {
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	var allowed map[session.RunStatus]struct{}
	if len(statuses) > 0 {
		allowed = make(map[session.RunStatus]struct{}, len(statuses))
		for _, st := range statuses {
			allowed[st] = struct{}{}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]session.RunMeta, 0, len(s.runs))
	for _, run := range s.runs {
		if run.SessionID != sessionID {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[run.Status]; !ok {
				continue
			}
		}
		out = append(out, cloneRunMeta(run))
	}
	return out, nil
}

func cloneSession(in session.Session) session.Session {
	out := in
	if in.EndedAt != nil {
		at := *in.EndedAt
		out.EndedAt = &at
	}
	return out
}

func cloneRunMeta(in session.RunMeta) session.RunMeta {
	out := in
	if len(in.Labels) > 0 {
		out.Labels = make(map[string]string, len(in.Labels))
		for k, v := range in.Labels {
			out.Labels[k] = v
		}
	}
	if len(in.Metadata) > 0 {
		out.Metadata = make(map[string]any, len(in.Metadata))
		for k, v := range in.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
