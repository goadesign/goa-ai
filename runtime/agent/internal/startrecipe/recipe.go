// Package startrecipe snapshots workflow starts and defines their byte-level
// identity. The runtime and every engine adapter use the same type-preserving
// conversion, so accepted input cannot change after validation and duplicate
// detection cannot vary by adapter.
package startrecipe

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sort"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

const (
	// MemoKey stores the recipe digest on Temporal executions. Callers cannot
	// supply this key because the engine owns duplicate validation.
	MemoKey = "goa_ai_engine_start_recipe_v1"

	recipeVersion = "goa-ai-engine-start-recipe-v1"
)

type (
	// runInputSnapshot contains one decoded engine-owned input and the exact
	// boundary payload from which it was decoded.
	runInputSnapshot struct {
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

	// digestInput contains the exact values submitted to an engine.
	digestInput struct {
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
		// Memo contains the exact encoded workflow memo values.
		Memo map[string]engine.EncodedValue
		// SearchAttributes contains normalized and encoded visibility values.
		SearchAttributes []SearchAttribute
	}

	// RequestSnapshot contains one immutable workflow start request and every
	// encoded value derived from it. Engines execute Request.Input and use Digest
	// and SearchAttributes without encoding the caller's values again.
	RequestSnapshot struct {
		// Request is the copied workflow start request accepted by the engine.
		Request engine.WorkflowStartRequest
		// InputPayload is the exact encoded form of Request.Input.
		InputPayload *commonpb.Payload
		// SearchAttributes are normalized for engine visibility APIs.
		SearchAttributes []SearchAttribute
		// Digest identifies the complete immutable request.
		Digest [sha256.Size]byte
	}

	// ChildRequestSnapshot contains one immutable child workflow request and its
	// final encoded input. Child starts do not carry the root recipe digest memo.
	ChildRequestSnapshot struct {
		// Request is the copied child workflow request accepted by the engine.
		Request engine.ChildWorkflowRequest
		// InputPayload is the exact encoded form of Request.Input.
		InputPayload *commonpb.Payload
	}
)

// EncodeMemo converts caller values into the portable representation every
// workflow engine receives. Each returned value owns its metadata and data.
func EncodeMemo(values map[string]any) (map[string]engine.EncodedValue, error) {
	if len(values) == 0 {
		return nil, nil
	}
	sourceBudget := new(workflowcodec.Budget)
	for name, value := range values {
		if name == "" {
			return nil, errors.New("workflow memo name is required")
		}
		if err := sourceBudget.AddText(name); err != nil {
			return nil, fmt.Errorf("validate workflow memo name %q: %w", name, err)
		}
		if err := sourceBudget.AddSource(value); err != nil {
			return nil, fmt.Errorf("validate workflow memo %q: %w", name, err)
		}
	}
	names := sortedKeys(values)
	dataConverter := workflowcodec.NewDataConverter()
	budget := new(workflowcodec.Budget)
	encoded := make(map[string]engine.EncodedValue, len(values))
	for _, name := range names {
		if err := budget.AddText(name); err != nil {
			return nil, fmt.Errorf("validate workflow memo %q: %w", name, err)
		}
		value := values[name]
		payload, err := dataConverter.ToPayload(value)
		if err != nil {
			return nil, fmt.Errorf("encode workflow memo %q: %w", name, err)
		}
		if err := budget.AddPayload(payload); err != nil {
			return nil, fmt.Errorf("validate workflow memo %q: %w", name, err)
		}
		encoded[name] = encodedValue(payload)
	}
	return encoded, nil
}

// MemoPayload returns an independent converter payload for one encoded memo
// value. Hashing, prepared storage, and Temporal submission share this form.
func MemoPayload(value engine.EncodedValue) *commonpb.Payload {
	return &commonpb.Payload{
		Metadata: clonePayloadMetadata(value.Metadata),
		Data:     append([]byte(nil), value.Data...),
	}
}

// snapshotRunInput round-trips a run input through the workflow data converter.
// The returned value no longer aliases caller-owned maps, slices, or pointers,
// and the returned payload is the exact input representation used by digest.
func snapshotRunInput(dataConverter converter.DataConverter, input *api.RunInput) (runInputSnapshot, error) {
	callerPayload, err := dataConverter.ToPayload(input)
	if err != nil {
		return runInputSnapshot{}, fmt.Errorf("encode workflow start input: %w", err)
	}
	var snapshot *api.RunInput
	if err := dataConverter.FromPayload(callerPayload, &snapshot); err != nil {
		return runInputSnapshot{}, fmt.Errorf("decode workflow start input snapshot: %w", err)
	}
	finalPayload, err := dataConverter.ToPayload(snapshot)
	if err != nil {
		return runInputSnapshot{}, fmt.Errorf("encode normalized workflow start input: %w", err)
	}
	return runInputSnapshot{Input: snapshot, Payload: finalPayload}, nil
}

// SnapshotRequest copies and encodes one complete workflow start request. It
// validates the combined input, memo, and search payload bytes before an engine
// records the request or compares its exact identity.
func SnapshotRequest(request engine.WorkflowStartRequest) (RequestSnapshot, error) {
	if err := engine.ValidateWorkflowStartRequest(request); err != nil {
		return RequestSnapshot{}, fmt.Errorf("validate workflow start request: %w", err)
	}
	dataConverter := workflowcodec.NewDataConverter()
	budget := new(workflowcodec.Budget)
	if err := budget.AddText(request.ID, request.Workflow, request.TaskQueue); err != nil {
		return RequestSnapshot{}, fmt.Errorf("validate workflow start text: %w", err)
	}
	if err := reserveRootRecipeMemo(dataConverter, budget); err != nil {
		return RequestSnapshot{}, err
	}
	input, err := snapshotRunInput(dataConverter, request.Input)
	if err != nil {
		return RequestSnapshot{}, err
	}
	if err := budget.AddPayload(input.Payload); err != nil {
		return RequestSnapshot{}, fmt.Errorf("validate workflow start input: %w", err)
	}
	searchAttributes, err := EncodeSearchAttributes(request.SearchAttributes)
	if err != nil {
		return RequestSnapshot{}, err
	}

	var ownedMemo map[string]engine.EncodedValue
	if len(request.Memo) > 0 {
		ownedMemo = make(map[string]engine.EncodedValue, len(request.Memo))
	}
	for _, name := range sortedKeys(request.Memo) {
		if name == "" {
			return RequestSnapshot{}, errors.New("workflow memo name is required")
		}
		if err := budget.AddText(name); err != nil {
			return RequestSnapshot{}, fmt.Errorf("validate workflow memo name %q: %w", name, err)
		}
		payload := memoPayloadView(request.Memo[name])
		if err := budget.AddPayload(payload); err != nil {
			return RequestSnapshot{}, fmt.Errorf("validate workflow memo %q: %w", name, err)
		}
		ownedMemo[name] = encodedValue(payload)
	}
	for _, attribute := range searchAttributes {
		if err := budget.AddText(attribute.Name); err != nil {
			return RequestSnapshot{}, fmt.Errorf("validate search attribute name %q: %w", attribute.Name, err)
		}
		if err := budget.AddPayload(attribute.Payload); err != nil {
			return RequestSnapshot{}, fmt.Errorf("validate search attribute %q: %w", attribute.Name, err)
		}
	}

	ownedRequest := request
	ownedRequest.Input = input.Input
	ownedRequest.Memo = ownedMemo
	ownedRequest.SearchAttributes = searchAttributeValues(searchAttributes)
	recipeDigest, err := digest(dataConverter, digestInput{
		Workflow:         ownedRequest.Workflow,
		TaskQueue:        ownedRequest.TaskQueue,
		InputPayload:     input.Payload,
		RunTimeout:       ownedRequest.RunTimeout,
		RetryPolicy:      ownedRequest.RetryPolicy,
		Memo:             ownedRequest.Memo,
		SearchAttributes: searchAttributes,
	})
	if err != nil {
		return RequestSnapshot{}, err
	}
	return RequestSnapshot{
		Request:          ownedRequest,
		InputPayload:     input.Payload,
		SearchAttributes: searchAttributes,
		Digest:           recipeDigest,
	}, nil
}

// SnapshotChildRequest copies and encodes one child workflow request. It uses
// the same text, input normalization, ownership, and byte rules as a root
// request without reserving the root-only recipe digest memo.
func SnapshotChildRequest(request engine.ChildWorkflowRequest) (ChildRequestSnapshot, error) {
	if err := engine.ValidateChildWorkflowRequest(request); err != nil {
		return ChildRequestSnapshot{}, fmt.Errorf("validate child workflow request: %w", err)
	}
	dataConverter := workflowcodec.NewDataConverter()
	budget := new(workflowcodec.Budget)
	if err := budget.AddText(request.ID, request.Workflow, request.TaskQueue); err != nil {
		return ChildRequestSnapshot{}, fmt.Errorf("validate child workflow text: %w", err)
	}
	input, err := snapshotRunInput(dataConverter, request.Input)
	if err != nil {
		return ChildRequestSnapshot{}, err
	}
	if err := budget.AddPayload(input.Payload); err != nil {
		return ChildRequestSnapshot{}, fmt.Errorf("validate child workflow input: %w", err)
	}
	ownedRequest := request
	ownedRequest.Input = input.Input
	return ChildRequestSnapshot{Request: ownedRequest, InputPayload: input.Payload}, nil
}

// EncodeSearchAttributes applies the engine's Temporal visibility type mapping
// and Temporal's native payload converter to each value independently. Entries
// are returned in key order for both start-option construction and hashing.
func EncodeSearchAttributes(attributes map[string]any) ([]SearchAttribute, error) {
	if len(attributes) == 0 {
		return nil, nil
	}
	if err := preflightSearchAttributes(attributes); err != nil {
		return nil, err
	}
	names := sortedKeys(attributes)
	budget := new(workflowcodec.Budget)
	encoded := make([]SearchAttribute, 0, len(names))
	for _, name := range names {
		if err := budget.AddText(name); err != nil {
			return nil, fmt.Errorf("validate search attribute name %q: %w", name, err)
		}
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
		if err := budget.AddPayload(payload); err != nil {
			return nil, fmt.Errorf("validate search attribute %q: %w", name, err)
		}
		encoded = append(encoded, SearchAttribute{
			Name: name, Value: value, ValueType: valueType, Payload: payload,
		})
	}
	return encoded, nil
}

// preflightSearchAttributes checks the complete source collection before the
// first value is encoded. Time values use the same text that JSON encoding
// writes; every other supported type can use the shared source walker.
func preflightSearchAttributes(attributes map[string]any) error {
	budget := new(workflowcodec.Budget)
	for name, value := range attributes {
		if name == "" {
			return errors.New("workflow search attribute name is required")
		}
		if err := budget.AddText(name); err != nil {
			return fmt.Errorf("validate workflow search attribute name %q: %w", name, err)
		}
		normalized, _, err := normalizeSearchAttribute(name, value)
		if err != nil {
			return err
		}
		if timestamp, ok := normalized.(time.Time); ok {
			err = budget.AddText(timestamp.Format(time.RFC3339Nano))
		} else {
			err = budget.AddSource(normalized)
		}
		if err != nil {
			return fmt.Errorf("validate workflow search attribute %q: %w", name, err)
		}
	}
	return nil
}

// reserveRootRecipeMemo counts the memo entry that official root adapters add
// after the request digest has been computed.
func reserveRootRecipeMemo(dataConverter converter.DataConverter, budget *workflowcodec.Budget) error {
	if err := budget.AddText(MemoKey); err != nil {
		return fmt.Errorf("validate workflow recipe memo name: %w", err)
	}
	payload, err := dataConverter.ToPayload(make([]byte, sha256.Size))
	if err != nil {
		return fmt.Errorf("encode workflow recipe digest: %w", err)
	}
	if err := budget.AddPayload(payload); err != nil {
		return fmt.Errorf("validate workflow recipe digest: %w", err)
	}
	return nil
}

// digest hashes one accepted start recipe. Every value is framed with lengths,
// and every payload includes sorted metadata plus data bytes, so distinct field
// layouts and native payload types cannot produce the same byte stream.
func digest(dataConverter converter.DataConverter, input digestInput) ([sha256.Size]byte, error) {
	hashValue := sha256.New()
	writeBytes(hashValue, []byte(recipeVersion))
	if err := writeValue(hashValue, dataConverter, "workflow", input.Workflow); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := writeValue(hashValue, dataConverter, "task_queue", input.TaskQueue); err != nil {
		return [sha256.Size]byte{}, err
	}
	writePayload(hashValue, "input", input.InputPayload)
	if err := writeValue(hashValue, dataConverter, "run_timeout", input.RunTimeout); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := writeValue(hashValue, dataConverter, "retry_policy", input.RetryPolicy); err != nil {
		return [sha256.Size]byte{}, err
	}
	for _, name := range sortedKeys(input.Memo) {
		writeEntry(hashValue, "memo", name, MemoPayload(input.Memo[name]))
	}
	searchAttributes := append([]SearchAttribute(nil), input.SearchAttributes...)
	sort.Slice(searchAttributes, func(i, j int) bool {
		return searchAttributes[i].Name < searchAttributes[j].Name
	})
	for _, attribute := range searchAttributes {
		writeEntry(hashValue, "search_attribute", attribute.Name, attribute.Payload)
	}
	var result [sha256.Size]byte
	copy(result[:], hashValue.Sum(nil))
	return result, nil
}

// encodedValue copies one converter payload into the engine-owned value type.
func encodedValue(payload *commonpb.Payload) engine.EncodedValue {
	return engine.EncodedValue{
		Metadata: clonePayloadMetadata(payload.GetMetadata()),
		Data:     append([]byte(nil), payload.GetData()...),
	}
}

// memoPayloadView exposes one encoded value to validation without copying it.
func memoPayloadView(value engine.EncodedValue) *commonpb.Payload {
	return &commonpb.Payload{Metadata: value.Metadata, Data: value.Data}
}

// clonePayloadMetadata gives an encoded value ownership of every metadata byte.
func clonePayloadMetadata(metadata map[string][]byte) map[string][]byte {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string][]byte, len(metadata))
	for name, value := range metadata {
		cloned[name] = append([]byte(nil), value...)
	}
	return cloned
}

// normalizeSearchAttribute maps generic engine values onto Temporal's typed
// visibility values before either the start request or digest sees them.
func normalizeSearchAttribute(name string, value any) (any, enumspb.IndexedValueType, error) {
	switch typed := value.(type) {
	case nil:
		return nil, 0, fmt.Errorf("workflow search attribute %q has nil value", name)
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
		return nil, 0, fmt.Errorf("workflow search attribute %q has unsupported type %T", name, value)
	}
}

// searchAttributeValues returns an owned map of the normalized values recorded
// in a request snapshot.
func searchAttributeValues(attributes []SearchAttribute) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	values := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		value := attribute.Value
		if list, ok := value.([]string); ok {
			value = append([]string(nil), list...)
		}
		values[attribute.Name] = value
	}
	return values
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
