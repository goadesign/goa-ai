// Package executor provides registry-backed tool execution. It adapts the
// shared registry call implementation to the runtime's executor interface.
package executor

import (
	"context"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/internal/registrycall"
	"goa.design/goa-ai/runtime/agent/runtime"
	aistream "goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// Client admits registry calls and resumes registry-directed retries.
	Client = registrycall.Client

	// SpecLookup supplies the exact contracts used to decode returned results.
	SpecLookup = registrycall.SpecLookup

	// Executor sends calls to one registered toolset and returns runtime results.
	Executor struct {
		calls *registrycall.Executor
	}

	// Option configures executor logging, tracing, or output streaming.
	Option = registrycall.Option
)

// WithStreamSink publishes tool output fragments when the profile enables them.
// It panics for a nil sink or an empty profile.
func WithStreamSink(sink aistream.Sink, profile aistream.StreamProfile) Option {
	return registrycall.WithStreamSink(sink, profile)
}

// WithLogger configures the executor logger.
func WithLogger(logger telemetry.Logger) Option {
	return registrycall.WithLogger(logger)
}

// WithTracer configures the executor tracer.
func WithTracer(tracer telemetry.Tracer) Option {
	return registrycall.WithTracer(tracer)
}

// New builds an executor for the named registration and its tool contracts.
func New(client Client, pulse pulsec.Client, toolset string, specs SpecLookup, opts ...Option) (*Executor, error) {
	calls, err := registrycall.New(client, pulse, toolset, specs, opts...)
	if err != nil {
		return nil, err
	}
	return &Executor{calls: calls}, nil
}

// Execute sends one tool call to the registry and waits for its final result.
func (e *Executor) Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
	var registryMeta *toolregistry.ToolCallMeta
	if meta != nil {
		registryMeta = &toolregistry.ToolCallMeta{
			RunID:            meta.RunID,
			SessionID:        meta.SessionID,
			TurnID:           meta.TurnID,
			ToolCallID:       meta.ToolCallID,
			ParentToolCallID: meta.ParentToolCallID,
			Labels:           meta.Labels,
		}
	}
	result, err := e.calls.Execute(ctx, registryMeta, call)
	if err != nil {
		return nil, err
	}
	return runtime.Executed(result), nil
}
