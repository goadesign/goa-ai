package runtime

import (
	"context"
	"sync"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// runtimePlannerEvents implements planner.PlannerEvents for runtime plan
// activities.
//
// It serves two purposes:
//   - Publish hook events (streaming / persistence / observability) via the runtime bus.
//   - Capture thinking/text into a per-turn transcript ledger and aggregate token usage
//     for deterministic workflow consumption.
//
// The planner (or model wrapper) may emit events while streaming; methods therefore
// take a mutex to allow concurrent calls without corrupting the ledger or usage totals.
type runtimePlannerEvents struct {
	rt        *Runtime
	agentID   agent.Ident
	runID     string
	sessionID string
	turnID    string

	mu  sync.Mutex
	led *transcript.Ledger

	usage model.TokenUsage

	hookErr error
}

// newPlannerEvents constructs a planner event sink that publishes to rt.Bus and
// records a provider transcript.
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
		led:       transcript.NewLedger(),
	}
}

func (e *runtimePlannerEvents) AssistantChunk(ctx context.Context, text string) {
	if text == "" {
		return
	}
	e.mu.Lock()
	e.led.AppendText(text)
	e.mu.Unlock()
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
	e.mu.Lock()
	e.usage = addTokenUsage(e.usage, usage)
	e.mu.Unlock()

	e.publish(ctx, hooks.NewUsageEvent(e.runID, e.agentID, e.sessionID, usage))
}

func (e *runtimePlannerEvents) PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart) {
	e.mu.Lock()
	e.led.AppendThinking(toTranscriptThinking(block))
	e.mu.Unlock()
	e.publish(ctx, hooks.NewThinkingBlockEvent(
		e.runID, e.agentID, e.sessionID,
		block.Text, block.Signature, block.Redacted, block.Index, block.Final,
	))
}

func (e *runtimePlannerEvents) exportTranscript() []*model.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.led.BuildMessages()
}

func (e *runtimePlannerEvents) exportUsage() model.TokenUsage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usage
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

func toTranscriptThinking(block model.ThinkingPart) transcript.ThinkingPart {
	cp := transcript.ThinkingPart{
		Text:      block.Text,
		Signature: block.Signature,
		Index:     block.Index,
		Final:     block.Final,
	}
	if len(block.Redacted) > 0 {
		cp.Redacted = append([]byte(nil), block.Redacted...)
	}
	return cp
}
