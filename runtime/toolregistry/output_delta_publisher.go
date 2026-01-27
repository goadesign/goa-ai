// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
//
// This file defines the context plumbing used by providers to expose an
// output-delta publisher to tool implementations. Tools may use it to emit
// best-effort output fragments while running; the canonical tool result is
// still delivered via ToolResultMessage.
package toolregistry

import "context"

type (
	// OutputDeltaPublisher emits best-effort tool output deltas for a single tool
	// execution. Providers inject an instance into the tool call context so tool
	// implementations can stream partial output while running.
	OutputDeltaPublisher interface {
		PublishToolOutputDelta(ctx context.Context, stream string, delta string) error
	}

	outputDeltaPublisherKey struct{}
)

// WithOutputDeltaPublisher returns a context that carries pub.
func WithOutputDeltaPublisher(ctx context.Context, pub OutputDeltaPublisher) context.Context {
	return context.WithValue(ctx, outputDeltaPublisherKey{}, pub)
}

// OutputDeltaPublisherFromContext returns the output-delta publisher carried by
// ctx, if any.
func OutputDeltaPublisherFromContext(ctx context.Context) (OutputDeltaPublisher, bool) {
	pub, ok := ctx.Value(outputDeltaPublisherKey{}).(OutputDeltaPublisher)
	return pub, ok
}

