package runtime

// historical_continuations.go restores dedicated pagination actions when a new
// run receives a structured transcript containing prior bounded tool calls.
// The transcript supplies only stable tool-call identities; canonical payloads,
// cursors, bounds, and correlation remain owned by the session run log.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

const historicalContinuationPageSize = 256

type historicalToolEvents struct {
	callRunID   string
	resultRunID string
	events      canonicalToolEvents
}

// loadHistoricalContinuationOutputs reconstructs canonical outputs for
// dedicated continuation chains named in the caller-supplied transcript. It
// reads the runtime's own session log rather than trusting cursor state copied
// through model-visible messages.
func (r *Runtime) loadHistoricalContinuationOutputs(
	ctx context.Context,
	input *PlanActivityInput,
) ([]*planner.ToolOutput, error) {
	names := r.historicalContinuationToolNames(input.AgentID)
	toolCallIDs, err := historicalContinuationToolCallIDs(input.Messages, names)
	if err != nil || len(toolCallIDs) == 0 {
		return nil, err
	}
	if input.RunContext.SessionID == "" {
		return nil, fmt.Errorf("runtime: historical continuation transcript requires a session id")
	}
	wanted := make(map[string]struct{}, len(toolCallIDs))
	for _, toolCallID := range toolCallIDs {
		wanted[toolCallID] = struct{}{}
	}
	events := make(map[string]*historicalToolEvents, len(wanted))
	cursor := ""
	for {
		page, err := r.Store.ListSessionRunRecords(
			ctx,
			input.RunContext.SessionID,
			cursor,
			historicalContinuationPageSize,
		)
		if err != nil {
			return nil, fmt.Errorf("runtime: list session run log for historical continuations: %w", err)
		}
		for _, event := range page.Events {
			if event == nil || event.AgentID != input.AgentID {
				continue
			}
			switch event.Type {
			case hooks.ToolCallScheduled:
				scheduled, err := decodeToolCallScheduledRunlogEvent(event)
				if err != nil {
					return nil, err
				}
				if _, ok := wanted[scheduled.ToolCallID]; !ok {
					continue
				}
				entry := historicalEntry(events, scheduled.ToolCallID)
				if entry.events.scheduled != nil {
					return nil, fmt.Errorf(
						"runtime: duplicate historical tool payload for tool_call_id=%s",
						scheduled.ToolCallID,
					)
				}
				entry.callRunID = event.RunID
				entry.events.scheduled = scheduled
			case hooks.ToolResultReceived:
				result, err := decodeToolResultRunlogEvent(event)
				if err != nil {
					return nil, err
				}
				if _, ok := wanted[result.ToolCallID]; !ok {
					continue
				}
				entry := historicalEntry(events, result.ToolCallID)
				if entry.events.result != nil {
					return nil, fmt.Errorf(
						"runtime: duplicate historical tool result for tool_call_id=%s",
						result.ToolCallID,
					)
				}
				entry.resultRunID = event.RunID
				entry.events.result = result
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	outputs := make([]*planner.ToolOutput, 0, len(events))
	for _, toolCallID := range toolCallIDs {
		entry := events[toolCallID]
		if entry == nil {
			continue
		}
		output, err := r.plannerToolOutputFromCanonicalEvents(
			entry.callRunID,
			entry.resultRunID,
			toolCallID,
			&canonicalToolEvents{scheduled: entry.events.scheduled},
			&canonicalToolEvents{result: entry.events.result},
		)
		if err != nil {
			return nil, fmt.Errorf("runtime: hydrate historical continuation output: %w", err)
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

// historicalContinuationToolNames returns the canonical source and continuation
// tool names that can contribute live actions for one agent.
func (r *Runtime) historicalContinuationToolNames(agentID agent.Ident) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range r.ToolSpecsForAgent(agentID) {
		if !isDedicatedContinuationSpec(spec) {
			continue
		}
		names[spec.Name.String()] = struct{}{}
		names[spec.Bounds.Paging.SourceTool.String()] = struct{}{}
	}
	return names
}

// historicalContinuationToolCallIDs selects stable call identities from
// structured assistant tool-use parts. Dynamic continuation actions use a
// provider-safe continue_ name, while source calls retain their canonical name.
func historicalContinuationToolCallIDs(
	messages []*model.Message,
	canonicalNames map[string]struct{},
) ([]string, error) {
	if len(canonicalNames) == 0 {
		return nil, nil
	}
	var ids []string
	seen := make(map[string]struct{})
	for messageIndex, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("runtime: historical transcript message[%d] is nil", messageIndex)
		}
		for partIndex, part := range message.Parts {
			toolUse, ok := part.(model.ToolUsePart)
			if !ok {
				continue
			}
			if _, canonical := canonicalNames[toolUse.Name]; !canonical &&
				!IsGeneratedContinuationToolName(tools.Ident(toolUse.Name)) {
				continue
			}
			if toolUse.ID == "" {
				return nil, fmt.Errorf(
					"runtime: historical transcript message[%d] part[%d] has an empty tool call id",
					messageIndex,
					partIndex,
				)
			}
			if _, duplicate := seen[toolUse.ID]; duplicate {
				continue
			}
			seen[toolUse.ID] = struct{}{}
			ids = append(ids, toolUse.ID)
		}
	}
	return ids, nil
}

// historicalEntry returns the mutable event accumulator for one transcript
// tool-call identity.
func historicalEntry(events map[string]*historicalToolEvents, toolCallID string) *historicalToolEvents {
	entry := events[toolCallID]
	if entry != nil {
		return entry
	}
	entry = &historicalToolEvents{}
	events[toolCallID] = entry
	return entry
}
