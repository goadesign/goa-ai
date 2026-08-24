// Package model defines the provider-neutral request, response, and streaming
// contracts used by planners and model adapters.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// streamValidator checks one provider stream against its complete response.
	// The validated stream calls accept for each owned chunk and finish at EOF.
	streamValidator struct {
		modelClass         ModelClass
		completionName     string
		expectsCompletion  bool
		stopped            bool
		stopReason         string
		text               strings.Builder
		textSeen           bool
		citations          []citationsPartSnapshot
		thinking           map[int]thinkingSnapshot
		thinkingOrder      []int
		toolCalls          []toolCallSnapshot
		finalToolCallIDs   map[string]struct{}
		toolDeltaNames     map[string]string
		toolDeltaPayloads  map[string]*strings.Builder
		usage              TokenUsage
		usageSeen          bool
		completion         *completionSnapshot
		completionValidate func(*Response, *Completion) error
		toolValidators     map[tools.Ident]toolCallValidator
		toolChoiceMode     ToolChoiceMode
		toolChoiceName     tools.Ident
		contract           streamValidationContract
		budget             dynamicValueWalk
	}

	// citationsPartSnapshot retains citation details without provider-owned
	// slices or pointers.
	citationsPartSnapshot struct {
		text      string
		citations []citationSnapshot
	}

	// citationSnapshot retains one citation using framework-owned scalar values.
	citationSnapshot struct {
		title            string
		source           string
		sourceContent    []string
		documentChar     DocumentCharLocation
		hasDocumentChar  bool
		documentChunk    DocumentChunkLocation
		hasDocumentChunk bool
		documentPage     DocumentPageLocation
		hasDocumentPage  bool
	}

	// thinkingSnapshot retains reasoning metadata without its mutable redacted
	// byte slice.
	thinkingSnapshot struct {
		text      string
		signature string
		redacted  string
		index     int
		final     bool
	}

	// toolCallSnapshot retains tool-call JSON as an immutable string.
	toolCallSnapshot struct {
		name             tools.Ident
		payload          string
		id               string
		thoughtSignature string
	}

	// completionSnapshot retains structured output JSON as an immutable string.
	completionSnapshot struct {
		name    string
		payload string
	}
)

// newStreamValidator creates mutable reconciliation state for one immutable
// request contract.
func newStreamValidator(contract *RequestContract) *streamValidator {
	validator := &streamValidator{
		finalToolCallIDs:   make(map[string]struct{}),
		toolDeltaNames:     make(map[string]string),
		toolDeltaPayloads:  make(map[string]*strings.Builder),
		thinking:           make(map[int]thinkingSnapshot),
		toolValidators:     contract.toolValidators,
		contract:           contract.stream,
		modelClass:         contract.stream.modelClass,
		completionValidate: contract.completionValidate,
		toolChoiceMode:     contract.toolChoiceMode,
		toolChoiceName:     contract.toolChoiceName,
	}
	if contract.stream.structuredOutputPresent {
		validator.expectsCompletion = true
		validator.completionName = contract.stream.structuredOutputName
	}
	return validator
}

// SetCompletionValidator attaches caller-supplied low-level validation to one
// structured-output request. The model client runs it before returning a unary
// response, final completion chunk, or clean stream EOF. Streaming calls first
// pass nil response with one completion for pre-exposure validation, then pass
// both values at EOF for reconciliation. Unary calls pass only response.
//
// This hook does not imply generated-code provenance. The typed completion
// package uses it with generated codecs; other low-level callers own any
// validator they attach.
func SetCompletionValidator(request *Request, validate func(*Response, *Completion) error) error {
	if request == nil {
		return errors.New("completion validator requires a request")
	}
	if request.StructuredOutput == nil {
		return errors.New("completion validator requires structured output")
	}
	if validate == nil {
		return errors.New("completion validator is required")
	}
	request.completionValidate = validate
	return nil
}

// accept validates and records one provider chunk.
func (v *streamValidator) accept(chunk Chunk) error {
	if err := v.preflightStreamChunk(chunk); err != nil {
		return err
	}
	owned, err := validateAndCloneChunk(chunk)
	if err != nil {
		return err
	}
	return v.acceptOwned(owned)
}

// acceptOwned validates one chunk whose mutable content belongs to the model
// stream boundary.
func (v *streamValidator) acceptOwned(chunk Chunk) error {
	if v.stopped {
		return fmt.Errorf("model stream emitted %q after stop", chunk.Kind())
	}
	switch actual := chunk.(type) {
	case TextChunk:
		if v.expectsCompletion {
			return errors.New("structured output stream emitted text instead of a completion")
		}
		for _, part := range actual.Message.Parts {
			switch text := part.(type) {
			case TextPart:
				v.text.WriteString(text.Text)
				v.textSeen = true
			case CitationsPart:
				v.text.WriteString(text.Text)
				v.textSeen = true
				v.citations = append(v.citations, snapshotCitationsPart(text))
			}
		}
	case ThinkingChunk:
		for _, part := range actual.Message.Parts {
			if thinking, ok := part.(ThinkingPart); ok {
				if prior, exists := v.thinking[thinking.Index]; exists && prior.final {
					return fmt.Errorf("model stream emitted thinking after final block %d", thinking.Index)
				}
				if _, exists := v.thinking[thinking.Index]; !exists {
					v.thinkingOrder = append(v.thinkingOrder, thinking.Index)
				}
				v.thinking[thinking.Index] = snapshotThinking(thinking)
			}
		}
	case ToolCallDeltaChunk:
		if v.expectsCompletion {
			return errors.New("structured output stream emitted a tool call delta")
		}
		if _, exists := v.toolValidators[actual.Delta.Name]; !exists {
			return fmt.Errorf(
				"model stream returned tool delta %q that was not present in its request",
				actual.Delta.Name,
			)
		}
		if _, finalized := v.finalToolCallIDs[actual.Delta.ID]; finalized {
			return fmt.Errorf("model stream emitted tool call delta after finalized call %q", actual.Delta.ID)
		}
		name := string(actual.Delta.Name)
		if prior := v.toolDeltaNames[actual.Delta.ID]; prior != "" && prior != name {
			return fmt.Errorf("model stream changed tool name for call %q", actual.Delta.ID)
		}
		v.toolDeltaNames[actual.Delta.ID] = name
		payload := v.toolDeltaPayloads[actual.Delta.ID]
		if payload == nil {
			payload = &strings.Builder{}
			v.toolDeltaPayloads[actual.Delta.ID] = payload
		}
		payload.WriteString(actual.Delta.Delta)
	case ToolCallChunk:
		if v.expectsCompletion {
			return errors.New("structured output stream emitted a tool call")
		}
		validate, exists := v.toolValidators[actual.ToolCall.Name]
		if !exists {
			return fmt.Errorf(
				"model stream returned tool %q that was not present in its request",
				actual.ToolCall.Name,
			)
		}
		if validate != nil {
			if err := validate(actual.ToolCall); err != nil {
				return err
			}
		}
		if _, exists := v.finalToolCallIDs[actual.ToolCall.ID]; exists {
			return fmt.Errorf("model stream repeated finalized tool call %q", actual.ToolCall.ID)
		}
		if name := v.toolDeltaNames[actual.ToolCall.ID]; name != "" && name != string(actual.ToolCall.Name) {
			return fmt.Errorf("model stream finalized tool call %q with a different name", actual.ToolCall.ID)
		}
		if payload, exists := v.toolDeltaPayloads[actual.ToolCall.ID]; exists &&
			!bytesEqualString(actual.ToolCall.Payload, payload.String()) {
			return fmt.Errorf(
				"model stream finalized tool call %q with a payload that differs from its deltas",
				actual.ToolCall.ID,
			)
		}
		v.finalToolCallIDs[actual.ToolCall.ID] = struct{}{}
		delete(v.toolDeltaNames, actual.ToolCall.ID)
		delete(v.toolDeltaPayloads, actual.ToolCall.ID)
		v.toolCalls = append(v.toolCalls, snapshotToolCall(actual.ToolCall))
	case UsageChunk:
		usage := actual.Usage
		v.stampUsageIdentity(&usage)
		accumulated, err := addStreamUsage(v.usage, usage)
		if err != nil {
			return err
		}
		v.usage = accumulated
		v.usageSeen = true
	case CompletionDeltaChunk:
		if !v.expectsCompletion {
			return errors.New("model stream emitted a completion delta without a structured output request")
		}
		if actual.Delta.Name != v.completionName {
			return fmt.Errorf(
				"stream completion delta %q does not match requested completion %q",
				actual.Delta.Name,
				v.completionName,
			)
		}
		if v.completion != nil {
			return errors.New("model stream emitted completion delta after final completion")
		}
	case CompletionChunk:
		if !v.expectsCompletion {
			return errors.New("model stream emitted a completion without a structured output request")
		}
		if actual.Completion.Name != v.completionName {
			return fmt.Errorf(
				"stream completion %q does not match requested completion %q",
				actual.Completion.Name,
				v.completionName,
			)
		}
		if v.completion != nil {
			return errors.New("model stream emitted multiple final completions")
		}
		if !json.Valid(actual.Completion.Payload) {
			return errors.New("model stream emitted invalid completion JSON")
		}
		if v.completionValidate != nil {
			completion := actual.Completion
			if err := v.completionValidate(nil, &completion); err != nil {
				return err
			}
		}
		completion := snapshotCompletion(actual.Completion)
		v.completion = &completion
	case StopChunk:
		for id := range v.toolDeltaNames {
			if _, finalized := v.finalToolCallIDs[id]; !finalized {
				return fmt.Errorf("model stream stopped before tool call %q was finalized", id)
			}
		}
		if v.expectsCompletion && v.completion == nil {
			return errors.New("structured output stream stopped before a completion")
		}
		v.stopped = true
		v.stopReason = actual.Reason
	}
	return nil
}

// validateAndCloneChunk transfers a provider chunk into framework ownership,
// then checks only the owned copy.
func validateAndCloneChunk(chunk Chunk) (Chunk, error) {
	owned, err := cloneChunk(chunk)
	if err != nil {
		return nil, err
	}
	if err := validateCanonicalChunk(owned); err != nil {
		return nil, fmt.Errorf("invalid model chunk: %w", err)
	}
	return owned, nil
}

// finish validates EOF and reconciles every recorded chunk with the complete
// provider response.
func (v *streamValidator) finish(response *Response) error {
	if v.expectsCompletion && v.completion == nil {
		return errors.New("structured output stream ended without a completion")
	}
	if !v.stopped {
		return errors.New("model stream ended without stop chunk")
	}
	if err := validateCanonicalResponse(response); err != nil {
		return fmt.Errorf("invalid canonical response: %w", err)
	}
	if v.stopReason != response.StopReason {
		return fmt.Errorf(
			"stream stop reason %q does not match canonical response %q",
			v.stopReason,
			response.StopReason,
		)
	}
	responseCalls := response.ToolCalls()
	if err := validateToolChoiceResponse(v.toolChoiceMode, v.toolChoiceName, response); err != nil {
		return err
	}
	if len(responseCalls) != len(v.toolCalls) {
		return fmt.Errorf(
			"stream emitted %d tool calls but canonical response contains %d",
			len(v.toolCalls),
			len(responseCalls),
		)
	}
	for index, responseCall := range responseCalls {
		streamCall := v.toolCalls[index]
		if snapshotToolCall(responseCall) != streamCall {
			return fmt.Errorf("stream tool call %d does not match canonical response", index)
		}
	}
	responseUsage := response.Usage
	v.stampUsageIdentity(&responseUsage)
	if v.usageSeen && v.usage != responseUsage {
		return errors.New("stream usage deltas do not match canonical response usage")
	}
	if v.textSeen && v.text.String() != responseText(response) {
		return errors.New("streamed text does not match canonical response")
	}
	if len(v.citations) > 0 && !reflect.DeepEqual(v.citations, snapshotResponseCitations(response)) {
		return errors.New("streamed citations do not match canonical response")
	}
	thinking := make([]thinkingSnapshot, 0, len(v.thinkingOrder))
	for _, index := range v.thinkingOrder {
		block := v.thinking[index]
		if !block.final {
			return fmt.Errorf("model stream ended before thinking block %d was final", index)
		}
		thinking = append(thinking, block)
	}
	if !slices.Equal(thinking, snapshotResponseThinking(response)) {
		return errors.New("streamed thinking does not match canonical response")
	}
	if v.completion != nil {
		if !json.Valid([]byte(v.completion.payload)) {
			return errors.New("model stream emitted invalid completion JSON")
		}
		if v.completionValidate != nil {
			completion := &Completion{
				Name:    v.completion.name,
				Payload: []byte(v.completion.payload),
			}
			if err := v.completionValidate(response, completion); err != nil {
				return err
			}
		} else if !bytes.Equal([]byte(v.completion.payload), []byte(responseText(response))) {
			return errors.New("stream completion does not match canonical response")
		}
	}
	return nil
}

// responseText returns assistant text in provider response order.
func responseText(response *Response) string {
	var text strings.Builder
	for _, message := range response.Content {
		for _, part := range message.Parts {
			switch actual := part.(type) {
			case TextPart:
				text.WriteString(actual.Text)
			case CitationsPart:
				text.WriteString(actual.Text)
			}
		}
	}
	return text.String()
}

// snapshotResponseThinking returns immutable reasoning metadata in response
// order.
func snapshotResponseThinking(response *Response) []thinkingSnapshot {
	var thinking []thinkingSnapshot
	for _, message := range response.Content {
		for _, part := range message.Parts {
			if actual, ok := part.(ThinkingPart); ok {
				thinking = append(thinking, snapshotThinking(actual))
			}
		}
	}
	return thinking
}

// snapshotResponseCitations returns immutable citation metadata in response
// order.
func snapshotResponseCitations(response *Response) []citationsPartSnapshot {
	var citations []citationsPartSnapshot
	for _, message := range response.Content {
		for _, part := range message.Parts {
			if actual, ok := part.(CitationsPart); ok {
				citations = append(citations, snapshotCitationsPart(actual))
			}
		}
	}
	return citations
}

// snapshotCitationsPart transfers citation slices and locations into private
// validation state.
func snapshotCitationsPart(part CitationsPart) citationsPartSnapshot {
	citations := make([]citationSnapshot, len(part.Citations))
	for index, citation := range part.Citations {
		snapshot := citationSnapshot{
			title:         citation.Title,
			source:        citation.Source,
			sourceContent: slices.Clone(citation.SourceContent),
		}
		if citation.Location.DocumentChar != nil {
			snapshot.documentChar = *citation.Location.DocumentChar
			snapshot.hasDocumentChar = true
		}
		if citation.Location.DocumentChunk != nil {
			snapshot.documentChunk = *citation.Location.DocumentChunk
			snapshot.hasDocumentChunk = true
		}
		if citation.Location.DocumentPage != nil {
			snapshot.documentPage = *citation.Location.DocumentPage
			snapshot.hasDocumentPage = true
		}
		citations[index] = snapshot
	}
	return citationsPartSnapshot{
		text:      part.Text,
		citations: citations,
	}
}

// snapshotThinking transfers mutable reasoning bytes into an immutable string.
func snapshotThinking(part ThinkingPart) thinkingSnapshot {
	return thinkingSnapshot{
		text:      part.Text,
		signature: part.Signature,
		redacted:  string(part.Redacted),
		index:     part.Index,
		final:     part.Final,
	}
}

// snapshotToolCall transfers tool JSON into an immutable string.
func snapshotToolCall(call ToolCall) toolCallSnapshot {
	return toolCallSnapshot{
		name:             call.Name,
		payload:          string(call.Payload),
		id:               call.ID,
		thoughtSignature: call.ThoughtSignature,
	}
}

// snapshotCompletion transfers completion JSON into an immutable string.
func snapshotCompletion(completion Completion) completionSnapshot {
	return completionSnapshot{
		name:    completion.Name,
		payload: string(completion.Payload),
	}
}

// addStreamUsage combines one usage delta while requiring one model identity
// for the entire invocation.
func addStreamUsage(current, delta TokenUsage) (TokenUsage, error) {
	if current.Model != "" && delta.Model != "" && current.Model != delta.Model {
		return TokenUsage{}, errors.New("model stream changed usage model")
	}
	if current.ModelClass != "" && delta.ModelClass != "" && current.ModelClass != delta.ModelClass {
		return TokenUsage{}, errors.New("model stream changed usage model class")
	}
	if current.Model == "" {
		current.Model = delta.Model
	}
	if current.ModelClass == "" {
		current.ModelClass = delta.ModelClass
	}
	input, err := addStreamTokenCount("input", current.InputTokens, delta.InputTokens)
	if err != nil {
		return TokenUsage{}, err
	}
	output, err := addStreamTokenCount("output", current.OutputTokens, delta.OutputTokens)
	if err != nil {
		return TokenUsage{}, err
	}
	total, err := addStreamTokenCount("total", current.TotalTokens, delta.TotalTokens)
	if err != nil {
		return TokenUsage{}, err
	}
	cacheRead, err := addStreamTokenCount("cache read", current.CacheReadTokens, delta.CacheReadTokens)
	if err != nil {
		return TokenUsage{}, err
	}
	cacheWrite, err := addStreamTokenCount("cache write", current.CacheWriteTokens, delta.CacheWriteTokens)
	if err != nil {
		return TokenUsage{}, err
	}
	return TokenUsage{
		Model:            current.Model,
		ModelClass:       current.ModelClass,
		InputTokens:      input,
		OutputTokens:     output,
		TotalTokens:      total,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}, nil
}

// addStreamTokenCount rejects overflow before a usage delta can wrap negative.
func addStreamTokenCount(name string, current, delta int) (int, error) {
	if current < 0 || delta < 0 {
		return 0, fmt.Errorf("model stream %s token usage cannot be negative", name)
	}
	sum := current + delta
	if sum < current {
		return 0, fmt.Errorf("model stream %s token usage exceeds the supported integer range", name)
	}
	return sum, nil
}

// stampUsageIdentity applies the logical model class from the immutable
// request contract. Missing provider model identity remains empty.
func (v *streamValidator) stampUsageIdentity(usage *TokenUsage) {
	if v.modelClass != "" {
		usage.ModelClass = v.modelClass
	}
}
