// Package prompt defines the storage contract for persisting and resolving
// scoped prompt template overrides.
package prompt

import "context"

type (
	// Store persists prompt overrides and resolves the active override for a
	// prompt/scope pair.
	//
	// Resolve must return (nil, nil) when no override matches. Callers treat nil
	// as "use baseline spec".
	Store interface {
		// Resolve returns the highest-precedence override for promptID within scope.
		// Returns (nil, nil) when no override exists.
		Resolve(ctx context.Context, promptID Ident, scope Scope) (*Override, error)
		// Set persists a new override record.
		Set(ctx context.Context, promptID Ident, scope Scope, template string, metadata map[string]string) error
		// Reset destructively removes stored override records matching the exact
		// (promptID, scope) pair. Subsequent resolutions for this promptID and scope
		// fall back to the next applicable scoped override or baseline spec.
		// Matching on scope is exact: SessionID must match exactly, and the labels map
		// must contain identical key/value entries with no extra or missing keys.
		Reset(ctx context.Context, promptID Ident, scope Scope) error
		// History returns all stored override records for promptID ordered newest-first.
		History(ctx context.Context, promptID Ident) ([]*Override, error)
		// List returns all override records ordered newest-first.
		List(ctx context.Context) ([]*Override, error)
	}
)
