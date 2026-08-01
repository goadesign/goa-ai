package runtime

// This file owns model-visible bounded-result continuation references.
// Providers keep their opaque cursor bytes in durable Bounds metadata; planners
// receive a short reference whose scope and freshness are verified before the
// runtime reconstructs the originating arguments with the provider cursor.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// continuationUseError distinguishes rejected model-authored references from
	// failures to read or interpret the runtime's durable continuation state.
	continuationUseError struct {
		cause error
	}
)

const continuationPrefix = "next"

func (e *continuationUseError) Error() string {
	return e.cause.Error()
}

func (e *continuationUseError) Unwrap() error {
	return e.cause
}

// attachContinuation derives the model-visible reference after a provider has
// returned a cursor and before bounds validation, projection, and persistence.
func attachContinuation(call planner.ToolRequest, result *planner.ToolResult) error {
	if result == nil || result.Failure != nil || result.Bounds == nil {
		return nil
	}
	if result.Bounds.NextCursor == nil {
		if result.Bounds.Continuation != nil {
			return fmt.Errorf("tool %q returned a continuation without a provider cursor", call.Name)
		}
		return nil
	}
	if *result.Bounds.NextCursor == "" {
		return fmt.Errorf("tool %q returned an empty provider cursor", call.Name)
	}
	if result.Bounds.Continuation != nil {
		return fmt.Errorf("tool %q returned a model-visible continuation", call.Name)
	}
	if call.RunID == "" || call.SessionID == "" || call.ToolCallID == "" || call.Name == "" {
		return fmt.Errorf("tool %q provider cursor requires run, session, call, and tool identities", call.Name)
	}
	reference := continuationReference(call.RunID, call.SessionID, call.Name, call.ToolCallID, *result.Bounds.NextCursor)
	result.Bounds.Continuation = &reference
	return nil
}

// resolvePlanContinuations authorizes every paging reference in a planner
// result against durable history and rewrites only the execution payload. The
// exact model-authored payload remains available through TranscriptPayload.
func (r *Runtime) resolvePlanContinuations(ctx context.Context, input *PlanActivityInput, result *planner.PlanResult) error {
	if result == nil || len(result.ToolCalls) == 0 {
		return nil
	}
	type pendingCall struct {
		index       int
		cursorField string
		reference   string
		payload     map[string]any
	}
	var pending []pendingCall
	uses := make(map[string]int)
	for i := range result.ToolCalls {
		call := &result.ToolCalls[i]
		if call.PreflightFailure != nil {
			return fmt.Errorf("planner authored preflight failure for %q", call.Name)
		}
		spec, ok := r.toolSpec(call.Name)
		if !ok || spec.Bounds == nil || spec.Bounds.Paging == nil {
			continue
		}
		payload, err := decodePayloadObject(call.Payload)
		if err != nil {
			return fmt.Errorf("resolve continuation for %q: %w", call.Name, err)
		}
		cursor, present := payload[spec.Bounds.Paging.CursorField]
		if !present {
			continue
		}
		reference, ok := cursor.(string)
		if !ok || reference == "" {
			call.PreflightFailure = invalidContinuationFailure(
				fmt.Errorf("tool %q continuation must be a non-empty string", call.Name),
			)
			continue
		}
		if err := validateContinuationScope(
			reference,
			input.RunID,
			input.RunContext.SessionID,
			call.Name,
		); err != nil {
			call.PreflightFailure = invalidContinuationFailure(err)
			continue
		}
		uses[reference]++
		pending = append(pending, pendingCall{
			index:       i,
			cursorField: spec.Bounds.Paging.CursorField,
			reference:   reference,
			payload:     payload,
		})
	}
	if len(pending) == 0 {
		return nil
	}
	unique := pending[:0]
	for _, item := range pending {
		if uses[item.reference] > 1 {
			result.ToolCalls[item.index].PreflightFailure = invalidContinuationFailure(
				fmt.Errorf("continuation %q is used more than once in the same tool batch", item.reference),
			)
			continue
		}
		unique = append(unique, item)
	}
	pending = unique
	if len(pending) == 0 {
		return nil
	}
	if r.Memory == nil {
		return errors.New("continuation resolution requires durable memory")
	}
	snapshot, err := r.Memory.LoadRun(ctx, string(input.AgentID), input.RunID)
	if err != nil {
		return fmt.Errorf("load continuation history: %w", err)
	}
	for _, item := range pending {
		call := &result.ToolCalls[item.index]
		cursor, originPayload, err := resolveContinuation(
			snapshot.Events,
			call.Name,
			item.cursorField,
			item.reference,
			item.payload,
		)
		if err != nil {
			var useErr *continuationUseError
			if errors.As(err, &useErr) {
				call.PreflightFailure = invalidContinuationFailure(useErr)
				continue
			}
			return fmt.Errorf("resolve continuation for %q: %w", call.Name, err)
		}
		if len(call.ModelPayload) == 0 {
			call.ModelPayload = append(rawjson.Message(nil), call.Payload...)
		}
		originPayload[item.cursorField] = cursor
		executionPayload, err := json.Marshal(originPayload)
		if err != nil {
			return fmt.Errorf("encode execution payload for %q: %w", call.Name, err)
		}
		call.Payload = rawjson.Message(executionPayload)
	}
	return nil
}

// resolveContinuation finds one already scope-authorized reference and its
// originating call/result pair, then enforces continuation-only and freshness
// invariants against durable history.
func resolveContinuation(events []memory.Event, toolName tools.Ident, cursorField, reference string, current map[string]any) (string, map[string]any, error) {
	if len(current) != 1 {
		return "", nil, &continuationUseError{
			cause: fmt.Errorf("continuation %q must be the only argument", reference),
		}
	}
	calls := make(map[string]memory.ToolCallData)
	callIndexes := make(map[string]int)
	resultIndex := -1
	var origin memory.ToolResultData
	for i, event := range events {
		switch event.Type {
		case memory.EventToolCall:
			call, err := memory.DecodeToolCallData(event)
			if err != nil {
				return "", nil, fmt.Errorf("decode durable tool call: %w", err)
			}
			calls[call.ToolCallID] = call
			callIndexes[call.ToolCallID] = i
		case memory.EventToolResult:
			result, err := memory.DecodeToolResultData(event)
			if err != nil {
				return "", nil, fmt.Errorf("decode durable tool result: %w", err)
			}
			if result.Bounds == nil || result.Bounds.Continuation == nil || *result.Bounds.Continuation != reference {
				continue
			}
			if resultIndex >= 0 {
				return "", nil, fmt.Errorf("continuation %q is not unique", reference)
			}
			resultIndex = i
			origin = result
		case memory.EventUserMessage, memory.EventAssistantMessage, memory.EventPlannerNote, memory.EventThinking:
		}
	}
	if resultIndex < 0 {
		return "", nil, &continuationUseError{
			cause: fmt.Errorf("continuation %q does not exist in this run", reference),
		}
	}
	if origin.ToolName != toolName {
		return "", nil, fmt.Errorf("continuation %q belongs to tool %q", reference, origin.ToolName)
	}
	if origin.Bounds.NextCursor == nil {
		return "", nil, fmt.Errorf("continuation %q has no provider cursor", reference)
	}
	prior, ok := calls[origin.ToolCallID]
	if !ok || callIndexes[origin.ToolCallID] >= resultIndex {
		return "", nil, fmt.Errorf("continuation %q has no originating tool call", reference)
	}
	if prior.ToolName != toolName {
		return "", nil, fmt.Errorf("continuation %q originating call belongs to tool %q", reference, prior.ToolName)
	}
	priorPayload, err := decodePayloadObject(prior.PayloadJSON)
	if err != nil {
		return "", nil, fmt.Errorf("decode originating payload: %w", err)
	}
	used, err := continuationWasUsed(events[resultIndex+1:], toolName, cursorField, *origin.Bounds.NextCursor, priorPayload)
	if err != nil {
		return "", nil, err
	}
	if used {
		return "", nil, &continuationUseError{
			cause: fmt.Errorf("continuation %q is stale", reference),
		}
	}
	return *origin.Bounds.NextCursor, priorPayload, nil
}

// invalidContinuationFailure returns the ordinary failed-tool contract used
// when a model-authored continuation cannot be authorized for execution.
func invalidContinuationFailure(err error) *planner.ToolFailure {
	return &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.ToolErrorFromError(err),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}
}

// continuationWasUsed recognizes a later persisted execution with the exact
// provider cursor and non-cursor arguments from the originating call.
func continuationWasUsed(events []memory.Event, toolName tools.Ident, cursorField, cursor string, origin map[string]any) (bool, error) {
	for _, event := range events {
		if event.Type != memory.EventToolCall {
			continue
		}
		call, err := memory.DecodeToolCallData(event)
		if err != nil {
			return false, fmt.Errorf("decode later durable tool call: %w", err)
		}
		if call.ToolName != toolName {
			continue
		}
		payload, err := decodePayloadObject(call.PayloadJSON)
		if err != nil {
			return false, fmt.Errorf("decode later durable tool payload: %w", err)
		}
		if payload[cursorField] == cursor && equalArguments(origin, payload, cursorField) {
			return true, nil
		}
	}
	return false, nil
}

// decodePayloadObject decodes the object-shaped generated tool payload used by
// paging contracts.
func decodePayloadObject(payload rawjson.Message) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("tool payload must be a JSON object")
	}
	return object, nil
}

// equalArguments compares the generated JSON argument contract after
// removing the one runtime-owned paging field.
func equalArguments(left, right map[string]any, cursorField string) bool {
	leftJSON, leftErr := json.Marshal(argumentsWithoutCursor(left, cursorField))
	rightJSON, rightErr := json.Marshal(argumentsWithoutCursor(right, cursorField))
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

// argumentsWithoutCursor returns the authored portion of one decoded payload
// without mutating durable state later reused for execution reconstruction.
func argumentsWithoutCursor(arguments map[string]any, cursorField string) map[string]any {
	out := make(map[string]any, len(arguments))
	for name, value := range arguments {
		if name != cursorField {
			out[name] = value
		}
	}
	return out
}

// continuationReference derives a collision-resistant reference whose visible
// scope prefixes allow cross-run, cross-session, and cross-tool rejection before lookup.
func continuationReference(runID, sessionID string, toolName tools.Ident, toolCallID, cursor string) string {
	runIDHash := sha256.Sum256([]byte(runID))
	session := sha256.Sum256([]byte(sessionID))
	tool := sha256.Sum256([]byte(toolName))
	identity := sha256.Sum256([]byte(runID + "\x00" + sessionID + "\x00" + string(toolName) + "\x00" + toolCallID + "\x00" + cursor))
	return strings.Join([]string{
		continuationPrefix,
		hex.EncodeToString(runIDHash[:4]),
		hex.EncodeToString(session[:4]),
		hex.EncodeToString(tool[:4]),
		hex.EncodeToString(identity[:16]),
	}, "-")
}

// validateContinuationScope rejects references issued for another run, session,
// or tool before durable cursor lookup.
func validateContinuationScope(reference, runID, sessionID string, toolName tools.Ident) error {
	parts := strings.Split(reference, "-")
	if len(parts) != 5 || parts[0] != continuationPrefix {
		return fmt.Errorf("invalid continuation reference %q", reference)
	}
	runIDHash := sha256.Sum256([]byte(runID))
	if parts[1] != hex.EncodeToString(runIDHash[:4]) {
		return fmt.Errorf("continuation %q belongs to another run", reference)
	}
	session := sha256.Sum256([]byte(sessionID))
	if parts[2] != hex.EncodeToString(session[:4]) {
		return fmt.Errorf("continuation %q belongs to another session", reference)
	}
	tool := sha256.Sum256([]byte(toolName))
	if parts[3] != hex.EncodeToString(tool[:4]) {
		return fmt.Errorf("continuation %q belongs to another tool", reference)
	}
	identity, err := hex.DecodeString(parts[4])
	if err != nil || len(identity) != 16 {
		return fmt.Errorf("invalid continuation reference %q", reference)
	}
	return nil
}
