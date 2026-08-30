// Package startrecipe defines the byte-level workflow start identity shared by
// workflow engine adapters. It converts values with the same Temporal payload
// encoders used at execution boundaries, then hashes framed payload bytes so
// adapter-specific map or JSON behavior cannot change duplicate detection.
package startrecipe

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"sort"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

const (
	// MemoKey stores the recipe digest on Temporal executions. Callers cannot
	// supply this key because the engine owns duplicate validation.
	MemoKey = "goa_ai_engine_start_recipe_v1"

	recipeVersion = "goa-ai-engine-start-recipe-v1"
)

type (
	// strictJSONPayloadConverter preserves integer precision and rejects
	// unknown fields when workflow values are decoded.
	strictJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
	}

	// RunInputSnapshot contains one decoded engine-owned input and the exact
	// boundary payload from which it was decoded.
	RunInputSnapshot struct {
		// Input is the engine-owned run input.
		Input *api.RunInput
		// Payload is the exact encoded input included in the recipe digest.
		Payload *commonpb.Payload
	}

	// SearchAttribute contains one visibility value after the same type
	// normalization and payload encoding used by Temporal's typed API.
	SearchAttribute struct {
		// Name is the visibility field name.
		Name string
		// Value is the normalized value supplied to Temporal's typed key.
		Value any
		// ValueType is the Temporal visibility type recorded in payload metadata.
		ValueType enumspb.IndexedValueType
		// Payload is the native Temporal representation included in the digest.
		Payload *commonpb.Payload
	}

	// DigestInput contains the exact values submitted to an engine.
	DigestInput struct {
		// Workflow is the registered workflow name the engine executes.
		Workflow string
		// TaskQueue is the queue in the caller's request, including empty.
		TaskQueue string
		// InputPayload is the accepted RunInput after boundary conversion.
		InputPayload *commonpb.Payload
		// RunTimeout is the timeout in the caller's request.
		RunTimeout time.Duration
		// RetryPolicy controls workflow retries.
		RetryPolicy engine.RetryPolicy
		// Memo contains caller-owned native workflow memo values.
		Memo map[string]any
		// SearchAttributes contains normalized and encoded visibility values.
		SearchAttributes []SearchAttribute
	}
)

// NewDataConverter returns the common native payload conversion used for
// workflow inputs, memo values, and recipe hashing in every engine adapter.
func NewDataConverter() converter.DataConverter {
	return converter.NewCompositeDataConverter(
		converter.NewNilPayloadConverter(),
		converter.NewByteSlicePayloadConverter(),
		converter.NewProtoPayloadConverter(),
		converter.NewProtoJSONPayloadConverter(),
		&strictJSONPayloadConverter{
			JSONPayloadConverter: converter.NewJSONPayloadConverter(),
		},
	)
}

// FromPayload decodes canonical JSON without losing integer precision,
// accepting unknown fields, or ignoring trailing bytes.
func (c *strictJSONPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if payload == nil {
		return fmt.Errorf("temporal: payload is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload.Data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(valuePtr); err != nil {
		return fmt.Errorf("temporal: decode canonical JSON payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("temporal: canonical JSON payload has trailing data")
	}
	return nil
}

// SnapshotRunInput round-trips a run input through the workflow data converter.
// The returned value no longer aliases caller-owned maps, slices, or pointers,
// and the returned payload is the exact input representation used by Digest.
func SnapshotRunInput(dataConverter converter.DataConverter, input *api.RunInput) (RunInputSnapshot, error) {
	payload, err := dataConverter.ToPayload(input)
	if err != nil {
		return RunInputSnapshot{}, fmt.Errorf("encode workflow start input: %w", err)
	}
	var snapshot *api.RunInput
	if err := dataConverter.FromPayload(payload, &snapshot); err != nil {
		return RunInputSnapshot{}, fmt.Errorf("decode workflow start input snapshot: %w", err)
	}
	return RunInputSnapshot{Input: snapshot, Payload: payload}, nil
}

// EncodeSearchAttributes applies the engine's Temporal visibility type mapping
// and Temporal's native payload converter to each value independently. Entries
// are returned in key order for both start-option construction and hashing.
func EncodeSearchAttributes(attributes map[string]any) ([]SearchAttribute, error) {
	names := sortedKeys(attributes)
	encoded := make([]SearchAttribute, 0, len(names))
	for _, name := range names {
		value, valueType, err := normalizeSearchAttribute(name, attributes[name])
		if err != nil {
			return nil, err
		}
		payload, err := converter.GetDefaultDataConverter().ToPayload(value)
		if err != nil {
			return nil, fmt.Errorf("encode search attribute %q: %w", name, err)
		}
		if payload.GetData() != nil {
			if payload.Metadata == nil {
				payload.Metadata = make(map[string][]byte)
			}
			payload.Metadata["type"] = []byte(valueType.String())
		}
		encoded = append(encoded, SearchAttribute{
			Name: name, Value: value, ValueType: valueType, Payload: payload,
		})
	}
	return encoded, nil
}

// Digest hashes one accepted start recipe. Every value is framed with lengths,
// and every payload includes sorted metadata plus data bytes, so distinct field
// layouts and native payload types cannot produce the same byte stream.
func Digest(dataConverter converter.DataConverter, input DigestInput) ([sha256.Size]byte, error) {
	digest := sha256.New()
	writeBytes(digest, []byte(recipeVersion))
	if err := writeValue(digest, dataConverter, "workflow", input.Workflow); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := writeValue(digest, dataConverter, "task_queue", input.TaskQueue); err != nil {
		return [sha256.Size]byte{}, err
	}
	writePayload(digest, "input", input.InputPayload)
	if err := writeValue(digest, dataConverter, "run_timeout", input.RunTimeout); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := writeValue(digest, dataConverter, "retry_policy", input.RetryPolicy); err != nil {
		return [sha256.Size]byte{}, err
	}
	for _, name := range sortedKeys(input.Memo) {
		payload, err := dataConverter.ToPayload(input.Memo[name])
		if err != nil {
			return [sha256.Size]byte{}, fmt.Errorf("encode workflow memo %q: %w", name, err)
		}
		writeEntry(digest, "memo", name, payload)
	}
	searchAttributes := append([]SearchAttribute(nil), input.SearchAttributes...)
	sort.Slice(searchAttributes, func(i, j int) bool {
		return searchAttributes[i].Name < searchAttributes[j].Name
	})
	for _, attribute := range searchAttributes {
		writeEntry(digest, "search_attribute", attribute.Name, attribute.Payload)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

// normalizeSearchAttribute maps generic engine values onto Temporal's typed
// visibility values before either the start request or digest sees them.
func normalizeSearchAttribute(name string, value any) (any, enumspb.IndexedValueType, error) {
	switch typed := value.(type) {
	case nil:
		return nil, 0, fmt.Errorf("temporal engine: search attribute %q has nil value", name)
	case string:
		return typed, enumspb.INDEXED_VALUE_TYPE_KEYWORD, nil
	case bool:
		return typed, enumspb.INDEXED_VALUE_TYPE_BOOL, nil
	case int:
		return int64(typed), enumspb.INDEXED_VALUE_TYPE_INT, nil
	case int8:
		return int64(typed), enumspb.INDEXED_VALUE_TYPE_INT, nil
	case int16:
		return int64(typed), enumspb.INDEXED_VALUE_TYPE_INT, nil
	case int32:
		return int64(typed), enumspb.INDEXED_VALUE_TYPE_INT, nil
	case int64:
		return typed, enumspb.INDEXED_VALUE_TYPE_INT, nil
	case float32:
		return float64(typed), enumspb.INDEXED_VALUE_TYPE_DOUBLE, nil
	case float64:
		return typed, enumspb.INDEXED_VALUE_TYPE_DOUBLE, nil
	case time.Time:
		return typed, enumspb.INDEXED_VALUE_TYPE_DATETIME, nil
	case []string:
		return append([]string(nil), typed...), enumspb.INDEXED_VALUE_TYPE_KEYWORD_LIST, nil
	default:
		return nil, 0, fmt.Errorf("temporal engine: search attribute %q has unsupported type %T", name, value)
	}
}

// writeValue converts one scalar or struct through the workflow boundary before
// adding its complete payload representation to the digest.
func writeValue(digest hash.Hash, dataConverter converter.DataConverter, name string, value any) error {
	payload, err := dataConverter.ToPayload(value)
	if err != nil {
		return fmt.Errorf("encode workflow start field %q: %w", name, err)
	}
	writePayload(digest, name, payload)
	return nil
}

// writeEntry distinguishes a map section, key, and encoded value.
func writeEntry(digest hash.Hash, section, name string, payload *commonpb.Payload) {
	writeBytes(digest, []byte(section))
	writeBytes(digest, []byte(name))
	writePayload(digest, "value", payload)
}

// writePayload includes the field name, sorted metadata entries, and payload
// bytes as separately length-prefixed segments.
func writePayload(digest hash.Hash, name string, payload *commonpb.Payload) {
	writeBytes(digest, []byte(name))
	if payload == nil {
		writeUint64(digest, 0)
		return
	}
	writeUint64(digest, 1)
	metadataNames := sortedKeys(payload.Metadata)
	writeUint64(digest, uint64(len(metadataNames)))
	for _, metadataName := range metadataNames {
		writeBytes(digest, []byte(metadataName))
		writeBytes(digest, payload.Metadata[metadataName])
	}
	writeBytes(digest, payload.Data)
}

// writeBytes writes one unambiguous length-prefixed segment.
func writeBytes(digest hash.Hash, value []byte) {
	writeUint64(digest, uint64(len(value)))
	if _, err := digest.Write(value); err != nil {
		panic(fmt.Sprintf("write workflow recipe digest: %v", err))
	}
}

// writeUint64 writes a framing length to the digest.
func writeUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	if _, err := digest.Write(encoded[:]); err != nil {
		panic(fmt.Sprintf("write workflow recipe frame: %v", err))
	}
}

// sortedKeys returns stable key order without retaining caller maps.
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
