package mongo

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/features/session/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/session"
)

// Store implements session.Store by delegating to the Mongo client.
type Store struct {
	client mongo.Client
}

// NewStore builds a Store using the provided client.
func NewStore(client mongo.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: client}, nil
}

// CreateSession implements session.Store.
func (s *Store) CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error) {
	return s.client.CreateSession(ctx, sessionID, createdAt)
}

// LoadSession implements session.Store.
func (s *Store) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	return s.client.LoadSession(ctx, sessionID)
}

// EndSession implements session.Store.
func (s *Store) EndSession(ctx context.Context, sessionID string, endedAt time.Time) (session.Session, error) {
	return s.client.EndSession(ctx, sessionID, endedAt)
}

// UpsertRun implements session.Store.
func (s *Store) UpsertRun(ctx context.Context, run session.RunMeta) error {
	return s.client.UpsertRun(ctx, run)
}

// LoadRun implements session.Store.
func (s *Store) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	return s.client.LoadRun(ctx, runID)
}

// ListRunsBySession implements session.Store.
func (s *Store) ListRunsBySession(ctx context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error) {
	return s.client.ListRunsBySession(ctx, sessionID, statuses)
}
