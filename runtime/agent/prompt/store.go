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
		// History returns override records for promptID ordered newest-first.
		History(ctx context.Context, promptID Ident) ([]*Override, error)
		// List returns all override records ordered newest-first.
		List(ctx context.Context) ([]*Override, error)
	}
)
