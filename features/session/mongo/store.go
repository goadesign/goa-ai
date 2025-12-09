package mongo

import (
	"context"
	"errors"

	clientsmongo "goa.design/goa-ai/features/session/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/session"
)

// Store implements session.Store by delegating to the Mongo client.
type Store struct {
	client clientsmongo.Client
}

// NewStore builds a Store using the provided client.
func NewStore(client clientsmongo.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: client}, nil
}

// Upsert stores the provided session metadata.
func (s *Store) Upsert(ctx context.Context, run session.Run) error {
	return s.client.UpsertRun(ctx, run)
}

// Load retrieves run metadata from storage.
func (s *Store) Load(ctx context.Context, runID string) (session.Run, error) {
	return s.client.LoadRun(ctx, runID)
}
