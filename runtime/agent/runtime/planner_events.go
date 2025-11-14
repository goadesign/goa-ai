package runtime

import (
	"context"
	"sync"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// runtimePlannerEvents implements planner.PlannerEvents by publishing to the runtime bus
// and capturing thinking/text into a per-turn provider ledger.
type runtimePlannerEvents struct {
	rt    *Runtime
	agent string
	runID string
	mu    sync.Mutex
	led   *transcript.Ledger
}

func newPlannerEvents(rt *Runtime, agentID, runID string) *runtimePlannerEvents {
	return &runtimePlannerEvents{
		rt:    rt,
		agent: agentID,
		runID: runID,
		led:   transcript.NewLedger(),
	}
}

func (e *runtimePlannerEvents) AssistantChunk(ctx context.Context, text string) {
	if e == nil || text == "" {
		return
	}
	e.mu.Lock()
	if e.led != nil {
		e.led.AppendText(text)
	}
	e.mu.Unlock()
	if e.rt == nil || e.rt.Bus == nil {
		return
	}
	_ = e.rt.Bus.Publish(ctx, hooks.NewAssistantMessageEvent(e.runID, e.agent, text, nil))
}

func (e *runtimePlannerEvents) PlannerThought(ctx context.Context, note string, labels map[string]string) {
	if e == nil || e.rt == nil || e.rt.Bus == nil || note == "" {
		return
	}
	_ = e.rt.Bus.Publish(ctx, hooks.NewPlannerNoteEvent(e.runID, e.agent, note, labels))
}

func (e *runtimePlannerEvents) UsageDelta(ctx context.Context, usage model.TokenUsage) {
	if e == nil || e.rt == nil || e.rt.Bus == nil {
		return
	}
	_ = e.rt.Bus.Publish(ctx, hooks.NewUsageEvent(e.runID, e.agent, usage.InputTokens, usage.OutputTokens, usage.TotalTokens))
}

func (e *runtimePlannerEvents) PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.led != nil {
		e.led.AppendThinking(toTranscriptThinking(block))
	}
	e.mu.Unlock()
	if e.rt == nil || e.rt.Bus == nil {
		return
	}
	_ = e.rt.Bus.Publish(ctx, hooks.NewThinkingBlockEvent(
		e.runID, e.agent,
		block.Text, block.Signature, block.Redacted, block.Index, block.Final,
	))
}

func (e *runtimePlannerEvents) exportTranscript() []*model.Message {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.led == nil {
		return nil
	}
	return e.led.BuildMessages()
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


