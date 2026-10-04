// Package run carries the accepted execution restriction to tool services.
// The runtime and registry providers set this value from saved run metadata;
// ordinary service calls without that metadata allow their usual output.
package run

import "context"

type textOnlyContextKey struct{}

// WithTextOnlyContext supplies trusted execution metadata to a local service or
// provider. Models never supply this context value.
func WithTextOnlyContext(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, textOnlyContextKey{}, enabled)
}

// IsTextOnly reports whether the current tool execution forbids UI output.
// Ordinary service requests without tool execution metadata return false.
func IsTextOnly(ctx context.Context) bool {
	value, ok := ctx.Value(textOnlyContextKey{}).(bool)
	return ok && value
}
