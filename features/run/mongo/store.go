package mongo

import (
	"context"
	"errors"

	mongoc "goa.design/goa-ai/features/run/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/run"
)

// Store implements run.Store by delegating to the Mongo client.
type Store struct {
	client mongoc.Client
}

// NewStore builds a Store using the provided client.
func NewStore(client mongoc.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: client}, nil
}

// Upsert stores the provided run metadata.
func (s *Store) Upsert(ctx context.Context, run run.Record) error {
	return s.client.UpsertRun(ctx, run)
}

// Load retrieves run metadata from storage.
func (s *Store) Load(ctx context.Context, runID string) (run.Record, error) {
	return s.client.LoadRun(ctx, runID)
}
