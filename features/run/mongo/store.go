package mongo

import (
	"context"
	"errors"

	"goa.design/goa-ai/agents/runtime/run"
	clientsmongo "goa.design/goa-ai/features/run/mongo/clients/mongo"
)

// Options configures the Mongo-backed session store.
type Options struct {
	Client clientsmongo.Client
}

// Store implements run.Store by delegating to the Mongo client.
type Store struct {
	client clientsmongo.Client
}

// NewStore builds a Store using the provided client.
func NewStore(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: opts.Client}, nil
}

// NewStoreFromMongo instantiates the Store by constructing the underlying client.
func NewStoreFromMongo(opts clientsmongo.Options) (*Store, error) {
	client, err := clientsmongo.New(opts)
	if err != nil {
		return nil, err
	}
	return NewStore(Options{Client: client})
}

// Upsert stores the provided run metadata.
func (s *Store) Upsert(ctx context.Context, run run.Record) error {
	return s.client.UpsertRun(ctx, run)
}

// Load retrieves run metadata from storage.
func (s *Store) Load(ctx context.Context, runID string) (run.Record, error) {
	return s.client.LoadRun(ctx, runID)
}
