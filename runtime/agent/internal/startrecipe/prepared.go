// Package startrecipe serializes complete workflow start requests so callers
// can store one accepted request and submit its exact values after a restart.
package startrecipe

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

type (
	// PreparedRequest owns one validated engine request. MarshalBinary creates
	// the bytes an application stores before submitting that request.
	PreparedRequest struct {
		// AgentID identifies the generated agent definition that prepared the run.
		AgentID string
		// Request is the exact workflow request submitted to the engine.
		Request engine.WorkflowStartRequest
		// TaskQueueOverride is the caller's explicit queue choice. Empty means the
		// generated agent definition supplied the request queue.
		TaskQueueOverride string
		// Digest identifies every value in Request.
		Digest   [sha256.Size]byte
		snapshot RequestSnapshot
		data     []byte
	}

	// preparedRequestWire is the versioned durable representation. Dynamic
	// values remain converter payloads so a later process does not need their
	// original concrete Go types to submit the same engine request.
	preparedRequestWire struct {
		Version           string                    `json:"version"`
		AgentID           string                    `json:"agent_id"`
		ID                string                    `json:"id"`
		Workflow          string                    `json:"workflow"`
		TaskQueue         string                    `json:"task_queue"`
		Input             preparedPayload           `json:"input"`
		RunTimeout        time.Duration             `json:"run_timeout"`
		RetryPolicy       engine.RetryPolicy        `json:"retry_policy"`
		Memo              []preparedValue           `json:"memo"`
		SearchAttributes  []preparedSearchAttribute `json:"search_attributes"`
		TaskQueueOverride string                    `json:"task_queue_override,omitempty"`
	}

	// preparedValue stores one named engine value as an encoded payload.
	preparedValue struct {
		Name    string          `json:"name"`
		Payload preparedPayload `json:"payload"`
	}

	// preparedSearchAttribute stores one visibility value as an encoded payload.
	// Its payload metadata records the closed Temporal visibility type.
	preparedSearchAttribute struct {
		Name    string          `json:"name"`
		Payload preparedPayload `json:"payload"`
	}

	// preparedPayload is the JSON-safe form of a Temporal payload.
	preparedPayload struct {
		Metadata map[string][]byte `json:"metadata"`
		Data     []byte            `json:"data"`
	}
)

const (
	preparedRequestVersion = "goa-ai-prepared-run-v1"

	// maxPreparedRequestBytes bounds storage input before JSON decoding. It is
	// eight times the engine request limit, which is larger than the six-byte
	// JSON escape or four-byte base64 form of one input byte. The writer checks
	// the complete record, including JSON field syntax, against this limit too.
	maxPreparedRequestBytes = 8 * engine.MaxPayloadBytes

	searchAttributeTypeMetadata = "type"
)

// NewPreparedRequest validates and snapshots one complete engine request. The
// returned value owns the values submitted to the engine. It does not create
// the durable representation until MarshalBinary is called.
func NewPreparedRequest(
	agentID string,
	request engine.WorkflowStartRequest,
	taskQueueOverride string,
) (PreparedRequest, error) {
	if err := validatePreparedRequest(agentID, request, taskQueueOverride); err != nil {
		return PreparedRequest{}, err
	}
	snapshot, err := SnapshotRequest(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	return PreparedRequest{
		AgentID:           agentID,
		Request:           snapshot.Request,
		TaskQueueOverride: taskQueueOverride,
		Digest:            snapshot.Digest,
		snapshot:          snapshot,
	}, nil
}

// ParsePreparedRequest parses one stored prepared run. It rebuilds the bytes
// from the decoded request and rejects every alternate JSON representation.
func ParsePreparedRequest(data []byte) (PreparedRequest, error) {
	if len(data) > maxPreparedRequestBytes {
		return PreparedRequest{}, fmt.Errorf(
			"decode prepared run: stored value exceeds maximum size %d bytes",
			maxPreparedRequestBytes,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire preparedRequestWire
	if err := decoder.Decode(&wire); err != nil {
		return PreparedRequest{}, fmt.Errorf("decode prepared run: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return PreparedRequest{}, errors.New("decode prepared run: trailing data")
	}
	if err := validatePreparedWire(wire); err != nil {
		return PreparedRequest{}, err
	}

	var input *api.RunInput
	if err := workflowcodec.NewDataConverter().FromPayload(preparedPayloadView(wire.Input), &input); err != nil {
		return PreparedRequest{}, fmt.Errorf("decode prepared run input: %w", err)
	}
	if input == nil {
		return PreparedRequest{}, errors.New("decode prepared run: input is required")
	}
	memo := unmarshalPreparedValues(wire.Memo)
	searchAttributes, err := unmarshalPreparedSearchAttributes(wire.SearchAttributes)
	if err != nil {
		return PreparedRequest{}, err
	}
	request := engine.WorkflowStartRequest{
		ID:               wire.ID,
		Workflow:         wire.Workflow,
		TaskQueue:        wire.TaskQueue,
		Input:            input,
		RunTimeout:       wire.RunTimeout,
		Memo:             memo,
		SearchAttributes: searchAttributes,
		RetryPolicy:      wire.RetryPolicy,
	}
	if err := validatePreparedRequest(wire.AgentID, request, wire.TaskQueueOverride); err != nil {
		return PreparedRequest{}, fmt.Errorf("decode prepared run: %w", err)
	}
	snapshot, err := SnapshotRequest(request)
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("validate prepared workflow request: %w", err)
	}
	prepared := PreparedRequest{
		AgentID:           wire.AgentID,
		Request:           snapshot.Request,
		TaskQueueOverride: wire.TaskQueueOverride,
		Digest:            snapshot.Digest,
		snapshot:          snapshot,
	}
	expected, err := prepared.MarshalBinary()
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("encode parsed prepared run: %w", err)
	}
	if !bytes.Equal(data, expected) {
		return PreparedRequest{}, errors.New(
			"decode prepared run: bytes are not the canonical prepared-run encoding",
		)
	}
	prepared.data = slices.Clone(data)
	return prepared, nil
}

// MarshalBinary returns an independent copy of the prepared run's durable
// representation. A parsed request returns the exact accepted bytes. A new
// request creates the representation and enforces the storage size limit.
func (p PreparedRequest) MarshalBinary() ([]byte, error) {
	if p.data != nil {
		return slices.Clone(p.data), nil
	}
	data, err := json.Marshal(marshalPreparedRequestWire(p.AgentID, p.snapshot, p.TaskQueueOverride))
	if err != nil {
		return nil, fmt.Errorf("marshal prepared run: %w", err)
	}
	if len(data) > maxPreparedRequestBytes {
		return nil, fmt.Errorf(
			"prepared run exceeds maximum stored size %d bytes",
			maxPreparedRequestBytes,
		)
	}
	return data, nil
}

// validatePreparedRequest checks the relationship between the agent, workflow
// identity, input, and explicit queue before any durable bytes are created.
func validatePreparedRequest(
	agentID string,
	request engine.WorkflowStartRequest,
	taskQueueOverride string,
) error {
	if agentID == "" {
		return errors.New("prepared run agent id is required")
	}
	if request.ID == "" {
		return errors.New("prepared run workflow id is required")
	}
	if request.Workflow == "" {
		return errors.New("prepared run workflow name is required")
	}
	if request.TaskQueue == "" {
		return errors.New("prepared run task queue is required")
	}
	if request.Input == nil {
		return errors.New("prepared run input is required")
	}
	if string(request.Input.AgentID) != agentID {
		return errors.New("prepared run input agent id does not match its envelope")
	}
	if request.Input.RunID != request.ID {
		return errors.New("prepared run input run id does not match its workflow id")
	}
	if taskQueueOverride != "" && taskQueueOverride != request.TaskQueue {
		return errors.New("prepared run task queue override does not match its workflow request")
	}
	if _, reserved := request.Memo[MemoKey]; reserved {
		return fmt.Errorf("prepared run memo key %q is reserved", MemoKey)
	}
	return nil
}

// validatePreparedWire checks the complete engine request before copying or
// decoding any stored workflow value. One budget counts the exact text and
// payload bytes the engine will receive, including its reserved digest memo.
func validatePreparedWire(wire preparedRequestWire) error {
	if wire.Version != preparedRequestVersion {
		return fmt.Errorf("decode prepared run: unsupported version %q", wire.Version)
	}
	if wire.AgentID == "" {
		return errors.New("decode prepared run: agent id is required")
	}
	if wire.ID == "" {
		return errors.New("decode prepared run: workflow id is required")
	}
	if wire.Workflow == "" {
		return errors.New("decode prepared run: workflow name is required")
	}
	if wire.TaskQueue == "" {
		return errors.New("decode prepared run: task queue is required")
	}
	if wire.TaskQueueOverride != "" && wire.TaskQueueOverride != wire.TaskQueue {
		return errors.New("decode prepared run: task queue override does not match its workflow request")
	}
	if err := engine.ValidateWorkflowLaunchSettings(wire.RunTimeout, wire.RetryPolicy); err != nil {
		return fmt.Errorf("decode prepared run: %w", err)
	}
	if err := validatePreparedNames(
		wire.Memo,
		func(value preparedValue) string { return value.Name },
	); err != nil {
		return fmt.Errorf("decode prepared run memo: %w", err)
	}
	if err := validatePreparedNames(
		wire.SearchAttributes,
		func(value preparedSearchAttribute) string { return value.Name },
	); err != nil {
		return fmt.Errorf("decode prepared run search attributes: %w", err)
	}
	budget := new(workflowcodec.Budget)
	if err := budget.AddText(wire.ID, wire.Workflow, wire.TaskQueue); err != nil {
		return fmt.Errorf("decode prepared run text: %w", err)
	}
	if err := reserveRootRecipeMemo(workflowcodec.NewDataConverter(), budget); err != nil {
		return fmt.Errorf("decode prepared run: %w", err)
	}
	if err := budget.AddPayload(preparedPayloadView(wire.Input)); err != nil {
		return fmt.Errorf("decode prepared run input: %w", err)
	}
	for _, value := range wire.Memo {
		if err := budget.AddText(value.Name); err != nil {
			return fmt.Errorf("decode prepared run memo name %q: %w", value.Name, err)
		}
		if err := budget.AddPayload(preparedPayloadView(value.Payload)); err != nil {
			return fmt.Errorf("decode prepared run memo %q: %w", value.Name, err)
		}
	}
	for _, attribute := range wire.SearchAttributes {
		if err := budget.AddText(attribute.Name); err != nil {
			return fmt.Errorf("decode prepared run search attribute name %q: %w", attribute.Name, err)
		}
		if err := budget.AddPayload(preparedPayloadView(attribute.Payload)); err != nil {
			return fmt.Errorf("decode prepared run search attribute %q: %w", attribute.Name, err)
		}
	}
	return nil
}

// marshalPreparedRequestWire builds the only accepted durable representation
// from an owned request snapshot.
func marshalPreparedRequestWire(
	agentID string,
	snapshot RequestSnapshot,
	taskQueueOverride string,
) preparedRequestWire {
	return preparedRequestWire{
		Version:           preparedRequestVersion,
		AgentID:           agentID,
		ID:                snapshot.Request.ID,
		Workflow:          snapshot.Request.Workflow,
		TaskQueue:         snapshot.Request.TaskQueue,
		Input:             marshalPreparedPayload(snapshot.InputPayload),
		RunTimeout:        snapshot.Request.RunTimeout,
		RetryPolicy:       snapshot.Request.RetryPolicy,
		Memo:              marshalPreparedValues(snapshot.Request.Memo),
		SearchAttributes:  marshalPreparedSearchAttributes(snapshot.SearchAttributes),
		TaskQueueOverride: taskQueueOverride,
	}
}

// marshalPreparedValues orders encoded memo values for the durable envelope.
func marshalPreparedValues(values map[string]engine.EncodedValue) []preparedValue {
	names := sortedKeys(values)
	encoded := make([]preparedValue, 0, len(names))
	for _, name := range names {
		encoded = append(encoded, preparedValue{
			Name:    name,
			Payload: marshalPreparedPayload(MemoPayload(values[name])),
		})
	}
	return encoded
}

// unmarshalPreparedValues restores exact encoded memo bytes without guessing
// which private Go type produced them.
func unmarshalPreparedValues(
	values []preparedValue,
) map[string]engine.EncodedValue {
	if len(values) == 0 {
		return nil
	}
	decoded := make(map[string]engine.EncodedValue, len(values))
	for _, value := range values {
		decoded[value.Name] = encodedValue(preparedPayloadView(value.Payload))
	}
	return decoded
}

// marshalPreparedSearchAttributes stores each normalized visibility payload.
// The payload metadata already records its closed Temporal visibility type.
func marshalPreparedSearchAttributes(attributes []SearchAttribute) []preparedSearchAttribute {
	encoded := make([]preparedSearchAttribute, len(attributes))
	for index, attribute := range attributes {
		encoded[index] = preparedSearchAttribute{
			Name:    attribute.Name,
			Payload: marshalPreparedPayload(attribute.Payload),
		}
	}
	return encoded
}

// unmarshalPreparedSearchAttributes decodes only visibility types recorded in
// payload metadata and verifies that each value has its one accepted encoding.
func unmarshalPreparedSearchAttributes(
	attributes []preparedSearchAttribute,
) (map[string]any, error) {
	if len(attributes) == 0 {
		return nil, nil
	}
	decoded := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		payload := preparedPayloadView(attribute.Payload)
		value, valueType, err := unmarshalPreparedSearchAttribute(payload)
		if err != nil {
			return nil, fmt.Errorf("decode prepared run search attribute %q: %w", attribute.Name, err)
		}
		canonical, err := EncodeSearchAttributes(map[string]any{attribute.Name: value})
		if err != nil {
			return nil, err
		}
		if canonical[0].ValueType != valueType || !proto.Equal(canonical[0].Payload, payload) {
			return nil, fmt.Errorf("payload does not match recorded type %s", valueType)
		}
		decoded[attribute.Name] = value
	}
	return decoded, nil
}

// unmarshalPreparedSearchAttribute derives the closed visibility type from
// payload metadata, then decodes the corresponding Go value.
func unmarshalPreparedSearchAttribute(payload *commonpb.Payload) (any, enumspb.IndexedValueType, error) {
	typeName := string(payload.GetMetadata()[searchAttributeTypeMetadata])
	valueType, err := enumspb.IndexedValueTypeFromString(typeName)
	if err != nil {
		return nil, 0, fmt.Errorf("unsupported value type %q", payload.GetMetadata()[searchAttributeTypeMetadata])
	}
	dataConverter := converter.GetDefaultDataConverter()
	switch valueType {
	case enumspb.INDEXED_VALUE_TYPE_UNSPECIFIED, enumspb.INDEXED_VALUE_TYPE_TEXT:
		return nil, 0, fmt.Errorf("unsupported value type %s", valueType)
	case enumspb.INDEXED_VALUE_TYPE_KEYWORD:
		var value string
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	case enumspb.INDEXED_VALUE_TYPE_INT:
		var value int64
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	case enumspb.INDEXED_VALUE_TYPE_DOUBLE:
		var value float64
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	case enumspb.INDEXED_VALUE_TYPE_BOOL:
		var value bool
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	case enumspb.INDEXED_VALUE_TYPE_DATETIME:
		var value time.Time
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	case enumspb.INDEXED_VALUE_TYPE_KEYWORD_LIST:
		var value []string
		if err := dataConverter.FromPayload(payload, &value); err != nil {
			return nil, 0, err
		}
		return value, valueType, nil
	default:
		return nil, 0, fmt.Errorf("unsupported value type %s", valueType)
	}
}

// marshalPreparedPayload copies one converter payload into its JSON-safe wire
// representation.
func marshalPreparedPayload(payload *commonpb.Payload) preparedPayload {
	metadata := make(map[string][]byte, len(payload.Metadata))
	for name, value := range payload.Metadata {
		metadata[name] = slices.Clone(value)
	}
	return preparedPayload{Metadata: metadata, Data: slices.Clone(payload.Data)}
}

// preparedPayloadView exposes bytes already owned by the decoded JSON record.
// Validation and decoding use this view without allocating a second copy.
func preparedPayloadView(payload preparedPayload) *commonpb.Payload {
	return &commonpb.Payload{Metadata: payload.Metadata, Data: payload.Data}
}

// validatePreparedNames rejects empty, duplicate, or unsorted entry names so
// one logical request has one durable representation.
func validatePreparedNames[T any](values []T, name func(T) string) error {
	for index, value := range values {
		current := name(value)
		if current == "" {
			return errors.New("entry name is required")
		}
		if index > 0 && name(values[index-1]) >= current {
			return fmt.Errorf("entry %q is duplicate or out of order", current)
		}
	}
	return nil
}
