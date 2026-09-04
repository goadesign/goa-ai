// Package model owns the canonical provider-neutral response contract. These
// validators enforce that provider adapters return complete, replayable model
// values before planners or the runtime consume them.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/tools"
)

const maxTokenUsageModelBytes = 512

// ValidateResponse verifies a completed model response is canonical and safe
// for transcript replay.
func ValidateResponse(response *Response) error {
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneCanonical); err != nil {
		return err
	}
	return validateCanonicalResponse(response)
}

// validateCanonicalResponse checks a response that has already passed complete
// allocation preflight.
func validateCanonicalResponse(response *Response) error {
	_, err := validateCanonicalResponseOutput(response)
	return err
}

// validateCanonicalResponseOutput checks one completed response and reports
// which closed output boundary rejected it.
func validateCanonicalResponseOutput(response *Response) (OutputValidationKind, error) {
	if response == nil {
		return OutputValidationResponseShape, errors.New("model: response is nil")
	}
	if len(response.Content) == 0 {
		return OutputValidationResponseShape, errors.New("model: response has no assistant content")
	}
	for index := range response.Content {
		if err := validateResponseMessage(&response.Content[index]); err != nil {
			return OutputValidationResponseShape, fmt.Errorf("model: response content %d: %w", index, err)
		}
	}
	if err := validateTokenUsage(response.Usage); err != nil {
		return OutputValidationUsage, err
	}
	seen := make(map[string]struct{})
	for messageIndex := range response.Content {
		for partIndex, part := range response.Content[messageIndex].Parts {
			use, ok := part.(ToolUsePart)
			if !ok {
				continue
			}
			payload := use.Input
			call := ToolCall{
				Name:             tools.Ident(use.Name),
				Payload:          payload,
				ID:               use.ID,
				ThoughtSignature: use.ThoughtSignature,
			}
			if kind, err := validateToolCallOutput(&call); err != nil {
				return kind, fmt.Errorf("model: response content %d part %d: %w", messageIndex, partIndex, err)
			}
			if _, ok := seen[call.ID]; ok {
				return OutputValidationToolIdentity, fmt.Errorf(
					"model: response content %d part %d: duplicate tool call ID %q",
					messageIndex,
					partIndex,
					call.ID,
				)
			}
			seen[call.ID] = struct{}{}
		}
	}
	if response.StopReason == "" {
		return OutputValidationResponseShape, errors.New("model: response is missing its stop reason")
	}
	return "", nil
}

// ValidateChunk verifies one model presentation event follows the canonical
// union contract.
func ValidateChunk(chunk Chunk) error {
	if err := preflightChunk(chunk, &dynamicValueWalk{}); err != nil {
		return err
	}
	return validateCanonicalChunk(chunk)
}

// validateCanonicalChunk checks one chunk already charged to a unary or
// stream-wide preflight budget.
func validateCanonicalChunk(chunk Chunk) error {
	_, err := validateCanonicalChunkOutput(chunk)
	return err
}

// validateCanonicalChunkOutput checks one stream event and reports the closed
// output boundary that rejected it.
func validateCanonicalChunkOutput(chunk Chunk) (OutputValidationKind, error) {
	switch actual := chunk.(type) {
	case TextChunk:
		return OutputValidationResponseShape, validateChunkMessage(&actual.Message, false)
	case ThinkingChunk:
		return OutputValidationResponseShape, validateChunkMessage(&actual.Message, true)
	case ToolCallChunk:
		return validateToolCallOutput(&actual.ToolCall)
	case ToolCallDeltaChunk:
		if actual.Delta.Name == "" {
			return OutputValidationToolIdentity, errors.New("model: tool-call delta is missing its name")
		}
		if actual.Delta.ID == "" {
			return OutputValidationToolIdentity, errors.New("model: tool-call delta is missing its ID")
		}
		if actual.Delta.Delta == "" {
			return OutputValidationToolArguments, errors.New("model: tool-call delta is empty")
		}
	case CompletionChunk:
		if actual.Completion.Name == "" {
			return OutputValidationStructuredOutput, errors.New("model: completion is missing its name")
		}
		if !json.Valid(actual.Completion.Payload) {
			return OutputValidationStructuredOutput, errors.New("model: completion payload is not valid JSON")
		}
	case CompletionDeltaChunk:
		if actual.Delta.Name == "" {
			return OutputValidationStructuredOutput, errors.New("model: completion delta is missing its name")
		}
		if actual.Delta.Delta == "" {
			return OutputValidationStructuredOutput, errors.New("model: completion delta is empty")
		}
	case UsageChunk:
		return OutputValidationUsage, validateTokenUsage(actual.Usage)
	case StopChunk:
		if actual.Reason == "" {
			return OutputValidationResponseShape, errors.New("model: stop chunk is missing its reason")
		}
	case nil:
		return OutputValidationResponseShape, errors.New("model: stream chunk is nil")
	default:
		return OutputValidationResponseShape, fmt.Errorf("model: unsupported stream chunk %T", chunk)
	}
	return "", nil
}

func validateResponseMessage(message *Message) error {
	if message.Role != ConversationRoleAssistant {
		return fmt.Errorf("message role must be assistant, got %q", message.Role)
	}
	if len(message.Parts) == 0 {
		return errors.New("assistant response message has no parts")
	}
	if err := validateCanonicalDynamicValue(reflect.ValueOf(message.Meta)); err != nil {
		return fmt.Errorf("message metadata: %w", err)
	}
	if _, err := MarshalMetadata(message.Meta); err != nil {
		return fmt.Errorf("message metadata is not valid JSON: %w", err)
	}
	for index, part := range message.Parts {
		switch actual := part.(type) {
		case TextPart:
			if actual.Text == "" {
				return fmt.Errorf("part %d: text is empty", index)
			}
		case CitationsPart:
			if err := ValidateCitationsPart(actual); err != nil {
				return fmt.Errorf("part %d: %w", index, err)
			}
		case ToolUsePart:
		case ThinkingPart:
			if !actual.Final {
				return fmt.Errorf("part %d: completed response contains draft thinking", index)
			}
			if err := validateThinkingPart(actual); err != nil {
				return fmt.Errorf("part %d: %w", index, err)
			}
		default:
			return fmt.Errorf("part %d: unsupported assistant response part %T", index, part)
		}
	}
	return nil
}

func validateChunkMessage(message *Message, thinking bool) error {
	if message == nil {
		return errors.New("model: content chunk is missing its message")
	}
	if message.Role != ConversationRoleAssistant {
		return fmt.Errorf("model: stream message role must be assistant, got %q", message.Role)
	}
	if len(message.Parts) == 0 {
		return errors.New("model: content chunk message has no parts")
	}
	for index, part := range message.Parts {
		if thinking {
			actual, ok := part.(ThinkingPart)
			if !ok {
				return fmt.Errorf("model: thinking chunk part %d has type %T", index, part)
			}
			if actual.Final {
				if err := validateThinkingPart(actual); err != nil {
					return fmt.Errorf("model: thinking chunk part %d: %w", index, err)
				}
			} else if actual.Text == "" || actual.Signature != "" || len(actual.Redacted) > 0 {
				return fmt.Errorf("model: thinking chunk part %d is not a plaintext draft", index)
			}
			continue
		}
		switch actual := part.(type) {
		case TextPart:
			if actual.Text == "" {
				return fmt.Errorf("model: text chunk part %d is empty", index)
			}
		case CitationsPart:
			if err := ValidateCitationsPart(actual); err != nil {
				return fmt.Errorf("model: text chunk part %d: %w", index, err)
			}
		default:
			return fmt.Errorf("model: text chunk part %d has type %T", index, part)
		}
	}
	return nil
}

func validateThinkingPart(part ThinkingPart) error {
	// Valid variants: signed or plaintext reasoning (text and/or signature —
	// Opus 4.8-class thinking output "omitted" emits signature-only blocks
	// whose empty text must be preserved for verbatim replay), or redacted
	// bytes. Redacted content is exclusive of both text and signature.
	content := part.Text != "" || part.Signature != ""
	redacted := len(part.Redacted) > 0
	if content == redacted {
		return errors.New("thinking must contain exactly signed/plaintext or redacted content")
	}
	return nil
}

// validateToolCallOutput distinguishes call identity from argument failures
// without inspecting an error string.
func validateToolCallOutput(call *ToolCall) (OutputValidationKind, error) {
	if call == nil {
		return OutputValidationToolIdentity, errors.New("model: tool-call chunk is missing its call")
	}
	if call.ID == "" {
		return OutputValidationToolIdentity, errors.New("tool call is missing its ID")
	}
	if call.Name == "" {
		return OutputValidationToolIdentity, fmt.Errorf("tool call %q is missing its name", call.ID)
	}
	if !json.Valid(call.Payload) {
		return OutputValidationToolArguments, NewMalformedToolArgumentsError(
			fmt.Errorf("tool call %q payload is not valid JSON", call.ID),
		)
	}
	return "", nil
}

// ValidateCitationsPart verifies that a canonical citation block has generated
// text, source attribution, and at most one provider-neutral location variant.
func ValidateCitationsPart(part CitationsPart) error {
	if part.Text == "" {
		return errors.New("citation text is empty")
	}
	if len(part.Citations) == 0 {
		return errors.New("citation list is empty")
	}
	for index, citation := range part.Citations {
		locations := 0
		if citation.Location.DocumentChar != nil {
			locations++
		}
		if citation.Location.DocumentChunk != nil {
			locations++
		}
		if citation.Location.DocumentPage != nil {
			locations++
		}
		if locations > 1 {
			return fmt.Errorf("citation %d has multiple locations", index)
		}
		if citation.Title == "" && citation.Source == "" && len(citation.SourceContent) == 0 && locations == 0 {
			return fmt.Errorf("citation %d has no source identity or location", index)
		}
	}
	return nil
}

func validateTokenUsage(usage TokenUsage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 ||
		usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 {
		return errors.New("model: token usage cannot be negative")
	}
	if err := validateTokenUsageModel(usage.Model); err != nil {
		return err
	}
	switch usage.ModelClass {
	case "", ModelClassDefault, ModelClassHighReasoning, ModelClassSmall:
	default:
		return fmt.Errorf("model: token usage has unsupported model class %q", usage.ModelClass)
	}
	return nil
}

// validateTokenUsageModel checks the provider model identifier before it
// enters usage records or token-count results.
func validateTokenUsageModel(model string) error {
	if len(model) > maxTokenUsageModelBytes {
		return fmt.Errorf("model: token usage model exceeds %d bytes", maxTokenUsageModelBytes)
	}
	if !utf8.ValidString(model) {
		return errors.New("model: token usage model is not valid UTF-8")
	}
	return nil
}
