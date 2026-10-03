// Package runtime carries the accepted output restriction to local executors.
// Services receive the same restriction through generated tool-call metadata.
package runtime

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
