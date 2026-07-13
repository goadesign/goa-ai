package runtime

// model_invocation_journal.go owns tentative provider responses within one
// planner activity. The journal validates model-boundary values, isolates
// concurrent calls, and exports only the unique response identified by the
// planner's unchanged model-facing result.

import (
	"bytes"
	"errors"
	"sync"

	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// modelInvocationCandidate is the complete provider-owned state for one
	// model call. It remains tentative until the journal matches its exact
	// model-facing result.
	modelInvocationCandidate struct {
		response  *model.Response
		usageSeen bool
		err       error
	}

	// modelFacingToolCall is the provider transcript identity of a planner
	// result call after synthetic tools are compiled to executable calls.
	modelFacingToolCall struct {
		id      string
		name    tools.Ident
		payload rawjson.Message
	}

	// modelInvocationJournal owns all tentative model responses for one planner
	// activity and implements modelInvocationSink independently from planner
	// event publication.
	modelInvocationJournal struct {
		mu           sync.Mutex
		invocations  map[modelInvocationID]*modelInvocationCandidate
		messageOwner map[*model.Message]modelInvocationID
		designated   modelInvocationID
		usage        model.TokenUsage
	}
)

// beginModelInvocation creates an isolated response candidate.
func (j *modelInvocationJournal) beginModelInvocation() modelInvocationID {
	j.mu.Lock()
	defer j.mu.Unlock()
	id := provenance.New()
	if j.invocations == nil {
		j.invocations = make(map[modelInvocationID]*modelInvocationCandidate)
	}
	j.invocations[id] = &modelInvocationCandidate{}
	return id
}

// designateModelInvocation marks the one invocation owned by
// PlannerModelClient rather than a raw probing client.
func (j *modelInvocationJournal) designateModelInvocation(id modelInvocationID) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.designated.IsZero() {
		return errors.New("planner activity used PlannerModelClient for multiple model invocations")
	}
	candidate := j.invocations[id]
	if candidate == nil {
		return errors.New("cannot designate an unknown model invocation")
	}
	j.designated = id
	return nil
}

// recordModelResponse validates and captures one canonical provider response
// before planner code can transform it into a decision.
func (j *modelInvocationJournal) recordModelResponse(
	invocationID modelInvocationID,
	response *model.Response,
) error {
	if err := model.ValidateResponse(response); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("model response references an unknown invocation")
	}
	if candidate.response != nil {
		return errors.New("model invocation returned multiple canonical responses")
	}
	captured, err := model.CloneResponse(response)
	if err != nil {
		return err
	}
	candidate.response = captured
	if j.messageOwner == nil {
		j.messageOwner = make(map[*model.Message]modelInvocationID)
	}
	for i := range response.Content {
		j.messageOwner[&response.Content[i]] = invocationID
	}
	if !candidate.usageSeen {
		j.usage = addTokenUsage(j.usage, response.Usage)
	}
	return nil
}

// recordModelChunk validates one provider presentation event and aggregates
// usage independently from the canonical response captured at EOF.
func (j *modelInvocationJournal) recordModelChunk(invocationID modelInvocationID, chunk model.Chunk) error {
	if err := model.ValidateChunk(chunk); err != nil {
		return err
	}
	if usage, ok := chunk.(model.UsageChunk); ok {
		j.mu.Lock()
		defer j.mu.Unlock()
		candidate := j.invocations[invocationID]
		if candidate == nil {
			return errors.New("model chunk references an unknown invocation")
		}
		candidate.usageSeen = true
		j.usage = addTokenUsage(j.usage, usage.Usage)
		return nil
	}
	return nil
}

// finishModelInvocation records a failed invocation or verifies that a
// successful stream supplied its canonical response.
func (j *modelInvocationJournal) finishModelInvocation(invocationID modelInvocationID, err error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("model completion references an unknown invocation")
	}
	if err != nil {
		candidate.err = err
		return err
	}
	if candidate.response == nil {
		candidate.err = errors.New("model stream ended without a canonical response")
		return candidate.err
	}
	return nil
}

// exportModelInvocation matches canonical message identity or exact model-facing tool
// identities and returns the selected response without reconstructing it.
func (j *modelInvocationJournal) exportModelInvocation(
	result *planner.PlanResult,
) ([]*model.Message, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	calls := planResultModelToolCalls(result)
	var owner modelInvocationID
	var hasOwner bool
	if result != nil && result.FinalResponse != nil {
		owner, hasOwner = j.messageOwner[result.FinalResponse.Message]
	}
	if len(j.invocations) == 0 {
		return nil, nil
	}
	var selected *modelInvocationCandidate
	var selectedID modelInvocationID
	for id, candidate := range j.invocations {
		if candidate.response == nil || candidate.err != nil {
			continue
		}
		if !j.designated.IsZero() && !hasOwner && id != j.designated {
			continue
		}
		matches := (hasOwner && id == owner && modelInvocationMatches(candidate, calls)) ||
			(!hasOwner && len(calls) > 0 && modelInvocationMatches(candidate, calls))
		if !matches {
			continue
		}
		if selected != nil {
			return nil, errors.New("planner result matches multiple model invocations")
		}
		selected = candidate
		selectedID = id
	}
	if selected == nil {
		if hasOwner {
			return nil, errors.New("planner result did not preserve the selected model invocation")
		}
		if modelInvocationOwnsAnyCall(j.invocations, calls) {
			return nil, errors.New("planner result modified or mixed model-authored tool calls")
		}
		if !j.designated.IsZero() {
			return nil, errors.New("planner result discarded the PlannerModelClient invocation")
		}
		return nil, nil
	}
	if !j.designated.IsZero() && selectedID != j.designated {
		return nil, errors.New("planner result selected a probe after using PlannerModelClient")
	}
	captured, err := model.CloneResponse(selected.response)
	if err != nil {
		return nil, err
	}
	messages := make([]*model.Message, len(captured.Content))
	for i := range captured.Content {
		messages[i] = &captured.Content[i]
	}
	return messages, nil
}

// planResultModelToolCalls returns every provider-native tool call that the
// workflow will record from result, including out-of-band await calls.
func planResultModelToolCalls(result *planner.PlanResult) []modelFacingToolCall {
	if result == nil {
		return nil
	}
	calls := make([]modelFacingToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		calls = append(calls, modelFacingToolCall{
			id:      call.ToolCallID,
			name:    call.TranscriptName(),
			payload: call.TranscriptPayload(),
		})
	}
	if result.Await == nil {
		return calls
	}
	for _, item := range result.Await.Items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
		case planner.AwaitItemKindQuestions:
			calls = append(calls, modelFacingToolCall{
				id:      item.Questions.ToolCallID,
				name:    item.Questions.ToolName,
				payload: item.Questions.Payload,
			})
		case planner.AwaitItemKindExternalTools:
			for _, call := range item.ExternalTools.Items {
				calls = append(calls, modelFacingToolCall{
					id:      call.ToolCallID,
					name:    call.Name,
					payload: call.Payload,
				})
			}
		}
	}
	return calls
}

// modelInvocationMatches reports whether calls are exactly the finalized tool
// calls emitted by candidate. PlanResult grouping cannot change provider order
// because the selected response itself is persisted.
func modelInvocationMatches(candidate *modelInvocationCandidate, calls []modelFacingToolCall) bool {
	capturedCalls := candidate.response.ToolCalls()
	if len(capturedCalls) != len(calls) {
		return false
	}
	byID := make(map[string]model.ToolCall, len(capturedCalls))
	for _, call := range capturedCalls {
		if call.ID == "" {
			return false
		}
		if _, exists := byID[call.ID]; exists {
			return false
		}
		byID[call.ID] = call
	}
	for _, call := range calls {
		captured, ok := byID[call.id]
		if !ok || captured.Name != call.name || !bytes.Equal(captured.Payload, call.payload) {
			return false
		}
		delete(byID, call.id)
	}
	return len(byID) == 0
}

// modelInvocationOwnsAnyCall reports whether calls contain an ID captured from
// any response, distinguishing planner-authored calls from corrupted model
// output.
func modelInvocationOwnsAnyCall(
	invocations map[modelInvocationID]*modelInvocationCandidate,
	calls []modelFacingToolCall,
) bool {
	ids := make(map[string]struct{})
	for _, candidate := range invocations {
		if candidate.response == nil {
			continue
		}
		for _, call := range candidate.response.ToolCalls() {
			ids[call.ID] = struct{}{}
		}
	}
	for _, call := range calls {
		if _, ok := ids[call.id]; ok {
			return true
		}
	}
	return false
}

// exportUsage returns token usage for every invocation, including probes that
// the planner did not select.
func (j *modelInvocationJournal) exportUsage() model.TokenUsage {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.usage
}
