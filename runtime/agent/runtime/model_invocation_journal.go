package runtime

// model_invocation_journal.go owns tentative provider responses within one
// planner activity. The journal validates model-boundary values, isolates
// concurrent calls, and exports only the unique response identified by the
// planner's unchanged model-facing result.

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"

	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// modelPresentationKind identifies one user-visible event captured from a
	// provider response before the planner selects that response.
	modelPresentationKind uint8

	// modelPresentationEvent is the immutable provider output projected from one
	// model chunk or unary response.
	modelPresentationEvent struct {
		kind       modelPresentationKind
		text       string
		thinking   model.ThinkingPart
		toolCallID string
		toolName   tools.Ident
	}

	// modelInvocationCandidate is the complete provider-owned state for one
	// model call. It remains tentative until the journal matches its exact
	// model-facing result.
	modelInvocationCandidate struct {
		response     *model.Response
		presentation []modelPresentationEvent
		streamed     bool
		usageSeen    bool
		err          error
	}

	// modelFacingToolCall is the provider transcript identity of a planner
	// result call after synthetic tools are compiled to executable calls.
	modelFacingToolCall struct {
		id      string
		name    tools.Ident
		payload rawjson.Message
	}

	// modelInvocationMessageOwner identifies one message in a captured provider
	// response so additive planner metadata returns to that exact transcript row.
	modelInvocationMessageOwner struct {
		invocationID modelInvocationID
		messageIndex int
	}

	// modelInvocationJournal owns all tentative model responses for one planner
	// activity and implements modelInvocationSink independently from planner
	// event publication.
	modelInvocationJournal struct {
		mu           sync.Mutex
		invocations  map[modelInvocationID]*modelInvocationCandidate
		messageOwner map[*model.Message]modelInvocationMessageOwner
		designated   modelInvocationID
		selected     modelInvocationID
		usageEvents  []model.TokenUsage
		usage        model.TokenUsage
	}
)

const (
	modelPresentationText modelPresentationKind = iota + 1
	modelPresentationThinking
	modelPresentationToolCallDelta
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
	if !candidate.streamed {
		for i := range response.Content {
			candidate.presentation = append(candidate.presentation, presentationFromMessage(&response.Content[i])...)
		}
	}
	if j.messageOwner == nil {
		j.messageOwner = make(map[*model.Message]modelInvocationMessageOwner)
	}
	for i := range response.Content {
		j.messageOwner[&response.Content[i]] = modelInvocationMessageOwner{
			invocationID: invocationID,
			messageIndex: i,
		}
	}
	if !candidate.usageSeen {
		j.usage = addTokenUsage(j.usage, response.Usage)
		if response.Usage != (model.TokenUsage{}) {
			j.usageEvents = append(j.usageEvents, response.Usage)
		}
	}
	return nil
}

// recordModelChunk validates one provider presentation event and aggregates
// usage independently from the canonical response captured at EOF.
func (j *modelInvocationJournal) recordModelChunk(invocationID modelInvocationID, chunk model.Chunk) error {
	if err := model.ValidateChunk(chunk); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("model chunk references an unknown invocation")
	}
	candidate.streamed = true
	if usage, ok := chunk.(model.UsageChunk); ok {
		candidate.usageSeen = true
		j.usage = addTokenUsage(j.usage, usage.Usage)
		j.usageEvents = append(j.usageEvents, usage.Usage)
		return nil
	}
	candidate.presentation = append(candidate.presentation, presentationFromChunk(chunk)...)
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
	var owner modelInvocationMessageOwner
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
		matches := (hasOwner && id == owner.invocationID && modelInvocationMatches(candidate, calls)) ||
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
		var zero modelInvocationID
		j.selected = zero
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
	j.selected = selectedID
	captured, err := model.CloneResponse(selected.response)
	if err != nil {
		return nil, err
	}
	if hasOwner {
		providerMeta := captured.Content[owner.messageIndex].Meta
		plannerMeta := result.FinalResponse.Message.Meta
		for key, value := range providerMeta {
			plannerValue, ok := plannerMeta[key]
			if !ok || !reflect.DeepEqual(value, plannerValue) {
				return nil, errors.New("planner result modified provider-owned message metadata")
			}
		}
		enriched, err := model.CloneMessages([]*model.Message{result.FinalResponse.Message})
		if err != nil {
			return nil, err
		}
		captured.Content[owner.messageIndex].Meta = enriched[0].Meta
	}
	messages := make([]*model.Message, len(captured.Content))
	for i := range captured.Content {
		messages[i] = &captured.Content[i]
	}
	return messages, nil
}

// publishSelectedPresentation emits only the provider output selected by the
// planner result. Usage remains activity-wide so failed probes and retries are
// still accounted for.
func (j *modelInvocationJournal) publishSelectedPresentation(ctx context.Context, events planner.PlannerEvents) {
	j.mu.Lock()
	var presentation []modelPresentationEvent
	if selected := j.invocations[j.selected]; selected != nil {
		presentation = append(presentation, selected.presentation...)
	}
	usageEvents := append([]model.TokenUsage(nil), j.usageEvents...)
	j.mu.Unlock()

	for _, event := range presentation {
		switch event.kind {
		case modelPresentationText:
			events.AssistantChunk(ctx, event.text)
		case modelPresentationThinking:
			events.PlannerThinkingBlock(ctx, event.thinking)
		case modelPresentationToolCallDelta:
			events.ToolCallArgsDelta(ctx, event.toolCallID, event.toolName, event.text)
		}
	}
	for _, usage := range usageEvents {
		events.UsageDelta(ctx, usage)
	}
}

// presentationFromMessage projects unary response parts in provider order.
func presentationFromMessage(message *model.Message) []modelPresentationEvent {
	if message == nil {
		return nil
	}
	var presentation []modelPresentationEvent
	for _, part := range message.Parts {
		switch actual := part.(type) {
		case model.TextPart:
			if actual.Text != "" {
				presentation = append(presentation, modelPresentationEvent{
					kind: modelPresentationText,
					text: actual.Text,
				})
			}
		case model.CitationsPart:
			if actual.Text != "" {
				presentation = append(presentation, modelPresentationEvent{
					kind: modelPresentationText,
					text: actual.Text,
				})
			}
		case model.ThinkingPart:
			presentation = append(presentation, modelPresentationEvent{
				kind:     modelPresentationThinking,
				thinking: actual,
			})
		}
	}
	return presentation
}

// presentationFromChunk projects streaming provider output in receive order.
func presentationFromChunk(chunk model.Chunk) []modelPresentationEvent {
	switch actual := chunk.(type) {
	case model.TextChunk:
		var text string
		for _, part := range actual.Message.Parts {
			if value, ok := part.(model.TextPart); ok {
				text += value.Text
			}
		}
		if text != "" {
			return []modelPresentationEvent{{
				kind: modelPresentationText,
				text: text,
			}}
		}
	case model.ThinkingChunk:
		var presentation []modelPresentationEvent
		for _, part := range actual.Message.Parts {
			if value, ok := part.(model.ThinkingPart); ok {
				presentation = append(presentation, modelPresentationEvent{
					kind:     modelPresentationThinking,
					thinking: value,
				})
			}
		}
		return presentation
	case model.ToolCallDeltaChunk:
		return []modelPresentationEvent{{
			kind:       modelPresentationToolCallDelta,
			text:       actual.Delta.Delta,
			toolCallID: actual.Delta.ID,
			toolName:   actual.Delta.Name,
		}}
	}
	return nil
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
		case planner.AwaitItemKindToolClarification:
			calls = append(calls, modelFacingToolCall{
				id:      item.ToolClarification.ToolCallID,
				name:    item.ToolClarification.ToolName,
				payload: item.ToolClarification.Payload,
			})
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
