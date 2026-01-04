// Package mongo wires the runlog.Store interface to the MongoDB client.
package mongo

import (
	"context"
	"errors"

	clientsmongo "goa.design/goa-ai/features/runlog/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/runlog"
)

// Store implements runlog.Store by delegating to the Mongo client.
type Store struct {
	client clientsmongo.Client
}

// NewStore builds a Mongo-backed run log store using the provided client.
func NewStore(client clientsmongo.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: client}, nil
}

// Append implements runlog.Store.
func (s *Store) Append(ctx context.Context, e *runlog.Event) error {
	return s.client.Append(ctx, e)
}

// List implements runlog.Store.
func (s *Store) List(ctx context.Context, runID string, cursor string, limit int) (runlog.Page, error) {
	return s.client.List(ctx, runID, cursor, limit)
}
