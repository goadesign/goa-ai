package runtime

// Literal structured history retains its existing exact execution-ID lookup.
// Provider IDs are never interpreted as execution IDs for referenced history;
// that history is read by selected_continuations.go using owned run records.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
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

// loadLiteralContinuationOutputs preserves the exact execution-ID contract for
// caller-supplied messages. It never searches by a stored provider ID.
func (r *Runtime) loadLiteralContinuationOutputs(
	ctx context.Context,
	input *resolvedPlanActivityInput,
	specs map[tools.Ident]tools.ToolSpec,
) ([]*planner.ToolOutput, error) {
	names := historicalContinuationToolNames(specs)
	if len(names) == 0 {
		registration, ok := r.agentByID(input.AgentID)
		if !ok || registration.Definition.registryTools == nil {
			return nil, nil
		}
	}
	toolCallIDs, err := historicalContinuationToolCallIDs(input.Messages)
	if err != nil || len(toolCallIDs) == 0 {
		return nil, err
	}
	if input.RunContext.SessionID == "" {
		return nil, fmt.Errorf("runtime: historical continuation transcript requires a session id")
	}
	literalNames := make(map[string][]string, len(toolCallIDs))
	for _, message := range input.Messages {
		for _, part := range message.Parts {
			if call, ok := part.(model.ToolUsePart); ok {
				literalNames[call.ID] = append(literalNames[call.ID], call.Name)
			}
		}
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
		if call := entry.events.scheduled; call != nil {
			callNames := names
			if call.Registry != nil {
				resolved, err := registrycontract.Read(call.Registry)
				if err != nil {
					return nil, err
				}
				callNames = historicalContinuationToolNames(resolved.Specs)
			}
			if _, relevant := callNames[call.ToolName.String()]; !relevant {
				continue
			}
			recognized := false
			for _, name := range literalNames[toolCallID] {
				_, canonical := callNames[name]
				recognized = recognized || canonical || IsGeneratedContinuationToolName(tools.Ident(name))
			}
			if !recognized {
				continue
			}
		}
		output, err := toolOutputFromStoredEvents(
			entry.callRunID,
			entry.resultRunID,
			toolCallID,
			&canonicalToolEvents{scheduled: entry.events.scheduled},
			&canonicalToolEvents{result: entry.events.result},
		)
		if err != nil {
			return nil, fmt.Errorf("runtime: hydrate historical continuation output: %w", err)
		}
		if err := r.validateHistoricalContinuationOutput(output, entry.events.result); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

// validateHistoricalContinuationOutput checks paging and failure metadata.
// Saved successful result bytes remain evidence under their original contract.
func (r *Runtime) validateHistoricalContinuationOutput(output *planner.ToolOutput, result *hooks.ToolResultReceivedEvent) error {
	call := ToolCall{Name: output.Name, ToolCallID: output.ToolCallID, Registry: output.Registry}
	var err error
	if output.Failure != nil {
		_, err = validatePersistedToolResult(nil, call, result.ResultJSON, output.ServerData, output.Bounds, output.Failure)
	} else {
		var spec tools.ToolSpec
		var ok bool
		spec, ok, err = lookupCallSpec(call, r.toolSpec)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("runtime: historical continuation references unregistered tool %q", output.Name)
		}
		err = validateToolBoundsContract(spec, call, false, output.Bounds)
	}
	if err != nil {
		return fmt.Errorf("runtime: invalid historical continuation metadata (tool_call_id=%s): %w", output.ToolCallID, err)
	}
	return nil
}

// historicalContinuationToolNames returns the canonical source and continuation
// tool names that can contribute live actions for one agent.
func historicalContinuationToolNames(specs map[tools.Ident]tools.ToolSpec) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range specs {
		if !isDedicatedContinuationSpec(spec) {
			continue
		}
		names[spec.Name.String()] = struct{}{}
		names[spec.Bounds.Paging.SourceTool.String()] = struct{}{}
	}
	return names
}

// historicalContinuationToolCallIDs reads literal execution identities in
// message order. The saved schedule determines whether each call supports
// paging, including calls whose registration is absent from today's catalog.
func historicalContinuationToolCallIDs(messages []*model.Message) ([]string, error) {
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
