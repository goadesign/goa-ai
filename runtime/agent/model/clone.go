// Package model owns cloning of provider responses captured across the runtime
// boundary. Clones preserve concrete metadata container types while isolating
// every mutable slice and map from planner code.
package model

import (
	"fmt"
	"math"
	"reflect"
)

// CloneResponse returns a deep copy of response suitable for isolated
// transcript ownership. Metadata must contain JSON-compatible scalar, slice,
// array, or string-keyed map values.
func CloneResponse(response *Response) (*Response, error) {
	if response == nil {
		return nil, nil
	}
	out := *response
	out.Content = make([]Message, len(response.Content))
	for i := range response.Content {
		message, err := cloneMessage(response.Content[i])
		if err != nil {
			return nil, fmt.Errorf("model: clone response content %d: %w", i, err)
		}
		out.Content[i] = message
	}
	return &out, nil
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

func cloneMessage(message Message) (Message, error) {
	out := message
	out.Parts = make([]Part, len(message.Parts))
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
		actual.Bytes = append([]byte(nil), actual.Bytes...)
		return actual, nil
	case DocumentPart:
		actual.Bytes = append([]byte(nil), actual.Bytes...)
		actual.Chunks = append([]string(nil), actual.Chunks...)
		return actual, nil
	case CitationsPart:
		citations := actual.Citations
		actual.Citations = make([]Citation, len(citations))
		for i, citation := range citations {
			citation.SourceContent = append([]string(nil), citation.SourceContent...)
			citation.Location = cloneCitationLocation(citation.Location)
			actual.Citations[i] = citation
		}
		return actual, nil
	case ThinkingPart:
		actual.Redacted = append([]byte(nil), actual.Redacted...)
		return actual, nil
	case ToolUsePart:
		actual.Input = append(actual.Input[:0:0], actual.Input...)
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
	panic("unreachable message part")
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
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned, err := cloneMetadataValue(reflect.ValueOf(value))
		if err != nil {
			return nil, fmt.Errorf("metadata %q: %w", key, err)
		}
		if cloned.IsValid() {
			out[key] = cloned.Interface()
		} else {
			out[key] = nil
		}
	}
	return out, nil
}

// cloneMetadataValue recursively copies JSON-compatible metadata while
// preserving named slice and map types used by provider adapters.
func cloneMetadataValue(value reflect.Value) (reflect.Value, error) {
	if !value.IsValid() {
		return reflect.Value{}, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneMetadataValue(value.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloned)
		return out, nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("map key type %s is not a string", value.Type().Key())
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned, err := cloneMetadataValue(iter.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(iter.Key(), cloned)
		}
		return out, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned, err := cloneMetadataValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(cloned)
		}
		return out, nil
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned, err := cloneMetadataValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(cloned)
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
		reflect.Uint64,
		reflect.String:
		return value, nil
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return reflect.Value{}, fmt.Errorf("value %v is not a finite JSON number", number)
		}
		return value, nil
	case reflect.Invalid,
		reflect.Uintptr,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.Pointer,
		reflect.Struct,
		reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
	}
	panic("unreachable metadata kind")
}
