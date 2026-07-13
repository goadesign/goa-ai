// Package runtime publishes planner presentation events independently from
// provider transcript ownership.
package runtime

import (
	"context"
	"sync"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// runtimePlannerEvents implements planner.PlannerEvents for runtime plan
	// activities.
	//
	// It publishes hook events; the model invocation journal owns usage and
	// provider transcript state.
	runtimePlannerEvents struct {
		rt        *Runtime
		agentID   agent.Ident
		runID     string
		sessionID string
		turnID    string

		mu      sync.Mutex
		hookErr error
	}
)

// newPlannerEvents constructs a planner presentation sink that publishes to
// rt.Bus.
//
// The runtime requires a hook bus. If rt.Bus is nil, this panics to surface an
// invalid runtime configuration early.
func newPlannerEvents(rt *Runtime, agentID agent.Ident, runID, sessionID, turnID string) *runtimePlannerEvents {
	if rt == nil {
		panic("runtime: planner events runtime is nil")
	}
	if rt.Bus == nil {
		panic("runtime: planner events hook bus is nil")
	}
	return &runtimePlannerEvents{
		rt:        rt,
		agentID:   agentID,
		runID:     runID,
		sessionID: sessionID,
		turnID:    turnID,
	}
}

func (e *runtimePlannerEvents) AssistantChunk(ctx context.Context, text string) {
	if text == "" {
		return
	}
	e.publish(ctx, hooks.NewAssistantMessageEvent(e.runID, e.agentID, e.sessionID, text, nil))
}

func (e *runtimePlannerEvents) ToolCallArgsDelta(ctx context.Context, toolCallID string, toolName tools.Ident, delta string) {
	if toolCallID == "" || delta == "" {
		return
	}
	e.publish(ctx, hooks.NewToolCallArgsDeltaEvent(e.runID, e.agentID, e.sessionID, toolCallID, toolName, delta))
}

func (e *runtimePlannerEvents) PlannerThought(ctx context.Context, note string, labels map[string]string) {
	if note == "" {
		return
	}
	e.publish(ctx, hooks.NewPlannerNoteEvent(e.runID, e.agentID, e.sessionID, note, labels))
}

func (e *runtimePlannerEvents) UsageDelta(ctx context.Context, usage model.TokenUsage) {
	e.publish(ctx, hooks.NewUsageEvent(e.runID, e.agentID, e.sessionID, usage))
}

func (e *runtimePlannerEvents) PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart) {
	e.publish(ctx, hooks.NewThinkingBlockEvent(
		e.runID, e.agentID, e.sessionID,
		block.Text, block.Signature, block.Redacted, block.Index, block.Final,
	))
}

func (e *runtimePlannerEvents) hookError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hookErr
}

func (e *runtimePlannerEvents) publish(ctx context.Context, evt hooks.Event) {
	if e.hookError() != nil {
		return
	}
	if err := e.rt.publishHookErr(ctx, evt, e.turnID); err != nil {
		e.mu.Lock()
		if e.hookErr == nil {
			e.hookErr = err
		}
		e.mu.Unlock()
	}
}
