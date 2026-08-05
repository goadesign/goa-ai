// Package toolregistry defines the canonical wire protocol and context values
// shared by registry providers and method-backed tool implementations.
package toolregistry

import "context"

type toolUseIDContextKey struct{}

// WithToolUseID returns a context carrying the canonical run-scoped tool-call
// identity. Provider runtimes call it before dispatching a tool implementation.
func WithToolUseID(ctx context.Context, toolUseID string) context.Context {
	return context.WithValue(ctx, toolUseIDContextKey{}, toolUseID)
}

// ToolUseIDFromContext returns the canonical run-scoped tool-call identity
// injected by the provider runtime.
func ToolUseIDFromContext(ctx context.Context) (string, bool) {
	toolUseID, ok := ctx.Value(toolUseIDContextKey{}).(string)
	return toolUseID, ok
}
