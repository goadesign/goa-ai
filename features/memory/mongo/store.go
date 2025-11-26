// Package mongo wires the memory.Store interface to the MongoDB client.
package mongo

import (
	"context"
	"errors"

	clientsmongo "goa.design/goa-ai/features/memory/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/memory"
)

// Options configures the Store wrapper.
type Options struct {
	Client clientsmongo.Client
}

// Store implements memory.Store by delegating to the Mongo client.
type Store struct {
	client clientsmongo.Client
}

// NewStore builds a Mongo-backed memory store using the provided client.
func NewStore(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{client: opts.Client}, nil
}

// NewStoreFromMongo is a helper that instantiates the underlying client using the given options.
func NewStoreFromMongo(opts clientsmongo.Options) (*Store, error) {
	client, err := clientsmongo.New(opts)
	if err != nil {
		return nil, err
	}
	return NewStore(Options{Client: client})
}

// LoadRun loads the snapshot for the given agent/run.
func (s *Store) LoadRun(ctx context.Context, agentID, runID string) (memory.Snapshot, error) {
	return s.client.LoadRun(ctx, agentID, runID)
}

// AppendEvents appends the provided events to the run history.
func (s *Store) AppendEvents(ctx context.Context, agentID, runID string, events ...memory.Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.client.AppendEvents(ctx, agentID, runID, events)
}
