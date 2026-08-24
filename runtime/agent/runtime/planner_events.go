// Package runtime waits to publish user-visible planner events until the
// planner result has passed every runtime check.
package runtime

import (
	"context"
	"sync"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// runtimePlannerEvents collects events produced during one planner activity.
	//
	// The model response store separately keeps token usage and the exact
	// provider response.
	runtimePlannerEvents struct {
		agentID   agent.Ident
		runID     string
		sessionID string

		mu      sync.Mutex
		pending []hooks.Event
	}
)

// newPlannerEvents creates an event collector for one planner activity. The
// workflow publishes these events only after the activity result is accepted.
func newPlannerEvents(agentID agent.Ident, runID, sessionID string) *runtimePlannerEvents {
	return &runtimePlannerEvents{
		agentID:   agentID,
		runID:     runID,
		sessionID: sessionID,
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

// acceptedRecords freezes all accepted planner events into activity output.
// When budget is non-nil, each encoded record is charged before its payload is
// copied so a large event collection is abandoned as soon as the complete
// activity envelope cannot fit. The workflow assigns stable event keys and
// timestamps after the activity succeeds.
func (e *runtimePlannerEvents) acceptedRecords(
	budget *planActivityOutputBudget,
) ([]*api.PlannerEventRecord, error) {
	e.mu.Lock()
	pending := append([]hooks.Event(nil), e.pending...)
	e.mu.Unlock()
	records := make([]*api.PlannerEventRecord, 0, len(pending))
	for _, event := range pending {
		payload, err := hooks.EncodeRecordPayload(event)
		if err != nil {
			return nil, err
		}
		record := &api.PlannerEventRecord{
			Type:    event.Type(),
			Payload: payload,
		}
		if budget != nil {
			if err := budget.add(record); err != nil {
				return nil, err
			}
		}
		record.Payload = append([]byte(nil), payload...)
		records = append(records, record)
	}
	return records, nil
}

func (e *runtimePlannerEvents) publish(_ context.Context, evt hooks.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = append(e.pending, evt)
}
