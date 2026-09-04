package runtime

// model_invocation_journal.go validates and correlates every model response
// made during one planner activity. Text and thinking from the designated
// planner call stream immediately. Emitted text is append-only. A selected
// response becomes durable as its complete provider transcript. Text emitted
// by a rejected response or ordinary failure reported before activity
// cancellation becomes durable as a plain assistant message before recovery or
// failure continues.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// modelInvocationCandidate holds one complete model call until the planner
	// returns the result that selected it.
	modelInvocationCandidate struct {
		response                 *model.Response
		responseEvidence         model.ResponseEvidence
		usageSeen                bool
		finished                 bool
		requestModelClass        model.ModelClass
		cancel                   context.CancelFunc
		done                     chan struct{}
		usage                    model.TokenUsage
		err                      error
		rejectedResponseEvidence *model.ResponseEvidence
		recoveryCorrection       string
		unadvertisedToolName     string
		rejectedValidationErr    *model.OutputValidationError
		rejectedOutputErr        error
		outcome                  *modelcall.Outcome
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

	// modelInvocationJournal keeps every model call made during one planner
	// activity separate from user-visible event publication.
	modelInvocationJournal struct {
		runtime       *Runtime
		runID         string
		sessionID     string
		responseID    string
		mu            sync.Mutex
		publishMu     sync.Mutex
		invocations   map[modelInvocationID]*modelInvocationCandidate
		order         []modelInvocationID
		designated    modelInvocationID
		selected      modelInvocationID
		recovery      modelInvocationID
		usage         model.TokenUsage
		outputErr     error
		publishedText strings.Builder
		sealed        bool
		sealedErr     error
		sealDone      chan struct{}
	}
)

// maxPublishedAssistantTextBytes leaves room for worst-case JSON escaping and
// rejection metadata inside the one-megabyte planner activity output contract.
const maxPublishedAssistantTextBytes = maxPlanActivityOutputBytes / 8

// beginModelInvocation creates a place to save one model response and the
// runtime-owned controls that stop and join it when planning ends.
func (j *modelInvocationJournal) beginModelInvocation(
	requestModelClass model.ModelClass,
	cancel context.CancelFunc,
) (modelInvocationID, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.sealedError(); err != nil {
		return modelInvocationID{}, err
	}
	if j.outputErr != nil {
		return modelInvocationID{}, j.outputErr
	}
	id := provenance.New()
	if j.invocations == nil {
		j.invocations = make(map[modelInvocationID]*modelInvocationCandidate)
	}
	j.invocations[id] = &modelInvocationCandidate{
		requestModelClass: requestModelClass,
		cancel:            cancel,
		done:              make(chan struct{}),
	}
	j.order = append(j.order, id)
	return id, nil
}

// seal prevents new model calls, cancels every call context, and waits for each
// provider operation to return. Completed calls retain their context through
// finalization so runtime-owned cleanup cannot look like an operation failure.
// Planner activity completion therefore means no model inference or usage
// accounting remains in flight.
func (j *modelInvocationJournal) seal() error {
	j.mu.Lock()
	if j.sealed {
		err := j.sealedErr
		done := j.sealDone
		j.mu.Unlock()
		<-done
		return err
	}
	j.sealed = true
	j.sealDone = make(chan struct{})
	outputErr := j.outputContractErrorLocked()
	type pendingInvocation struct {
		cancel context.CancelFunc
		done   <-chan struct{}
	}
	var invocations []pendingInvocation
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate == nil {
			continue
		}
		invocations = append(invocations, pendingInvocation{
			cancel: candidate.cancel,
			done:   candidate.done,
		})
	}
	if outputErr != nil {
		j.sealedErr = outputErr
	} else {
		for _, id := range j.order {
			candidate := j.invocations[id]
			if candidate != nil && !candidate.finished {
				j.sealedErr = outputcontract.NewWithOrigin(
					errors.New("planner returned while a model invocation was still running"),
					planner.OutputContractOriginPlanner,
				)
				break
			}
		}
	}
	err := j.sealedErr
	j.mu.Unlock()

	for _, invocation := range invocations {
		invocation.cancel()
	}
	for _, invocation := range invocations {
		<-invocation.done
	}
	close(j.sealDone)
	return err
}

// designateModelInvocation marks the one invocation owned by
// PlannerModelClient rather than a raw probing client.
func (j *modelInvocationJournal) designateModelInvocation(id modelInvocationID) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.sealedError(); err != nil {
		return err
	}
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

// stageRejectedModelOutput stores one validation failure without making it
// available for recovery. The activity considers it only after the model client
// freezes every provider and callback phase for this invocation.
func (j *modelInvocationJournal) stageRejectedModelOutput(
	invocationID modelInvocationID,
	evidence model.ResponseEvidence,
	err error,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.sealedError(); err != nil {
		return err
	}
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("invalid model response references an unknown invocation")
	}
	if evidence.Present {
		copied := evidence
		candidate.rejectedResponseEvidence = &copied
	}
	candidate.rejectedOutputErr = err
	var validationErr *model.OutputValidationError
	if errors.As(err, &validationErr) {
		candidate.rejectedValidationErr = validationErr
		candidate.recoveryCorrection = validationErr.RecoveryCorrection()
		candidate.unadvertisedToolName, _ = model.UnadvertisedToolName(validationErr)
	}
	return nil
}

// recordRejectedModelUsageTotal replaces this invocation's streamed deltas
// with the provider's terminal total. Providers report the same tokens in both
// forms, so adding the terminal value would count one invocation twice.
func (j *modelInvocationJournal) recordRejectedModelUsageTotal(
	invocationID modelInvocationID,
	rejected model.TokenUsage,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.sealedError(); err != nil {
		return err
	}
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("rejected model usage references an unknown invocation")
	}
	rejected = candidate.attributeUsage(rejected)
	previous := candidate.usage
	previousSeen := candidate.usageSeen
	candidate.usage = rejected
	candidate.usageSeen = true
	var activityUsage model.TokenUsage
	for _, id := range j.order {
		invocation := j.invocations[id]
		if invocation == nil || !invocation.usageSeen {
			continue
		}
		var err error
		activityUsage, err = addTokenUsage(activityUsage, invocation.usage)
		if err != nil {
			candidate.usage = previous
			candidate.usageSeen = previousSeen
			return outputcontract.NewWithOrigin(
				fmt.Errorf("aggregate rejected model usage total: %w", err),
				planner.OutputContractOriginPlanner,
			)
		}
	}
	j.usage = activityUsage
	return nil
}

// recordRejectedModelUsageDelta retains valid counts from one rejected usage
// chunk without treating them as an invocation total.
func (j *modelInvocationJournal) recordRejectedModelUsageDelta(
	ctx context.Context,
	invocationID modelInvocationID,
	delta model.TokenUsage,
) error {
	return j.recordModelChunk(ctx, invocationID, model.UsageChunk{Usage: delta})
}

// recordValidatedModelResponse saves a stream response already accepted by the
// model-owned stream boundary.
func (j *modelInvocationJournal) recordValidatedModelResponse(
	invocationID modelInvocationID,
	response *model.Response,
) error {
	return j.saveModelResponse(invocationID, response)
}

// saveModelResponse owns and stores one response for later planner-result
// matching and durable transcript publication.
func (j *modelInvocationJournal) saveModelResponse(
	invocationID modelInvocationID,
	response *model.Response,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.sealedError(); err != nil {
		return err
	}
	candidate := j.invocations[invocationID]
	if candidate == nil {
		return errors.New("model response references an unknown invocation")
	}
	if candidate.response != nil {
		return errors.New("model invocation returned multiple complete responses")
	}
	captured, err := model.CloneResponse(response)
	if err != nil {
		return err
	}
	candidate.response = captured
	candidate.responseEvidence = model.EvidenceForResponse(response)
	if !candidate.usageSeen && hasTokenCounts(response.Usage) {
		responseUsage := candidate.attributeUsage(response.Usage)
		usage, err := addTokenUsage(j.usage, responseUsage)
		if err != nil {
			return outputcontract.NewWithOrigin(
				fmt.Errorf("aggregate model usage: %w", err),
				planner.OutputContractOriginPlanner,
			)
		}
		j.usage = usage
		if responseUsage != (model.TokenUsage{}) {
			candidate.usage = responseUsage
			candidate.usageSeen = true
		}
	}
	return nil
}

// recordModelChunk accounts for one validated model chunk and immediately sends
// user-visible text or thinking from the designated planner invocation.
//
// Text and thinking fragments cannot be aggregated here: each fragment is
// useful by itself, and holding it until inference completes would remove the
// real-time UI behavior. Tool argument fragments have the opposite contract.
// A fragment is incomplete JSON and cannot execute or stand alone, so the
// inference client aggregates it and only the complete validated tool call may
// leave this boundary.
func (j *modelInvocationJournal) recordModelChunk(
	ctx context.Context,
	invocationID modelInvocationID,
	chunk model.Chunk,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	if err := j.sealedError(); err != nil {
		j.mu.Unlock()
		return err
	}
	candidate := j.invocations[invocationID]
	if candidate == nil {
		j.mu.Unlock()
		return errors.New("model chunk references an unknown invocation")
	}
	if usage, ok := chunk.(model.UsageChunk); ok {
		attributed := candidate.attributeUsage(usage.Usage)
		activityUsage, err := addTokenUsage(j.usage, attributed)
		if err != nil {
			j.mu.Unlock()
			return outputcontract.NewWithOrigin(
				fmt.Errorf("aggregate model usage: %w", err),
				planner.OutputContractOriginPlanner,
			)
		}
		accumulated, err := addTokenUsage(candidate.usage, attributed)
		if err != nil {
			j.mu.Unlock()
			return outputcontract.NewWithOrigin(
				fmt.Errorf("aggregate invocation usage: %w", err),
				planner.OutputContractOriginPlanner,
			)
		}
		accumulated.Model = attributed.Model
		accumulated.ModelClass = attributed.ModelClass
		candidate.usageSeen = true
		j.usage = activityUsage
		candidate.usage = accumulated
		j.mu.Unlock()
		return nil
	}
	if invocationID != j.designated {
		j.mu.Unlock()
		return nil
	}
	events := j.liveModelOutputEventsLocked(chunk)
	if len(events) == 0 {
		j.mu.Unlock()
		return nil
	}
	j.mu.Unlock()

	j.publishMu.Lock()
	defer j.publishMu.Unlock()
	for _, event := range events {
		if reply, ok := event.(stream.AssistantReply); ok {
			j.mu.Lock()
			nextSize := j.publishedText.Len() + len(reply.Data.Text)
			j.mu.Unlock()
			if nextSize > maxPublishedAssistantTextBytes {
				return outputcontract.NewWithOrigin(
					fmt.Errorf("published assistant text exceeds %d bytes", maxPublishedAssistantTextBytes),
					planner.OutputContractOriginModel,
				)
			}
		}
		published, err := j.publishLiveModelOutput(ctx, event)
		if err != nil {
			return err
		}
		if reply, ok := event.(stream.AssistantReply); published && ok {
			j.mu.Lock()
			j.publishedText.WriteString(reply.Data.Text)
			j.mu.Unlock()
		}
	}
	return nil
}

// finalizeModelInvocation freezes one complete provider and callback outcome.
// No recovery fact is visible until the activity evaluates every frozen call.
func (j *modelInvocationJournal) finalizeModelInvocation(
	invocationID modelInvocationID,
	outcome modelcall.Outcome,
) error {
	j.mu.Lock()
	candidate := j.invocations[invocationID]
	if candidate == nil {
		j.mu.Unlock()
		return errors.New("model completion references an unknown invocation")
	}
	if candidate.finished {
		j.mu.Unlock()
		return nil
	}
	candidate.finished = true
	frozen := outcome.Clone()
	candidate.outcome = &frozen
	candidate.err = frozen.Error()
	if recoverableModelCall(candidate) {
		// The frozen outcome still retains the close failure for tracing and
		// diagnostics. The operation result remains the exact staged rejection
		// so the activity can make its one typed recovery decision.
		candidate.err = candidate.rejectedOutputErr
	}
	if candidate.err == nil && candidate.response == nil {
		missingResponseErr := outputcontract.NewWithOrigin(
			errors.New("model stream ended without a complete response"),
			planner.OutputContractOriginModel,
		)
		candidate.outcome.Framework = modelcall.Result{Called: true, Err: missingResponseErr}
		candidate.err = missingResponseErr
	}
	var outputErr *planner.OutputContractError
	if j.outputErr == nil && errors.As(candidate.err, &outputErr) {
		j.outputErr = candidate.err
	}
	sealedErr := j.sealedError()
	close(candidate.done)
	j.mu.Unlock()
	if sealedErr != nil {
		return sealedErr
	}
	return nil
}

// exportModelInvocation matches the planner result to the exact saved message
// or tool calls and returns that response without rebuilding it.
func (j *modelInvocationJournal) exportModelInvocation(
	result *planner.PlanResult,
) ([]*model.Message, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.outputErr != nil {
		return nil, j.outputErr
	}
	calls := planResultModelToolCalls(result)
	var owner modelInvocationMessageOwner
	var hasOwner bool
	if result != nil && result.FinalResponse != nil {
		for id, candidate := range j.invocations {
			if candidate.response == nil {
				continue
			}
			for messageIndex := range candidate.response.Content {
				if !model.SameMessageOrigin(
					&candidate.response.Content[messageIndex],
					result.FinalResponse.Message,
				) {
					continue
				}
				if hasOwner {
					return nil, errors.New("planner result repeats one model message origin")
				}
				owner = modelInvocationMessageOwner{
					invocationID: id,
					messageIndex: messageIndex,
				}
				hasOwner = true
			}
		}
	}
	if len(j.invocations) == 0 {
		return nil, nil
	}
	var selected *modelInvocationCandidate
	var selectedID modelInvocationID
	var selectedOwner modelInvocationMessageOwner
	var selectedHasOwner bool
	for id, candidate := range j.invocations {
		if candidate.response == nil || candidate.err != nil {
			continue
		}
		if !j.designated.IsZero() && !hasOwner && id != j.designated {
			continue
		}
		candidateOwner := owner
		candidateHasOwner := hasOwner && id == owner.invocationID
		matches := (candidateHasOwner && modelInvocationMatches(candidate, calls)) ||
			(!hasOwner &&
				(result == nil || result.FinalResponse == nil) &&
				len(calls) > 0 &&
				modelInvocationMatches(candidate, calls))
		if !matches {
			continue
		}
		if selected != nil {
			return nil, errors.New("planner result matches multiple model invocations")
		}
		selected = candidate
		selectedID = id
		selectedOwner = candidateOwner
		selectedHasOwner = candidateHasOwner
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
	if selected.response.OutputLimited && len(selected.response.ToolCalls()) > 0 {
		err := outputcontract.NewWithOrigin(
			errors.New("model response reached its generated-output limit before completing the tool-call batch"),
			planner.OutputContractOriginModel,
		)
		j.selected = selectedID
		evidence := selected.responseEvidence
		selected.rejectedResponseEvidence = &evidence
		j.outputErr = err
		return nil, err
	}
	j.selected = selectedID
	owner = selectedOwner
	hasOwner = selectedHasOwner
	captured, err := model.CloneResponse(selected.response)
	if err != nil {
		return nil, err
	}
	if hasOwner {
		providerMessage := &captured.Content[owner.messageIndex]
		plannerMessage := result.FinalResponse.Message
		if providerMessage.Role != plannerMessage.Role ||
			!reflect.DeepEqual(providerMessage.Parts, plannerMessage.Parts) {
			return nil, errors.New("planner result modified provider-owned message content")
		}
		providerMeta := providerMessage.Meta
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
		providerMessage.Meta = enriched[0].Meta
		result.FinalResponse.Message = providerMessage
	}
	messages := make([]*model.Message, len(captured.Content))
	for i := range captured.Content {
		messages[i] = &captured.Content[i]
	}
	return messages, nil
}

// selectedCompiledModelCalls returns transcript identity for executable calls
// that the planner derived one-to-one from the selected provider response.
// exportModelInvocation must succeed first so selected identifies the accepted
// response.
func (j *modelInvocationJournal) selectedCompiledModelCalls(result *planner.PlanResult) (map[string]model.ToolCall, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return compiledModelCalls(j.invocations[j.selected], result)
}

// publishUsage copies every provider usage report into the supplied activity
// event collector. Callers may build and discard an accepted output before
// rebuilding a bounded rejection, so this method does not retain publication
// state.
func (j *modelInvocationJournal) publishUsage(ctx context.Context, events planner.PlannerEvents) {
	j.mu.Lock()
	usageEvents := make([]model.TokenUsage, 0, len(j.order))
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate != nil && candidate.usage != (model.TokenUsage{}) {
			usageEvents = append(usageEvents, candidate.usage)
		}
	}
	j.mu.Unlock()
	for _, usage := range usageEvents {
		events.UsageDelta(ctx, usage)
	}
}

func (j *modelInvocationJournal) publishedAssistantText() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.publishedText.String()
}

// attributeUsage applies the logical model class owned by the immutable
// request. Missing provider model identity remains empty.
func (c *modelInvocationCandidate) attributeUsage(usage model.TokenUsage) model.TokenUsage {
	usage.ModelClass = c.requestModelClass
	return usage
}

// hasTokenCounts reports whether a terminal response actually reported usage
// rather than carrying only request identity stamped by validation.
func hasTokenCounts(usage model.TokenUsage) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.CacheReadTokens != 0 ||
		usage.CacheWriteTokens != 0
}

// liveModelOutputEventsLocked converts one provider chunk into the client
// events that are independently useful. The caller holds j.mu while it reads
// the planner activity's response identifier.
func (j *modelInvocationJournal) liveModelOutputEventsLocked(chunk model.Chunk) []stream.Event {
	switch actual := chunk.(type) {
	case model.TextChunk:
		var events []stream.Event
		for _, part := range actual.Message.Parts {
			var text string
			switch value := part.(type) {
			case model.TextPart:
				text = value.Text
			case model.CitationsPart:
				text = value.Text
			}
			if text == "" {
				continue
			}
			payload := stream.AssistantReplyPayload{
				ResponseID: j.responseID,
				Text:       text,
			}
			events = append(events, stream.AssistantReply{
				Base: stream.NewBase(stream.EventAssistantReply, j.runID, j.sessionID, payload),
				Data: payload,
			})
		}
		return events
	case model.ThinkingChunk:
		var events []stream.Event
		for _, part := range actual.Message.Parts {
			value, ok := part.(model.ThinkingPart)
			if !ok || value.Text == "" {
				continue
			}
			payload := stream.PlannerThoughtPayload{
				ResponseID: j.responseID,
				Note:       value.Text,
			}
			events = append(events, stream.PlannerThought{
				Base: stream.NewBase(stream.EventPlannerThought, j.runID, j.sessionID, payload),
				Data: payload,
			})
		}
		return events
	case model.ToolCallDeltaChunk:
		// Partial tool arguments stay inside the model client. The provider
		// adapter aggregates these fragments and validation produces one complete
		// model.ToolCall that the planner and workflow can safely consume.
		return nil
	}
	return nil
}

// publishLiveModelOutput sends one text or thinking event directly to the
// session stream. It deliberately bypasses PlannerEvents, the run log, and the
// hook bus.
func (j *modelInvocationJournal) publishLiveModelOutput(ctx context.Context, event stream.Event) (bool, error) {
	if j.runtime == nil {
		return false, nil
	}
	return j.runtime.publishModelOutput(ctx, j.sessionID, event)
}

// planResultModelToolCalls returns every provider-native tool call that the
// workflow will record from result, including out-of-band await calls.
func planResultModelToolCalls(result *planner.PlanResult) []modelFacingToolCall {
	if result == nil {
		return nil
	}
	calls := make([]modelFacingToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		if call.ModelToolCallID == "" {
			continue
		}
		calls = append(calls, modelFacingToolCall{
			id:      call.ModelToolCallID,
			name:    call.Name,
			payload: call.Payload,
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
				id:      item.ToolClarification.ModelToolCallID,
				name:    item.ToolClarification.ToolName,
				payload: item.ToolClarification.Payload,
			})
		case planner.AwaitItemKindQuestions:
			calls = append(calls, modelFacingToolCall{
				id:      item.Questions.ModelToolCallID,
				name:    item.Questions.ToolName,
				payload: item.Questions.Payload,
			})
		case planner.AwaitItemKindExternalTools:
			for _, call := range item.ExternalTools.Items {
				calls = append(calls, modelFacingToolCall{
					id:      call.ModelToolCallID,
					name:    call.Name,
					payload: call.Payload,
				})
			}
		}
	}
	return calls
}

// runtimePlanResultModelToolCalls returns provider-facing identities from a
// validated runtime result. Executable names and payloads may differ after
// continuation binding or policy rewrites.
func runtimePlanResultModelToolCalls(result *PlanResult) []modelFacingToolCall {
	if result == nil {
		return nil
	}
	calls := make([]modelFacingToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		calls = append(calls, modelFacingToolCall{
			id:      transcriptToolCallID(call),
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
				id:      item.ToolClarification.ModelToolCallID,
				name:    item.ToolClarification.ToolName,
				payload: item.ToolClarification.Payload,
			})
		case planner.AwaitItemKindQuestions:
			calls = append(calls, modelFacingToolCall{
				id:      item.Questions.ModelToolCallID,
				name:    item.Questions.ToolName,
				payload: item.Questions.Payload,
			})
		case planner.AwaitItemKindExternalTools:
			for _, call := range item.ExternalTools.Items {
				calls = append(calls, modelFacingToolCall{
					id:      call.ModelToolCallID,
					name:    call.Name,
					payload: call.Payload,
				})
			}
		}
	}
	return calls
}

// transcriptToolCallID returns the provider correlation ID used by tool_use
// and tool_result parts. Runtime-authored calls have no provider identity, so
// their execution ID also identifies the synthetic transcript pair.
func transcriptToolCallID(call ToolCall) string {
	if call.ModelToolCallID != "" {
		return call.ModelToolCallID
	}
	return call.ToolCallID
}

// modelInvocationMatches reports whether every finalized call preserves one
// provider tool-call ID. The runtime compares names and payloads separately so
// a deterministic one-to-one compiler may replace a model-facing tool without
// losing the exact provider response that selected it.
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
		if _, ok := byID[call.id]; !ok {
			return false
		}
		delete(byID, call.id)
	}
	return len(byID) == 0
}

// compiledModelCalls returns provider names and payloads that differ from the
// planner-authored executable intent with the same tool-call ID. The runtime
// copies these values into execution calls after planner validation.
func compiledModelCalls(
	candidate *modelInvocationCandidate,
	result *planner.PlanResult,
) (map[string]model.ToolCall, error) {
	if candidate == nil || candidate.response == nil || result == nil {
		return nil, nil
	}
	captured := make(map[string]model.ToolCall)
	for _, call := range candidate.response.ToolCalls() {
		captured[call.ID] = call
	}
	bindings := make(map[string]model.ToolCall)
	for _, call := range result.ToolCalls {
		if call.ModelToolCallID == "" {
			continue
		}
		source, ok := captured[call.ModelToolCallID]
		if !ok {
			return nil, fmt.Errorf(
				"planner tool %q supplied a model call ID that does not belong to its selected model response",
				call.Name,
			)
		}
		bindings[call.ModelToolCallID] = source
	}
	return bindings, nil
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

// sealedError reports why provider callbacks can no longer modify the journal.
// The caller must hold j.mu.
func (j *modelInvocationJournal) sealedError() error {
	if !j.sealed {
		return nil
	}
	if j.sealedErr != nil {
		return j.sealedErr
	}
	return errors.New("planner model invocation journal is sealed")
}
