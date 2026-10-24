package runtime

// Literal structured history retains its existing exact execution-ID lookup.
// Provider IDs are never interpreted as execution IDs for referenced history;
// that history is read by selected_continuations.go using owned run records.

import (
	"context"
	"errors"
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
// caller-supplied messages. It never searches by a stored provider ID. Saved
// declarations share this reader's checks with the selected run records.
func (r *selectedContinuationReader) loadLiteralContinuationOutputs(ctx context.Context, messages []*model.Message) ([]*planner.ToolOutput, error) {
	input := r.input
	names, err := historicalContinuationToolNames(r.specs)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 && input.RunContext.SessionID == "" {
		registration, ok := r.runtime.agentByID(input.AgentID)
		if !ok || registration.Definition.registryTools == nil {
			// A sessionless request with no paging tools has no saved pages to
			// restore. Its ordinary literal tool exchanges remain model evidence.
			return nil, nil
		}
	}
	toolCallIDs, err := historicalContinuationToolCallIDs(messages)
	if err != nil || len(toolCallIDs) == 0 {
		return nil, err
	}
	if input.RunContext.SessionID == "" {
		return nil, fmt.Errorf("runtime: historical continuation transcript requires a session id")
	}
	literalNames := make(map[string][]string, len(toolCallIDs))
	for _, message := range messages {
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
		page, err := r.runtime.Store.ListSessionRunRecords(
			ctx,
			input.RunContext.SessionID,
			cursor,
			historicalContinuationPageSize,
		)
		if err != nil {
			return nil, fmt.Errorf("runtime: list session run log for historical continuations: %w", err)
		}
		for _, event := range page.Events {
			if event == nil {
				continue
			}
			executor, isAgent := event.Subject.Executor.AsAgent()
			if !isAgent || string(executor) != string(input.AgentID) {
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
		var savedSpecs map[tools.Ident]tools.ToolSpec
		if call := entry.events.scheduled; call != nil {
			callNames := names
			if call.Registry != nil {
				var err error
				savedSpecs, err = r.savedRegistrySpecs(call.Registry)
				if err != nil {
					return nil, err
				}
				callNames, err = historicalContinuationToolNames(savedSpecs)
				if err != nil {
					return nil, err
				}
			}
			_, relevant := callNames[call.ToolName.String()]
			savedPaging := call.ContinuationRootToolCallID != "" ||
				entry.events.result != nil && entry.events.result.Bounds != nil
			if !relevant && !savedPaging {
				continue
			}
			recognized := false
			for _, name := range literalNames[toolCallID] {
				_, canonical := callNames[name]
				recognized = recognized || canonical || name == call.ToolName.String() ||
					IsGeneratedContinuationToolName(tools.Ident(name))
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
		// Failed calls remain in the transcript but cannot supply another page.
		if output.Failure != nil {
			continue
		}
		if err := validateHistoricalContinuationBounds(output, savedSpecs); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

// validateHistoricalContinuationBounds checks saved paging facts against the
// declaration already read for this output. Calls without saved registrations
// and calls whose generated declarations differ in this build have no saved
// tool declarations, so only their result metadata is checked.
// Historical failures are excluded by the readers, and a result without bounds
// supplies no next page. This never authorizes execution of an old tool.
func validateHistoricalContinuationBounds(output *planner.ToolOutput, savedSpecs map[tools.Ident]tools.ToolSpec) error {
	call := ToolCall{Name: output.Name, ToolCallID: output.ToolCallID, Registry: output.Registry}
	if output.Bounds == nil {
		return nil
	}
	if savedSpecs == nil {
		return validateToolBoundsMetadata(call, output.Bounds)
	}
	spec, ok := savedSpecs[output.Name]
	if !ok {
		return fmt.Errorf("saved registry contract contains no tool %q", output.Name)
	}
	if err := validateToolBoundsContract(spec, call, false, output.Bounds); err != nil {
		return fmt.Errorf("runtime: invalid historical continuation metadata (tool_call_id=%s): %w", output.ToolCallID, err)
	}
	return nil
}

// savedHistoricalContinuationSpecs reads the saved call's tool declarations
// once. Callers use them to identify paging tools and check saved result metadata.
// When the generated declarations in this build differ from the saved call,
// it returns no tool declarations.
// The saved metadata can close an old chain, but cannot offer another page
// through declarations that this build no longer has.
func savedHistoricalContinuationSpecs(binding *tools.RegistryBinding, lookup toolSpecLookup) (map[tools.Ident]tools.ToolSpec, error) {
	resolved, err := registrycontract.Read(binding, lookup.registryDeclaration)
	if errors.Is(err, registrycontract.ErrCompiledContractChanged) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resolved.Specs, nil
}

// historicalContinuationToolNames returns the canonical source and continuation
// tool names that can contribute live actions for one agent.
func historicalContinuationToolNames(specs map[tools.Ident]tools.ToolSpec) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	for _, spec := range specs {
		composed, err := isComposedContinuationSpec(spec)
		if err != nil {
			return nil, err
		}
		if !composed && !isDedicatedContinuationSpec(spec) {
			continue
		}
		names[spec.Name.String()] = struct{}{}
		if !composed {
			names[spec.Bounds.Paging.SourceTool.String()] = struct{}{}
		}
	}
	return names, nil
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
