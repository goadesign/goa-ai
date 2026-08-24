// Package model owns cloning of provider responses captured across the runtime
// boundary. Clones preserve concrete metadata container types while isolating
// every mutable slice and map from planner code.
package model

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"unicode/utf8"
)

type dynamicCloneContract uint8

const (
	dynamicCloneEvidence dynamicCloneContract = iota
	dynamicCloneCanonical
)

// CloneResponse returns a deep copy of response suitable for isolated
// transcript ownership. Metadata must contain JSON-compatible scalar, slice,
// array, or string-keyed map values.
func CloneResponse(response *Response) (*Response, error) {
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneCanonical); err != nil {
		return nil, err
	}
	if err := validateResponseDynamicValues(response); err != nil {
		return nil, err
	}
	return cloneResponseUnchecked(response)
}

// cloneResponseUnchecked copies a response that has already passed complete
// response preflight.
func cloneResponseUnchecked(response *Response) (*Response, error) {
	if response == nil {
		return nil, nil
	}
	out := *response
	out.Content = slices.Clone(response.Content)
	for i := range response.Content {
		message, err := cloneMessage(response.Content[i])
		if err != nil {
			return nil, fmt.Errorf("model: clone response content %d: %w", i, err)
		}
		out.Content[i] = message
	}
	return &out, nil
}

// ownResponse copies a provider response before validation and assigns an
// unforgeable in-memory origin to each message. Later clones preserve those
// origins so runtime selection never relies on message content equality.
func ownResponse(response *Response) (*Response, error) {
	owned, err := cloneResponseForValidation(response)
	if err != nil {
		return nil, err
	}
	assignMessageOrigins(owned)
	return owned, nil
}

// ownPreflightedResponse copies a provider response already charged to its
// unary or stream-wide budget.
func ownPreflightedResponse(response *Response) (*Response, error) {
	owned, err := cloneResponseForValidationUnchecked(response)
	if err != nil {
		return nil, err
	}
	assignMessageOrigins(owned)
	return owned, nil
}

// SameMessageOrigin reports whether two framework-owned messages came from the
// same exact provider message. Messages without framework origins never match.
func SameMessageOrigin(left, right *Message) bool {
	return left != nil &&
		right != nil &&
		left.origin != nil &&
		left.origin == right.origin
}

// cloneResponseForValidation owns a complete provider response before strict
// validation so malformed parts and metadata remain available as evidence.
func cloneResponseForValidation(response *Response) (*Response, error) {
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return nil, err
	}
	return cloneResponseForValidationUnchecked(response)
}

// cloneResponseForValidationUnchecked copies evidence after response-wide
// preflight has bounded every allocation.
func cloneResponseForValidationUnchecked(response *Response) (*Response, error) {
	if response == nil {
		return nil, nil
	}
	out := *response
	out.Content = slices.Clone(response.Content)
	for messageIndex, message := range response.Content {
		cloned := message
		cloned.Parts = slices.Clone(message.Parts)
		for partIndex, part := range message.Parts {
			if part == nil {
				continue
			}
			value, err := clonePart(part)
			if err != nil {
				return nil, fmt.Errorf(
					"model: clone rejected response content %d part %d: %w",
					messageIndex,
					partIndex,
					err,
				)
			}
			cloned.Parts[partIndex] = value
		}
		if message.Meta != nil {
			meta, err := cloneRejectedMetadata(message.Meta)
			if err != nil {
				return nil, fmt.Errorf(
					"model: clone rejected response content %d metadata: %w",
					messageIndex,
					err,
				)
			}
			cloned.Meta = meta
		}
		out.Content[messageIndex] = cloned
	}
	return &out, nil
}

// assignMessageOrigins gives each newly owned provider message a distinct
// identity while preserving identities already assigned by an inner boundary.
func assignMessageOrigins(response *Response) {
	if response == nil {
		return
	}
	for index := range response.Content {
		if response.Content[index].origin == nil {
			response.Content[index].origin = &messageOrigin{}
		}
	}
}

// CloneMessages returns a deep copy of canonical messages for transfer across
// planner, workflow, and reminder ownership boundaries. Nil messages and
// non-JSON-compatible metadata or tool results are rejected.
func CloneMessages(messages []*Message) ([]*Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	out := make([]*Message, len(messages))
	for i, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("model: clone messages[%d]: message is nil", i)
		}
		cloned, err := cloneMessage(*message)
		if err != nil {
			return nil, fmt.Errorf("model: clone messages[%d]: %w", i, err)
		}
		out[i] = &cloned
	}
	return out, nil
}

// cloneRequest owns every mutable value that providers or observers can read
// during one call. Function-backed generated validators are immutable and are
// copied by reference.
func cloneRequest(request *Request) (*Request, error) {
	if err := preflightRequest(request); err != nil {
		return nil, err
	}
	messages, err := CloneMessages(request.Messages)
	if err != nil {
		return nil, fmt.Errorf("clone model request messages: %w", err)
	}
	owned := *request
	owned.PromptRefs = slices.Clone(request.PromptRefs)
	owned.Messages = messages
	owned.Tools = make([]*ToolDefinition, len(request.Tools))
	for i, definition := range request.Tools {
		if definition == nil {
			continue
		}
		cloned := *definition
		cloned.Input = ToolInput{
			jsonSchema:               slices.Clone(definition.Input.jsonSchema),
			schemaWithoutRootExample: slices.Clone(definition.Input.schemaWithoutRootExample),
			exampleJSON:              slices.Clone(definition.Input.exampleJSON),
			validate:                 definition.Input.validate,
		}
		owned.Tools[i] = &cloned
	}
	if request.ToolChoice != nil {
		value := *request.ToolChoice
		owned.ToolChoice = &value
	}
	if request.Thinking != nil {
		value := *request.Thinking
		owned.Thinking = &value
	}
	if request.StructuredOutput != nil {
		value := *request.StructuredOutput
		value.Schema = slices.Clone(request.StructuredOutput.Schema)
		value.SchemaWithoutRootExample = slices.Clone(request.StructuredOutput.SchemaWithoutRootExample)
		value.ExampleJSON = slices.Clone(request.StructuredOutput.ExampleJSON)
		owned.StructuredOutput = &value
	}
	if request.Cache != nil {
		value := *request.Cache
		owned.Cache = &value
	}
	return &owned, nil
}

// cloneChunk returns a deep copy of one provider stream chunk. Stream
// boundaries call it before retaining or returning chunks so provider buffer
// reuse cannot change accepted payloads.
func cloneChunk(chunk Chunk) (Chunk, error) {
	switch actual := chunk.(type) {
	case TextChunk:
		message, err := cloneMessage(actual.Message)
		if err != nil {
			return nil, fmt.Errorf("model: clone text chunk: %w", err)
		}
		actual.Message = message
		return actual, nil
	case ThinkingChunk:
		message, err := cloneMessage(actual.Message)
		if err != nil {
			return nil, fmt.Errorf("model: clone thinking chunk: %w", err)
		}
		actual.Message = message
		return actual, nil
	case ToolCallChunk:
		actual.ToolCall.Payload = slices.Clone(actual.ToolCall.Payload)
		return actual, nil
	case ToolCallDeltaChunk:
		return actual, nil
	case CompletionChunk:
		actual.Completion.Payload = slices.Clone(actual.Completion.Payload)
		return actual, nil
	case CompletionDeltaChunk:
		return actual, nil
	case UsageChunk:
		return actual, nil
	case StopChunk:
		return actual, nil
	case nil:
		return nil, errors.New("model: clone chunk: chunk is nil")
	}
	return nil, fmt.Errorf("model: clone chunk: unsupported chunk type %T", chunk)
}

func cloneMessage(message Message) (Message, error) {
	out := message
	out.Parts = slices.Clone(message.Parts)
	for i, part := range message.Parts {
		cloned, err := clonePart(part)
		if err != nil {
			return Message{}, fmt.Errorf("part %d: %w", i, err)
		}
		out.Parts[i] = cloned
	}
	if message.Meta != nil {
		meta, err := cloneMetadata(message.Meta)
		if err != nil {
			return Message{}, err
		}
		out.Meta = meta
	}
	return out, nil
}

func clonePart(part Part) (Part, error) {
	switch actual := part.(type) {
	case TextPart:
		return actual, nil
	case ImagePart:
		actual.Bytes = slices.Clone(actual.Bytes)
		return actual, nil
	case DocumentPart:
		actual.Bytes = slices.Clone(actual.Bytes)
		actual.Chunks = slices.Clone(actual.Chunks)
		return actual, nil
	case CitationsPart:
		citations := actual.Citations
		actual.Citations = slices.Clone(citations)
		for i, citation := range citations {
			citation.SourceContent = slices.Clone(citation.SourceContent)
			citation.Location = cloneCitationLocation(citation.Location)
			actual.Citations[i] = citation
		}
		return actual, nil
	case ThinkingPart:
		actual.Redacted = slices.Clone(actual.Redacted)
		return actual, nil
	case ToolUsePart:
		actual.Input = slices.Clone(actual.Input)
		return actual, nil
	case ToolResultPart:
		content, err := cloneMetadataValue(reflect.ValueOf(actual.Content))
		if err != nil {
			return nil, fmt.Errorf("tool result %q content: %w", actual.ToolUseID, err)
		}
		if content.IsValid() {
			actual.Content = content.Interface()
		} else {
			actual.Content = nil
		}
		return actual, nil
	case CacheCheckpointPart:
		return actual, nil
	case nil:
		return nil, fmt.Errorf("part is nil")
	}
	return nil, fmt.Errorf("unsupported message part type %T", part)
}

func cloneCitationLocation(location CitationLocation) CitationLocation {
	out := location
	if location.DocumentChar != nil {
		value := *location.DocumentChar
		out.DocumentChar = &value
	}
	if location.DocumentChunk != nil {
		value := *location.DocumentChunk
		out.DocumentChunk = &value
	}
	if location.DocumentPage != nil {
		value := *location.DocumentPage
		out.DocumentPage = &value
	}
	return out
}

func cloneMetadata(metadata map[string]any) (map[string]any, error) {
	return cloneMetadataWithContract(metadata, dynamicCloneCanonical)
}

// cloneRejectedMetadata copies ordinary metadata plus bounded structs so
// validation failures can reach observers as owned evidence. Pointers remain
// unsupported because their targets cannot be copied safely.
func cloneRejectedMetadata(metadata map[string]any) (map[string]any, error) {
	return cloneMetadataWithContract(metadata, dynamicCloneEvidence)
}

// cloneMetadataWithContract copies the complete metadata map under one shared
// depth, visit, byte, and cycle budget.
func cloneMetadataWithContract(
	metadata map[string]any,
	contract dynamicCloneContract,
) (map[string]any, error) {
	cloned, err := cloneDynamicValueAt(reflect.ValueOf(metadata), 0, &dynamicValueWalk{}, contract)
	if err != nil {
		return nil, err
	}
	return cloned.Interface().(map[string]any), nil
}

// cloneDynamicValueAt copies one metadata or tool-result value while enforcing
// shared recursion and work limits. The contract selects whether non-finite
// numbers remain available as rejected evidence or fail canonical copying.
func cloneDynamicValueAt(
	value reflect.Value,
	depth int,
	walk *dynamicValueWalk,
	contract dynamicCloneContract,
) (reflect.Value, error) {
	if !value.IsValid() {
		if _, _, err := walk.enter(value, depth); err != nil {
			return reflect.Value{}, err
		}
		return reflect.Value{}, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if _, _, err := walk.enter(value, depth); err != nil {
			return reflect.Value{}, err
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneDynamicValueAt(value.Elem(), depth, walk, contract)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloned)
		return out, nil
	case reflect.Map:
		container, tracked, err := walk.enter(value, depth)
		if err != nil {
			return reflect.Value{}, err
		}
		defer walk.leave(container, tracked)
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("map key type %s is not a string", value.Type().Key())
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if err := walk.checkChildren(value.Len()); err != nil {
			return reflect.Value{}, err
		}
		keys := sortedStringMapKeys(value)
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		for _, key := range keys {
			if err := walk.addBytes(len(key.String())); err != nil {
				return reflect.Value{}, err
			}
			if contract == dynamicCloneCanonical && !utf8.ValidString(key.String()) {
				return reflect.Value{}, fmt.Errorf("map key is not valid UTF-8")
			}
			cloned, err := cloneDynamicValueAt(value.MapIndex(key), depth+1, walk, contract)
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(key, cloned)
		}
		return out, nil
	case reflect.Slice:
		container, tracked, err := walk.enter(value, depth)
		if err != nil {
			return reflect.Value{}, err
		}
		defer walk.leave(container, tracked)
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			reflect.Copy(out, value)
			return out, nil
		}
		if err := walk.checkChildren(value.Len()); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned, err := cloneDynamicValueAt(value.Index(index), depth+1, walk, contract)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(index).Set(cloned)
		}
		return out, nil
	case reflect.Array:
		container, tracked, err := walk.enter(value, depth)
		if err != nil {
			return reflect.Value{}, err
		}
		defer walk.leave(container, tracked)
		if err := walk.checkChildren(value.Len()); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned, err := cloneDynamicValueAt(value.Index(index), depth+1, walk, contract)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(index).Set(cloned)
		}
		return out, nil
	case reflect.Struct:
		if contract == dynamicCloneCanonical {
			return reflect.Value{}, fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
		}
		container, tracked, err := walk.enter(value, depth)
		if err != nil {
			return reflect.Value{}, err
		}
		defer walk.leave(container, tracked)
		valueType := value.Type()
		descriptorBytes := len(valueType.PkgPath()) + len(valueType.Name())
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			descriptorBytes += len(field.Name) + len(field.Tag)
		}
		if err := walk.addBytes(descriptorBytes); err != nil {
			return reflect.Value{}, err
		}
		if err := walk.checkChildren(value.NumField()); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		for _, index := range sortedStructFieldIndexes(valueType) {
			if !out.Field(index).CanSet() {
				return reflect.Value{}, fmt.Errorf("struct field %s is not exported", value.Type().Field(index).Name)
			}
			cloned, err := cloneDynamicValueAt(value.Field(index), depth+1, walk, contract)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Field(index).Set(cloned)
		}
		return out, nil
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
		if _, _, err := walk.enter(value, depth); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	case reflect.String:
		if _, _, err := walk.enter(value, depth); err != nil {
			return reflect.Value{}, err
		}
		if contract == dynamicCloneCanonical && !utf8.ValidString(value.String()) {
			return reflect.Value{}, fmt.Errorf("string is not valid UTF-8")
		}
		return value, nil
	case reflect.Float32, reflect.Float64:
		if _, _, err := walk.enter(value, depth); err != nil {
			return reflect.Value{}, err
		}
		if contract == dynamicCloneCanonical {
			if err := validateCanonicalFloat(value); err != nil {
				return reflect.Value{}, err
			}
		}
		return value, nil
	case reflect.Invalid,
		reflect.Uintptr,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.Pointer,
		reflect.UnsafePointer:
		if contract == dynamicCloneCanonical {
			return reflect.Value{}, fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
		}
		return reflect.Value{}, fmt.Errorf("value type %s cannot be copied safely", value.Type())
	}
	panic("unreachable dynamic value kind")
}

// cloneMetadataValue recursively copies JSON-compatible metadata while
// preserving named slice and map types used by provider adapters.
func cloneMetadataValue(value reflect.Value) (reflect.Value, error) {
	return cloneDynamicValueAt(value, 0, &dynamicValueWalk{}, dynamicCloneCanonical)
}

// validateResponseDynamicValues applies canonical metadata rules without
// copying response content.
func validateResponseDynamicValues(response *Response) error {
	if response == nil {
		return nil
	}
	for index := range response.Content {
		if err := validateCanonicalDynamicValue(reflect.ValueOf(response.Content[index].Meta)); err != nil {
			return fmt.Errorf("model: response content %d metadata: %w", index, err)
		}
	}
	return nil
}
