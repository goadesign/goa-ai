package runtime

import (
	"context"
	"sync"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
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

	mu  sync.Mutex
	led *transcript.Ledger

	usage model.TokenUsage
}

// newPlannerEvents constructs a planner event sink that publishes to rt.Bus and
// records a provider transcript.
//
// The runtime requires a hook bus. If rt.Bus is nil, this panics to surface an
// invalid runtime configuration early.
func newPlannerEvents(rt *Runtime, agentID agent.Ident, runID, sessionID string) *runtimePlannerEvents {
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
	if err := e.rt.Bus.Publish(ctx, hooks.NewAssistantMessageEvent(e.runID, e.agentID, e.sessionID, text, nil)); err != nil {
		e.rt.logWarn(ctx, "hook publish failed", err, "event", hooks.AssistantMessage)
	}
}

func (e *runtimePlannerEvents) PlannerThought(ctx context.Context, note string, labels map[string]string) {
	if note == "" {
		return
	}
	if err := e.rt.Bus.Publish(ctx, hooks.NewPlannerNoteEvent(e.runID, e.agentID, e.sessionID, note, labels)); err != nil {
		e.rt.logWarn(ctx, "hook publish failed", err, "event", hooks.PlannerNote)
	}
}

func (e *runtimePlannerEvents) UsageDelta(ctx context.Context, usage model.TokenUsage) {
	e.mu.Lock()
	e.usage = addTokenUsage(e.usage, usage)
	e.mu.Unlock()

	if err := e.rt.Bus.Publish(ctx, hooks.NewUsageEvent(
		e.runID, e.agentID, e.sessionID,
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens,
		usage.CacheReadTokens, usage.CacheWriteTokens,
	)); err != nil {
		e.rt.logWarn(ctx, "hook publish failed", err, "event", hooks.Usage)
	}
}

func (e *runtimePlannerEvents) PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart) {
	e.mu.Lock()
	e.led.AppendThinking(toTranscriptThinking(block))
	e.mu.Unlock()
	if err := e.rt.Bus.Publish(ctx, hooks.NewThinkingBlockEvent(
		e.runID, e.agentID, e.sessionID,
		block.Text, block.Signature, block.Redacted, block.Index, block.Final,
	)); err != nil {
		e.rt.logWarn(ctx, "hook publish failed", err, "event", hooks.ThinkingBlock)
	}
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
