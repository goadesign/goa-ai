package runtime

// Saved references select execution records, not provider call IDs. Activities
// use this reader to recover pagination roots and pages through exact run ends.
// A continuation seed also selects the completed outputs retained by its
// predecessor checkpoint, including results after that transcript's end.

import (
	"context"
	"fmt"
	"slices"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	continuationHistoryFrame struct {
		ref  storage.HistoryPrefix
		meta session.RunMeta
		seed storage.RunSeed
	}

	continuationOutputKey struct {
		callRun, resultRun, call string
	}

	selectedContinuationReader struct {
		runtime *Runtime
		input   *resolvedPlanActivityInput
		specs   map[tools.Ident]tools.ToolSpec
		runs    map[string]struct{}
		events  map[string]map[string]*canonicalToolEvents
		seen    map[continuationOutputKey]struct{}
		outputs []*planner.ToolOutput
	}
)

// loadHistoricalContinuationOutputs reads the already-validated source chain
// oldest first. Literal imports retain their separate exact execution-ID lookup.
func (r *Runtime) loadHistoricalContinuationOutputs(ctx context.Context, input *resolvedPlanActivityInput, specs map[tools.Ident]tools.ToolSpec) ([]*planner.ToolOutput, error) {
	reader := selectedContinuationReader{
		runtime: r, input: input, specs: specs,
		runs: make(map[string]struct{}), events: make(map[string]map[string]*canonicalToolEvents),
		seen: make(map[continuationOutputKey]struct{}),
	}
	var frames []continuationHistoryFrame
	ref := storage.HistoryPrefix{RunID: input.RunID, EndID: input.HistoryEndID}
	for {
		if _, cycle := reader.runs[ref.RunID]; cycle {
			return nil, storage.NewContractError(fmt.Errorf("continuation history cycle at run %q", ref.RunID))
		}
		meta, err := r.Store.LoadRun(ctx, ref.RunID)
		if err != nil {
			return nil, err
		}
		if meta.RunID != ref.RunID || meta.AgentID != string(input.AgentID) || meta.SessionID != input.RunContext.SessionID {
			return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
		}
		reader.runs[ref.RunID] = struct{}{}
		frame := continuationHistoryFrame{ref: ref, meta: meta}
		if meta.SeedEndID != "" {
			frame.seed, err = r.Store.LoadRunSeed(ctx, ref.RunID, meta.SeedEndID)
			if err != nil {
				return nil, err
			}
			if frame.seed.EndID != meta.SeedEndID || frame.seed.Declaration.RunID != meta.RunID ||
				frame.seed.Declaration.AgentID != meta.AgentID || frame.seed.Declaration.SessionID != meta.SessionID {
				return nil, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
			}
		}
		frames = append(frames, frame)
		if frame.seed.Source == nil {
			break
		}
		ref = *frame.seed.Source
	}
	// A literal run has no declared source. Preserve its established transcript
	// execution-ID lookup, including callers that appended literal run records.
	// Referenced runs instead select only their explicit imported seed messages.
	literalStart := len(frames) == 1 && len(input.ToolOutputs) == 0
	if literalStart {
		if err := reader.readLiterals(ctx, input.Messages); err != nil {
			return nil, err
		}
	}
	for _, frame := range slices.Backward(frames) {
		if frame.seed.Declaration.Kind == storage.SeedContinuation {
			if err := reader.readCheckpoint(ctx, *frame.seed.Source); err != nil {
				return nil, err
			}
		}
		if !literalStart {
			literals, err := reader.readSeedLiterals(ctx, frame)
			if err != nil {
				return nil, err
			}
			if err := reader.readLiterals(ctx, literals); err != nil {
				return nil, err
			}
		}
		if err := reader.readRun(ctx, frame); err != nil {
			return nil, err
		}
	}
	return reader.outputs, nil
}

// continuationActionsForHistory combines saved and current work once per exact
// output identity. Active outputs stay separate from planner failure guidance
// and counters; only the continuation catalog consumes this combined history.
func (r *Runtime) continuationActionsForHistory(ctx context.Context, input *resolvedPlanActivityInput, specs map[tools.Ident]tools.ToolSpec, current []*planner.ToolOutput) ([]continuationAction, error) {
	outputs, err := r.loadHistoricalContinuationOutputs(ctx, input, specs)
	if err != nil {
		return nil, err
	}
	seen := make(map[continuationOutputKey]struct{}, len(outputs))
	for _, output := range outputs {
		seen[continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}] = struct{}{}
	}
	for _, output := range current {
		key := continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}
		if _, exists := seen[key]; !exists {
			outputs = append(outputs, output)
			seen[key] = struct{}{}
		}
	}
	actions, err := r.availableContinuationActions(input.AgentID, outputs, input.RunContext.TextOnly)
	if err != nil {
		return nil, err
	}
	policy := compileToolPolicy(input.Policy)
	return slices.DeleteFunc(actions, func(action continuationAction) bool {
		return !policy.allowsTool(action.spec.Name, toolPolicyFactsFromSpec(action.spec))
	}), nil
}

// readRun stops at the exact selected event, even if its final page also
// contains later records. Only completed results can add continuation state.
func (r *selectedContinuationReader) readRun(ctx context.Context, frame continuationHistoryFrame) error {
	seen := make(map[string]struct{})
	cursor := ""
	for {
		page, err := r.runtime.Store.ListRunRecords(ctx, frame.ref.RunID, cursor, historicalContinuationPageSize)
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if err := r.validateEvent(event, frame.ref.RunID, seen); err != nil {
				return err
			}
			switch event.Type {
			case hooks.ToolCallScheduled:
				call, err := decodeToolCallScheduledRunlogEvent(event)
				if err != nil {
					return err
				}
				entry := r.entry(event.RunID, call.ToolCallID)
				if entry.scheduled != nil {
					return fmt.Errorf("duplicate selected schedule for %q", call.ToolCallID)
				}
				entry.scheduled = call
			case hooks.ToolResultReceived:
				result, err := decodeToolResultRunlogEvent(event)
				if err != nil {
					return err
				}
				entry := r.entry(event.RunID, result.ToolCallID)
				if entry.result != nil {
					return fmt.Errorf("duplicate selected result for %q", result.ToolCallID)
				}
				if err := hooks.ValidateToolResultPlacement(event.RunID, entry.scheduled, result); err != nil {
					return err
				}
				entry.result = result
				if err := r.addReference(ctx, &api.ToolOutputRef{CallRunID: result.CallRunID, ResultRunID: event.RunID, ToolCallID: result.ToolCallID}); err != nil {
					return err
				}
			case transcript.RunLogMessagesSeeded, transcript.RunLogMessagesAppended:
				// Runs without published seeds use the original literal history
				// contract. Seeded runs select execution events instead.
				if frame.meta.SeedEndID == "" && frame.ref.RunID != r.input.RunID {
					messages, err := transcript.DecodeRunLogDelta(event.Payload)
					if err != nil {
						return err
					}
					if err := r.readLiterals(ctx, messages); err != nil {
						return err
					}
				}
			}
			if event.ID == frame.ref.EndID {
				return nil
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor || len(page.Events) == 0 {
			return storage.NewContractError(fmt.Errorf("continuation history did not reach run %q end %q", frame.ref.RunID, frame.ref.EndID))
		}
		cursor = page.NextCursor
	}
}

// readCheckpoint adds only completed output references owned by the exact
// suspended predecessor. It never extends that predecessor's selected end.
func (r *selectedContinuationReader) readCheckpoint(ctx context.Context, ref storage.HistoryPrefix) error {
	suspension, err := r.runtime.LoadRunSuspension(ctx, ref.RunID)
	if err != nil {
		return err
	}
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return err
	}
	if checkpoint.HistoryEndID != ref.EndID || checkpoint.AgentID != string(r.input.AgentID) ||
		checkpoint.SessionID != r.input.RunContext.SessionID || checkpoint.PreviousRunID != ref.RunID {
		return storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	for _, output := range checkpoint.State.ToolOutputs {
		if err := r.addReference(ctx, &api.ToolOutputRef{CallRunID: output.CallRunID, ResultRunID: output.ResultRunID, ToolCallID: output.ToolCallID}); err != nil {
			return err
		}
	}
	for _, record := range checkpoint.Batch.Records {
		if record.ResultPublished && record.ChildSuspension == nil {
			if err := r.addReference(ctx, &api.ToolOutputRef{CallRunID: record.CallRunID, ResultRunID: record.ResultRunID, ToolCallID: record.Call.ToolCallID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// addReference loads only the named schedule and result. The selected result
// may name a schedule beyond an ancestor's transcript end; that exact reference
// grants no access to other later results.
func (r *selectedContinuationReader) addReference(ctx context.Context, ref *api.ToolOutputRef) error {
	key := continuationOutputKey{ref.CallRunID, ref.ResultRunID, ref.ToolCallID}
	if _, exists := r.seen[key]; exists {
		return nil
	}
	for _, runID := range []string{ref.CallRunID, ref.ResultRunID} {
		if _, selected := r.runs[runID]; !selected || ref.ToolCallID == "" {
			return storage.NewContractError(fmt.Errorf("continuation output references unselected run %q", runID))
		}
	}
	if err := r.readNamedEvent(ctx, ref.CallRunID, ref.ToolCallID, hooks.ToolCallScheduled); err != nil {
		return err
	}
	call := r.entry(ref.CallRunID, ref.ToolCallID).scheduled
	specs := r.specs
	if call.Registry != nil {
		resolved, err := registrycontract.Read(call.Registry)
		if err != nil {
			return err
		}
		specs = resolved.Specs
	}
	names := historicalContinuationToolNames(specs)
	if _, relevant := names[call.ToolName.String()]; !relevant {
		return nil
	}
	if err := r.readNamedEvent(ctx, ref.ResultRunID, ref.ToolCallID, hooks.ToolResultReceived); err != nil {
		return err
	}
	result := r.entry(ref.ResultRunID, ref.ToolCallID)
	output, err := toolOutputFromStoredEvents(ref.CallRunID, ref.ResultRunID, ref.ToolCallID, r.entry(ref.CallRunID, ref.ToolCallID), result)
	if err != nil {
		return err
	}
	if err := r.runtime.validateHistoricalContinuationOutput(output, result.result); err != nil {
		return err
	}
	r.outputs = append(r.outputs, output)
	r.seen[key] = struct{}{}
	return nil
}

// readNamedEvent supplements a selected result or checkpoint reference with
// its exact missing event. It stops immediately on that event and never admits
// other results encountered during the lookup.
func (r *selectedContinuationReader) readNamedEvent(ctx context.Context, runID, callID string, kind runlog.Type) error {
	entry := r.entry(runID, callID)
	if kind == hooks.ToolCallScheduled && entry.scheduled != nil || kind == hooks.ToolResultReceived && entry.result != nil {
		return nil
	}
	cursor := ""
	seen := make(map[string]struct{})
	for {
		page, err := r.runtime.Store.ListRunRecords(ctx, runID, cursor, historicalContinuationPageSize)
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if err := r.validateEvent(event, runID, seen); err != nil {
				return err
			}
			if event.Type != kind {
				continue
			}
			if kind == hooks.ToolCallScheduled {
				call, err := decodeToolCallScheduledRunlogEvent(event)
				if err != nil {
					return err
				}
				if call.ToolCallID == callID {
					entry.scheduled = call
					return nil
				}
			} else {
				result, err := decodeToolResultRunlogEvent(event)
				if err != nil {
					return err
				}
				if result.ToolCallID == callID {
					entry.result = result
					return nil
				}
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor || len(page.Events) == 0 {
			return fmt.Errorf("missing selected continuation event %s in run %q for call %q", kind, runID, callID)
		}
		cursor = page.NextCursor
	}
}

// readSeedLiterals decodes only explicit imported messages. The transcript
// reader already validates seed ordering and references before this resolver.
func (r *selectedContinuationReader) readSeedLiterals(ctx context.Context, frame continuationHistoryFrame) ([]*model.Message, error) {
	if frame.meta.SeedEndID == "" {
		return nil, nil
	}
	var messages []*model.Message
	var decoder transcript.LiteralDecoder
	cursor := ""
	for {
		page, err := r.runtime.Store.ListRunSeedRecords(ctx, frame.ref.RunID, frame.meta.SeedEndID, cursor, historicalContinuationPageSize)
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			var delta []*model.Message
			if record.LiteralPart != nil {
				delta, err = decoder.Append(ctx, *record.LiteralPart)
			} else {
				if err := decoder.Finish(); err != nil {
					return nil, err
				}
				if len(record.Messages) > 0 {
					delta, err = transcript.DecodeRunLogDelta(record.Messages)
				}
			}
			if err != nil {
				return nil, err
			}
			messages = append(messages, delta...)
		}
		if page.NextCursor == "" {
			return messages, decoder.Finish()
		}
		if page.NextCursor == cursor || len(page.Records) == 0 {
			return nil, storage.NewContractError(fmt.Errorf("continuation seed made no progress"))
		}
		cursor = page.NextCursor
	}
}

func (r *selectedContinuationReader) readLiterals(ctx context.Context, messages []*model.Message) error {
	literal := *r.input
	literal.Messages = messages
	outputs, err := r.runtime.loadLiteralContinuationOutputs(ctx, &literal, r.specs)
	if err != nil {
		return err
	}
	for _, output := range outputs {
		// Selected run records already own their output order and end bounds.
		// A literal ID cannot move a later page ahead of its source or import a
		// result outside those bounds.
		if _, selected := r.runs[output.ResultRunID]; selected {
			continue
		}
		key := continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}
		if _, exists := r.seen[key]; !exists {
			r.outputs = append(r.outputs, output)
			r.seen[key] = struct{}{}
		}
	}
	return nil
}

func (r *selectedContinuationReader) entry(runID, callID string) *canonicalToolEvents {
	events := r.events[runID]
	if events == nil {
		events = make(map[string]*canonicalToolEvents)
		r.events[runID] = events
	}
	return canonicalEntry(events, callID)
}

// validateEvent rejects foreign, absent or repeated stored records before any
// of their payloads can affect the selected continuation history.
func (r *selectedContinuationReader) validateEvent(event *runlog.Event, runID string, seen map[string]struct{}) error {
	if event == nil || event.ID == "" || event.RunID != runID ||
		event.AgentID != r.input.AgentID || event.SessionID != r.input.RunContext.SessionID {
		return storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	if _, duplicate := seen[event.ID]; duplicate {
		return storage.NewContractError(fmt.Errorf("continuation history repeats record %q", event.ID))
	}
	seen[event.ID] = struct{}{}
	return nil
}
