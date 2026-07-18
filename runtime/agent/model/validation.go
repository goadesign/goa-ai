// Package model owns the canonical provider-neutral response contract. These
// validators enforce that provider adapters return complete, replayable model
// values before planners or the runtime consume them.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/tools"
)

// ValidateResponse verifies a completed model response is canonical and safe
// for transcript replay.
func ValidateResponse(response *Response) error {
	if response == nil {
		return errors.New("model: response is nil")
	}
	if len(response.Content) == 0 {
		return errors.New("model: response has no assistant content")
	}
	for index := range response.Content {
		if err := validateResponseMessage(&response.Content[index]); err != nil {
			return fmt.Errorf("model: response content %d: %w", index, err)
		}
	}
	if err := validateTokenUsage(response.Usage); err != nil {
		return err
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
			if err := validateToolCall(&call); err != nil {
				return fmt.Errorf("model: response content %d part %d: %w", messageIndex, partIndex, err)
			}
			if _, ok := seen[call.ID]; ok {
				return fmt.Errorf("model: response content %d part %d: duplicate tool call ID %q", messageIndex, partIndex, call.ID)
			}
			seen[call.ID] = struct{}{}
		}
	}
	if response.StopReason == "" {
		return errors.New("model: response is missing its stop reason")
	}
	return nil
}

// ValidateChunk verifies one model presentation event follows the canonical
// union contract.
func ValidateChunk(chunk Chunk) error {
	switch actual := chunk.(type) {
	case TextChunk:
		return validateChunkMessage(&actual.Message, false)
	case ThinkingChunk:
		return validateChunkMessage(&actual.Message, true)
	case ToolCallChunk:
		return validateToolCall(&actual.ToolCall)
	case ToolCallDeltaChunk:
		if actual.Delta.Name == "" {
			return errors.New("model: tool-call delta is missing its name")
		}
		if actual.Delta.ID == "" {
			return errors.New("model: tool-call delta is missing its ID")
		}
		if actual.Delta.Delta == "" {
			return errors.New("model: tool-call delta is empty")
		}
	case CompletionChunk:
		if actual.Completion.Name == "" {
			return errors.New("model: completion is missing its name")
		}
		if !json.Valid(actual.Completion.Payload) {
			return errors.New("model: completion payload is not valid JSON")
		}
	case CompletionDeltaChunk:
		if actual.Delta.Name == "" {
			return errors.New("model: completion delta is missing its name")
		}
		if actual.Delta.Delta == "" {
			return errors.New("model: completion delta is empty")
		}
	case UsageChunk:
		return validateTokenUsage(actual.Usage)
	case StopChunk:
		if actual.Reason == "" {
			return errors.New("model: stop chunk is missing its reason")
		}
	case nil:
		return errors.New("model: stream chunk is nil")
	default:
		return fmt.Errorf("model: unsupported stream chunk %T", chunk)
	}
	return nil
}

func validateResponseMessage(message *Message) error {
	if message.Role != ConversationRoleAssistant {
		return fmt.Errorf("message role must be assistant, got %q", message.Role)
	}
	if len(message.Parts) == 0 {
		return errors.New("assistant response message has no parts")
	}
	if _, err := cloneMetadata(message.Meta); err != nil {
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
	// Opus 4.8-class thinking display "omitted" emits signature-only blocks
	// whose empty text must be preserved for verbatim replay), or redacted
	// bytes. Redacted content is exclusive of both text and signature.
	content := part.Text != "" || part.Signature != ""
	redacted := len(part.Redacted) > 0
	if content == redacted {
		return errors.New("thinking must contain exactly signed/plaintext or redacted content")
	}
	return nil
}

func validateToolCall(call *ToolCall) error {
	if call == nil {
		return errors.New("model: tool-call chunk is missing its call")
	}
	if call.ID == "" {
		return errors.New("tool call is missing its ID")
	}
	if call.Name == "" {
		return fmt.Errorf("tool call %q is missing its name", call.ID)
	}
	if !json.Valid(call.Payload) {
		return fmt.Errorf("tool call %q payload is not valid JSON", call.ID)
	}
	if data := bytes.TrimSpace(call.Payload); len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("tool call %q payload must be a JSON object", call.ID)
	}
	return nil
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
	switch usage.ModelClass {
	case "", ModelClassDefault, ModelClassHighReasoning, ModelClassSmall:
	default:
		return fmt.Errorf("model: token usage has unsupported model class %q", usage.ModelClass)
	}
	return nil
}
