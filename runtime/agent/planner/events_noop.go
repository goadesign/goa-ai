package planner

import (
	"context"

	"goa.design/goa-ai/runtime/agent/model"
)

// NoopEvents returns a PlannerEvents implementation that discards all events.
// Useful for tests or planners that aggregate stream content without emitting
// intermediate updates to the runtime.
func NoopEvents() PlannerEvents {
	return noopEvents{}
}

type noopEvents struct{}

func (noopEvents) AssistantChunk(ctx context.Context, text string)                           {}
func (noopEvents) PlannerThought(ctx context.Context, note string, labels map[string]string) {}
func (noopEvents) UsageDelta(ctx context.Context, usage model.TokenUsage)                    {}
