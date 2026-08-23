// Package model preflights provider-controlled responses and stream chunks
// before any framework-owned copy, fingerprint, or reconciliation state is
// allocated. One budget is shared by every value in a unary response and by
// every chunk plus the terminal response in a stream.
package model

import (
	"fmt"
	"reflect"
	"unicode/utf8"
)

// chargeString rejects text that a JSON transport would rewrite and then
// charges its original bytes to the invocation.
func chargeString(walk *dynamicValueWalk, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("string is not valid UTF-8")
	}
	return walk.addBytes(len(value))
}

// chargeJSON rejects raw JSON bytes with invalid UTF-8 before ownership copying.
func chargeJSON(walk *dynamicValueWalk, value []byte) error {
	if !utf8.Valid(value) {
		return fmt.Errorf("raw JSON is not valid UTF-8")
	}
	return walk.addBytes(len(value))
}

// preflightResponse checks that one complete provider response can be copied
// as rejected evidence without exceeding the supplied invocation-wide budget.
// Canonical shape is validated after ownership transfer; unsupported assistant
// part variants fail here before their binary fields are inspected.
func preflightResponse(
	response *Response,
	walk *dynamicValueWalk,
	contract dynamicCloneContract,
) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	if err := walk.checkChildren(len(response.Content)); err != nil {
		return err
	}
	for messageIndex := range response.Content {
		if err := preflightResponseMessage(&response.Content[messageIndex], walk, contract); err != nil {
			return fmt.Errorf("model: preflight response content %d: %w", messageIndex, err)
		}
	}
	if err := chargeString(walk, response.Usage.Model); err != nil {
		return err
	}
	if err := chargeString(walk, string(response.Usage.ModelClass)); err != nil {
		return err
	}
	return chargeString(walk, response.StopReason)
}

// preflightResponseMessage charges one assistant message and rejects part
// variants that cannot appear in a canonical model response.
func preflightResponseMessage(
	message *Message,
	walk *dynamicValueWalk,
	contract dynamicCloneContract,
) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, string(message.Role)); err != nil {
		return err
	}
	if err := walk.checkChildren(len(message.Parts)); err != nil {
		return err
	}
	for partIndex, part := range message.Parts {
		if err := preflightResponsePart(part, walk); err != nil {
			return fmt.Errorf("part %d: %w", partIndex, err)
		}
	}
	if message.Meta != nil {
		if err := preflightDynamicValueAt(
			reflect.ValueOf(message.Meta),
			0,
			walk,
			contract,
		); err != nil {
			return fmt.Errorf("metadata: %w", err)
		}
	}
	return nil
}

// preflightResponsePart charges one supported assistant response part. Image,
// document, tool-result, and cache-checkpoint parts are rejected from provider
// output before any mutable payload they carry can be copied.
func preflightResponsePart(part Part, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	switch actual := part.(type) {
	case TextPart:
		return chargeString(walk, actual.Text)
	case CitationsPart:
		if err := chargeString(walk, actual.Text); err != nil {
			return err
		}
		if err := walk.checkChildren(len(actual.Citations)); err != nil {
			return err
		}
		for index := range actual.Citations {
			if err := preflightCitation(&actual.Citations[index], walk); err != nil {
				return fmt.Errorf("citation %d: %w", index, err)
			}
		}
		return nil
	case ThinkingPart:
		if err := chargeString(walk, actual.Text); err != nil {
			return err
		}
		if err := chargeString(walk, actual.Signature); err != nil {
			return err
		}
		return walk.addBytes(len(actual.Redacted))
	case ToolUsePart:
		if err := chargeString(walk, actual.ID); err != nil {
			return err
		}
		if err := chargeString(walk, actual.Name); err != nil {
			return err
		}
		if err := chargeJSON(walk, actual.Input); err != nil {
			return err
		}
		return chargeString(walk, actual.ThoughtSignature)
	case nil:
		return nil
	default:
		return fmt.Errorf("unsupported assistant response part %T", part)
	}
}

// preflightCitation charges source identity and excerpts before citation slices
// are copied into response or reconciliation state.
func preflightCitation(citation *Citation, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, citation.Title); err != nil {
		return err
	}
	if err := chargeString(walk, citation.Source); err != nil {
		return err
	}
	if err := walk.checkChildren(len(citation.SourceContent)); err != nil {
		return err
	}
	for _, content := range citation.SourceContent {
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, content); err != nil {
			return err
		}
	}
	return nil
}

// preflightChunk charges one provider stream event into the stream-wide budget
// before cloning any payload or growing reconciliation accumulators.
func preflightChunk(chunk Chunk, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	switch actual := chunk.(type) {
	case TextChunk:
		return preflightChunkMessage(&actual.Message, walk, false)
	case ThinkingChunk:
		return preflightChunkMessage(&actual.Message, walk, true)
	case ToolCallChunk:
		if err := chargeString(walk, string(actual.ToolCall.Name)); err != nil {
			return err
		}
		if err := chargeJSON(walk, actual.ToolCall.Payload); err != nil {
			return err
		}
		if err := chargeString(walk, actual.ToolCall.ID); err != nil {
			return err
		}
		return chargeString(walk, actual.ToolCall.ThoughtSignature)
	case ToolCallDeltaChunk:
		if err := chargeString(walk, string(actual.Delta.Name)); err != nil {
			return err
		}
		if err := chargeString(walk, actual.Delta.ID); err != nil {
			return err
		}
		return chargeString(walk, actual.Delta.Delta)
	case CompletionChunk:
		if err := chargeString(walk, actual.Completion.Name); err != nil {
			return err
		}
		return chargeJSON(walk, actual.Completion.Payload)
	case CompletionDeltaChunk:
		if err := chargeString(walk, actual.Delta.Name); err != nil {
			return err
		}
		return chargeString(walk, actual.Delta.Delta)
	case UsageChunk:
		if err := chargeString(walk, actual.Usage.Model); err != nil {
			return err
		}
		return chargeString(walk, string(actual.Usage.ModelClass))
	case StopChunk:
		return chargeString(walk, actual.Reason)
	case nil:
		return nil
	default:
		return fmt.Errorf("model: unsupported stream chunk %T", chunk)
	}
}

// preflightStreamChunk charges one chunk into this stream's shared budget. A
// finalized tool call is independently bounded, then its name, ID, and payload
// are exempted only when they exactly repeat the deltas already retained for
// that call.
func (v *streamValidator) preflightStreamChunk(chunk Chunk) error {
	actual, isToolCall := chunk.(ToolCallChunk)
	if !isToolCall {
		return preflightChunk(chunk, &v.budget)
	}
	payload, hasPayload := v.toolDeltaPayloads[actual.ToolCall.ID]
	name := v.toolDeltaNames[actual.ToolCall.ID]
	if !hasPayload ||
		name != string(actual.ToolCall.Name) ||
		!bytesEqualString(actual.ToolCall.Payload, payload.String()) {
		return preflightChunk(chunk, &v.budget)
	}
	if err := preflightChunk(chunk, &dynamicValueWalk{}); err != nil {
		return err
	}
	if err := v.budget.visit(); err != nil {
		return err
	}
	return chargeString(&v.budget, actual.ToolCall.ThoughtSignature)
}

// preflightChunkMessage charges the message wrapper, metadata, and the exact
// part variants allowed by a text or thinking stream chunk.
func preflightChunkMessage(message *Message, walk *dynamicValueWalk, thinking bool) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, string(message.Role)); err != nil {
		return err
	}
	if err := walk.checkChildren(len(message.Parts)); err != nil {
		return err
	}
	for index, part := range message.Parts {
		if err := walk.visit(); err != nil {
			return err
		}
		switch actual := part.(type) {
		case TextPart:
			if thinking {
				return fmt.Errorf("model: thinking chunk part %d has type %T", index, part)
			}
			if err := chargeString(walk, actual.Text); err != nil {
				return err
			}
		case CitationsPart:
			if thinking {
				return fmt.Errorf("model: thinking chunk part %d has type %T", index, part)
			}
			if err := chargeString(walk, actual.Text); err != nil {
				return err
			}
			if err := walk.checkChildren(len(actual.Citations)); err != nil {
				return err
			}
			for citationIndex := range actual.Citations {
				if err := preflightCitation(&actual.Citations[citationIndex], walk); err != nil {
					return err
				}
			}
		case ThinkingPart:
			if !thinking {
				return fmt.Errorf("model: text chunk part %d has type %T", index, part)
			}
			if err := chargeString(walk, actual.Text); err != nil {
				return err
			}
			if err := chargeString(walk, actual.Signature); err != nil {
				return err
			}
			if err := walk.addBytes(len(actual.Redacted)); err != nil {
				return err
			}
		default:
			if thinking {
				return fmt.Errorf("model: thinking chunk part %d has type %T", index, part)
			}
			return fmt.Errorf("model: text chunk part %d has type %T", index, part)
		}
	}
	if message.Meta != nil {
		if err := preflightDynamicValueAt(
			reflect.ValueOf(message.Meta),
			0,
			walk,
			dynamicCloneCanonical,
		); err != nil {
			return fmt.Errorf("model: stream chunk metadata: %w", err)
		}
	}
	return nil
}

// preflightTerminalResponse charges final-response data that is not an exact
// restatement of accepted chunks. The final response is independently
// preflighted first, so these comparisons only decide which already-bounded
// bytes are duplicates; final wrappers, metadata, and mismatched data consume
// the remaining shared stream budget before ownership copying.
func (v *streamValidator) preflightTerminalResponse(response *Response) error {
	walk := &v.budget
	if err := walk.visit(); err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	if err := walk.checkChildren(len(response.Content)); err != nil {
		return err
	}
	textRepeated := v.textSeen && responseTextEquals(response, v.text.String())
	completionRepeated := v.completion != nil &&
		responseTextEquals(response, v.completion.payload)
	citationIndex := 0
	thinkingIndex := 0
	toolIndex := 0
	for messageIndex := range response.Content {
		message := &response.Content[messageIndex]
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, string(message.Role)); err != nil {
			return err
		}
		if err := walk.checkChildren(len(message.Parts)); err != nil {
			return err
		}
		for partIndex, part := range message.Parts {
			if err := walk.visit(); err != nil {
				return err
			}
			switch actual := part.(type) {
			case TextPart:
				if !textRepeated && !completionRepeated {
					if err := chargeString(walk, actual.Text); err != nil {
						return err
					}
				}
			case CitationsPart:
				if !textRepeated && !completionRepeated {
					if err := chargeString(walk, actual.Text); err != nil {
						return err
					}
				}
				repeated := citationIndex < len(v.citations) &&
					citationsPartMatchesSnapshot(actual, v.citations[citationIndex])
				citationIndex++
				if repeated {
					continue
				}
				if err := walk.checkChildren(len(actual.Citations)); err != nil {
					return err
				}
				for index := range actual.Citations {
					if err := preflightCitation(&actual.Citations[index], walk); err != nil {
						return fmt.Errorf("citation %d: %w", index, err)
					}
				}
			case ThinkingPart:
				repeated := thinkingIndex < len(v.thinkingOrder) &&
					thinkingPartMatchesSnapshot(actual, v.thinking[v.thinkingOrder[thinkingIndex]])
				thinkingIndex++
				if repeated {
					continue
				}
				if err := chargeString(walk, actual.Text); err != nil {
					return err
				}
				if err := chargeString(walk, actual.Signature); err != nil {
					return err
				}
				if err := walk.addBytes(len(actual.Redacted)); err != nil {
					return err
				}
			case ToolUsePart:
				repeated := toolIndex < len(v.toolCalls) &&
					toolUsePartMatchesSnapshot(actual, v.toolCalls[toolIndex])
				toolIndex++
				if repeated {
					continue
				}
				if err := chargeString(walk, actual.ID); err != nil {
					return err
				}
				if err := chargeString(walk, actual.Name); err != nil {
					return err
				}
				if err := chargeJSON(walk, actual.Input); err != nil {
					return err
				}
				if err := chargeString(walk, actual.ThoughtSignature); err != nil {
					return err
				}
			case nil:
			default:
				return fmt.Errorf(
					"model: preflight response content %d part %d: unsupported assistant response part %T",
					messageIndex,
					partIndex,
					part,
				)
			}
		}
		if message.Meta != nil {
			if err := preflightDynamicValueAt(
				reflect.ValueOf(message.Meta),
				0,
				walk,
				dynamicCloneEvidence,
			); err != nil {
				return fmt.Errorf("model: preflight response content %d metadata: %w", messageIndex, err)
			}
		}
	}
	responseUsage := response.Usage
	v.stampUsageIdentity(&responseUsage)
	if !v.usageSeen || responseUsage != v.usage {
		if err := chargeString(walk, response.Usage.Model); err != nil {
			return err
		}
		if err := chargeString(walk, string(response.Usage.ModelClass)); err != nil {
			return err
		}
	}
	if !v.stopped || response.StopReason != v.stopReason {
		return chargeString(walk, response.StopReason)
	}
	return nil
}

// responseTextEquals compares ordered response text without building a second
// aggregate string.
func responseTextEquals(response *Response, expected string) bool {
	offset := 0
	for _, message := range response.Content {
		for _, part := range message.Parts {
			var text string
			switch actual := part.(type) {
			case TextPart:
				text = actual.Text
			case CitationsPart:
				text = actual.Text
			default:
				continue
			}
			if len(text) > len(expected)-offset || expected[offset:offset+len(text)] != text {
				return false
			}
			offset += len(text)
		}
	}
	return offset == len(expected)
}

// citationsPartMatchesSnapshot reports whether the final citation part exactly
// repeats the immutable details accepted from stream chunks.
func citationsPartMatchesSnapshot(part CitationsPart, snapshot citationsPartSnapshot) bool {
	if part.Text != snapshot.text || len(part.Citations) != len(snapshot.citations) {
		return false
	}
	for index, citation := range part.Citations {
		if !citationMatchesSnapshot(citation, snapshot.citations[index]) {
			return false
		}
	}
	return true
}

// citationMatchesSnapshot compares one citation without copying its source
// content or location pointers.
func citationMatchesSnapshot(citation Citation, snapshot citationSnapshot) bool {
	if citation.Title != snapshot.title ||
		citation.Source != snapshot.source ||
		len(citation.SourceContent) != len(snapshot.sourceContent) {
		return false
	}
	for index, content := range citation.SourceContent {
		if content != snapshot.sourceContent[index] {
			return false
		}
	}
	if (citation.Location.DocumentChar != nil) != snapshot.hasDocumentChar ||
		(citation.Location.DocumentChunk != nil) != snapshot.hasDocumentChunk ||
		(citation.Location.DocumentPage != nil) != snapshot.hasDocumentPage {
		return false
	}
	if snapshot.hasDocumentChar && *citation.Location.DocumentChar != snapshot.documentChar {
		return false
	}
	if snapshot.hasDocumentChunk && *citation.Location.DocumentChunk != snapshot.documentChunk {
		return false
	}
	return !snapshot.hasDocumentPage || *citation.Location.DocumentPage == snapshot.documentPage
}

// thinkingPartMatchesSnapshot compares one final reasoning part with the
// immutable chunk state without converting its redacted bytes to a string.
func thinkingPartMatchesSnapshot(part ThinkingPart, snapshot thinkingSnapshot) bool {
	return part.Text == snapshot.text &&
		part.Signature == snapshot.signature &&
		bytesEqualString(part.Redacted, snapshot.redacted) &&
		part.Index == snapshot.index &&
		part.Final == snapshot.final
}

// toolUsePartMatchesSnapshot compares one final tool call with the immutable
// chunk state without converting its raw JSON to a string.
func toolUsePartMatchesSnapshot(part ToolUsePart, snapshot toolCallSnapshot) bool {
	return part.ID == snapshot.id &&
		part.Name == string(snapshot.name) &&
		bytesEqualString(part.Input, snapshot.payload) &&
		part.ThoughtSignature == snapshot.thoughtSignature
}

// bytesEqualString compares raw provider bytes with an immutable snapshot
// without allocating a conversion.
func bytesEqualString(value []byte, snapshot string) bool {
	if len(value) != len(snapshot) {
		return false
	}
	for index, current := range value {
		if current != snapshot[index] {
			return false
		}
	}
	return true
}

// validateCanonicalDynamicValue checks canonical JSON shape without copying.
// Evidence preflight has already bounded the map-key allocation and recursion.
func validateCanonicalDynamicValue(value reflect.Value) error {
	return validateCanonicalDynamicValueAt(value, 0, make(map[dynamicContainer]struct{}))
}

// validateCanonicalDynamicValueAt rejects values transports would otherwise
// coerce or rewrite, including structs and malformed UTF-8 strings.
func validateCanonicalDynamicValueAt(
	value reflect.Value,
	depth int,
	active map[dynamicContainer]struct{},
) error {
	if depth > maxDynamicValueDepth {
		return fmt.Errorf("dynamic value exceeds maximum depth %d", maxDynamicValueDepth)
	}
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateCanonicalDynamicValueAt(value.Elem(), depth, active)
	}
	container, tracked := dynamicContainerIdentity(value)
	if tracked {
		if _, exists := active[container]; exists {
			return dynamicCycleError(value)
		}
		active[container] = struct{}{}
		defer delete(active, container)
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("map key type %s is not a string", value.Type().Key())
		}
		keys := sortedStringMapKeys(value)
		for _, key := range keys {
			if !utf8.ValidString(key.String()) {
				return fmt.Errorf("map key is not valid UTF-8")
			}
			if err := validateCanonicalDynamicValueAt(value.MapIndex(key), depth+1, active); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateCanonicalDynamicValueAt(value.Index(index), depth+1, active); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("string is not valid UTF-8")
		}
		return nil
	case reflect.Float32, reflect.Float64:
		return validateCanonicalFloat(value)
	case reflect.Bool,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64:
		return nil
	case reflect.Invalid,
		reflect.Interface,
		reflect.Struct,
		reflect.Uintptr,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.Pointer,
		reflect.UnsafePointer:
		return fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
	}
	panic("unreachable canonical dynamic value kind")
}
