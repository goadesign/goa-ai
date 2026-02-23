// Package mongo wires the prompt.Store interface to the MongoDB prompt client.
package mongo

import (
	"context"
	"errors"

	clientsmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo"
	"goa.design/goa-ai/runtime/agent/prompt"
)

type (
	// Store implements prompt.Store by delegating to the Mongo client.
	Store struct {
		client clientsmongo.Client
	}
)

// NewStore builds a Mongo-backed prompt store using the provided client.
func NewStore(client clientsmongo.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	return &Store{
		client: client,
	}, nil
}

// Resolve resolves the highest-precedence override for promptID within scope.
func (s *Store) Resolve(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) (*prompt.Override, error) {
	return s.client.Resolve(ctx, promptID, scope)
}

// Set persists one override entry.
func (s *Store) Set(ctx context.Context, promptID prompt.Ident, scope prompt.Scope, template string, metadata map[string]string) error {
	return s.client.Set(ctx, promptID, scope, template, metadata)
}

// History returns prompt override history for one prompt ID.
func (s *Store) History(ctx context.Context, promptID prompt.Ident) ([]*prompt.Override, error) {
	return s.client.History(ctx, promptID)
}

// List returns all prompt overrides ordered newest-first.
func (s *Store) List(ctx context.Context) ([]*prompt.Override, error) {
	return s.client.List(ctx)
}
