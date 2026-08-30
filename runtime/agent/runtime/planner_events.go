// Package runtime collects durable planner annotations and usage while model
// text and thinking travel directly from inference to the live session stream.
package runtime

import (
	"context"
	"sync"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
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
		prompts   *prompt.RenderRecorder

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
		prompts:   prompt.NewRenderRecorder(),
	}
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
	renders := e.prompts.Events()
	events := make([]hooks.Event, 0, len(renders)+len(pending))
	for _, rendered := range renders {
		events = append(events, hooks.NewPromptRenderedEvent(
			e.runID,
			e.agentID,
			e.sessionID,
			rendered.PromptID,
			rendered.Version,
			rendered.Scope,
		))
	}
	events = append(events, pending...)
	records := make([]*api.PlannerEventRecord, 0, len(events))
	for _, event := range events {
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
