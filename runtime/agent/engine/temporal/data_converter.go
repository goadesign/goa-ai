package temporal

// This file configures the Temporal value encoder used by agent workflows. It
// rejects planner values that cannot be stored, bounds input data before
// encoding, and rejects encoded payloads above the shared workflow byte limit.

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// agentJSONPayloadConverter rejects planner values that must remain inside
	// one process, then passes accepted values to Temporal's JSON converter.
	//
	// Temporal's default JSON converter decodes `any` fields as JSON-shaped values.
	// Tool results and artifacts therefore cross workflow boundaries as validated
	// JSON bytes (api.ToolEvent / api.ToolArtifact), not planner.ToolResult.
	//
	// This converter fails fast when code attempts to send planner.ToolResult
	// across a Temporal boundary; callers must use api.ToolEvent.
	agentJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
	}

	// boundedDataConverter limits the combined bytes for every workflow or
	// activity argument list, including raw bytes and protobuf messages.
	boundedDataConverter struct {
		inner converter.DataConverter
	}

	// workflowJSONContainer identifies one active reference value so recursive
	// preflight rejects cycles before encoding/json reaches a custom marshaler.
	workflowJSONContainer struct {
		kind    reflect.Kind
		typ     reflect.Type
		pointer uintptr
	}

	// workflowJSONPreflight limits the values inspected before JSON encoding and
	// rejects a planner.ToolResult even when it is nested inside another value.
	workflowJSONPreflight struct {
		visits      int
		sourceBytes int
		active      map[workflowJSONContainer]struct{}
	}
)

const (
	maxWorkflowJSONDepth  = 64
	maxWorkflowJSONVisits = 100_000
)

var (
	jsonMarshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType     = reflect.TypeFor[encoding.TextMarshaler]()
	plannerToolResultType = reflect.TypeFor[planner.ToolResult]()
	modelMessageType      = reflect.TypeFor[model.Message]()
	rawJSONMessageType    = reflect.TypeFor[rawjson.Message]()
	standardRawJSONType   = reflect.TypeFor[json.RawMessage]()
)

// NewAgentDataConverter returns a Temporal data converter that enforces goa-ai
// workflow boundary contracts.
//
// Tool values use validated JSON bytes and generated codecs rather than
// interface-valued planner.ToolResult payloads. Other JSON-shaped metadata is
// decoded with json.Number so numeric values remain lossless.
//
// This converter:
//   - Provides stable encoding/decoding for goa-ai API envelopes.
//   - Fails fast if planner.ToolResult crosses a Temporal boundary (use
//     api.ToolEvent instead).
func NewAgentDataConverter() converter.DataConverter {
	base := converter.NewJSONPayloadConverter()
	return &boundedDataConverter{
		inner: converter.NewCompositeDataConverter(
			converter.NewNilPayloadConverter(),
			converter.NewByteSlicePayloadConverter(),
			converter.NewProtoPayloadConverter(),
			converter.NewProtoJSONPayloadConverter(),
			&agentJSONPayloadConverter{
				JSONPayloadConverter: base,
			},
		),
	}
}

// ToPayload encodes one value and rejects a payload that exceeds the
// framework's Temporal byte limit.
func (c *boundedDataConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if err := preflightTemporalValues(value); err != nil {
		return nil, err
	}
	payload, err := c.inner.ToPayload(value)
	if err != nil {
		return nil, err
	}
	if err := validateTemporalPayloadBytes([]*commonpb.Payload{payload}); err != nil {
		return nil, err
	}
	return payload, nil
}

// FromPayload rejects an oversized saved payload before decoding it.
func (c *boundedDataConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if err := validateTemporalPayloadBytes([]*commonpb.Payload{payload}); err != nil {
		return err
	}
	return c.inner.FromPayload(payload, valuePtr)
}

// ToPayloads encodes all arguments under one aggregate byte limit.
func (c *boundedDataConverter) ToPayloads(values ...any) (*commonpb.Payloads, error) {
	if err := preflightTemporalValues(values...); err != nil {
		return nil, err
	}
	payloads, err := c.inner.ToPayloads(values...)
	if err != nil {
		return nil, err
	}
	if err := validateTemporalPayloadBytes(payloads.GetPayloads()); err != nil {
		return nil, err
	}
	return payloads, nil
}

// FromPayloads rejects oversized saved arguments before decoding them.
func (c *boundedDataConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...any) error {
	if err := validateTemporalPayloadBytes(payloads.GetPayloads()); err != nil {
		return err
	}
	return c.inner.FromPayloads(payloads, valuePtrs...)
}

// ToString delegates Temporal's diagnostic rendering.
func (c *boundedDataConverter) ToString(input *commonpb.Payload) string {
	return c.inner.ToString(input)
}

// ToStrings delegates Temporal's diagnostic rendering.
func (c *boundedDataConverter) ToStrings(input *commonpb.Payloads) []string {
	return c.inner.ToStrings(input)
}

// preflightTemporalValues limits strings, byte sequences, and collection sizes
// before Temporal allocates encoded payloads. A second check counts the exact
// encoded bytes and the metadata Temporal adds.
func preflightTemporalValues(values ...any) error {
	preflight := &workflowJSONPreflight{}
	for _, value := range values {
		switch actual := value.(type) {
		case []byte:
			if err := preflight.addBytes(len(actual)); err != nil {
				return err
			}
		case proto.Message:
			if err := preflight.addBytes(proto.Size(actual)); err != nil {
				return err
			}
		default:
			if err := preflight.walk(reflect.ValueOf(value), 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateTemporalPayloadBytes counts payload data and metadata without integer
// overflow and applies one limit to their combined size.
func validateTemporalPayloadBytes(payloads []*commonpb.Payload) error {
	total := 0
	for _, payload := range payloads {
		if payload == nil {
			continue
		}
		if err := addTemporalPayloadBytes(&total, len(payload.Data)); err != nil {
			return err
		}
		for key, value := range payload.Metadata {
			if err := addTemporalPayloadBytes(&total, len(key)); err != nil {
				return err
			}
			if err := addTemporalPayloadBytes(&total, len(value)); err != nil {
				return err
			}
		}
	}
	return nil
}

// addTemporalPayloadBytes adds one existing payload segment without allocating
// a second list of segment sizes.
func addTemporalPayloadBytes(total *int, size int) error {
	if size > engine.MaxPayloadBytes-*total {
		return fmt.Errorf(
			"temporal: payloads exceed maximum aggregate size %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	*total += size
	return nil
}

// ToPayload rejects values that cannot safely cross a workflow boundary, then
// delegates JSON encoding to Temporal.
func (c *agentJSONPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if err := (&workflowJSONPreflight{}).walk(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	return c.JSONPayloadConverter.ToPayload(value)
}

// FromPayload decodes one JSON payload without losing integer precision or
// accepting unknown fields and trailing data.
func (c *agentJSONPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
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

// walk inspects nested values before encoding/json may invoke custom code.
// model.Message is the one accepted custom object encoder and is still checked
// through its fields. Raw JSON messages are already validated byte values and
// contribute their complete size directly. Byte slices at any nesting depth
// are one byte block rather than one visited value per byte.
func (p *workflowJSONPreflight) walk(value reflect.Value, depth int) error {
	if depth > maxWorkflowJSONDepth {
		return fmt.Errorf("temporal: workflow JSON value exceeds maximum depth %d", maxWorkflowJSONDepth)
	}
	p.visits++
	if p.visits > maxWorkflowJSONVisits {
		return fmt.Errorf("temporal: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
	}
	if !value.IsValid() {
		return nil
	}
	typ := value.Type()
	if typ == plannerToolResultType ||
		(typ.Kind() == reflect.Pointer && typ.Elem() == plannerToolResultType) {
		return fmt.Errorf("temporal: planner.ToolResult must not cross workflow boundaries (use api.ToolEvent)")
	}
	if isWorkflowRawJSONType(typ) {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return nil
			}
			value = value.Elem()
		}
		if value.IsNil() {
			return nil
		}
		if !json.Valid(value.Bytes()) {
			return errors.New("temporal: workflow JSON value contains invalid raw JSON")
		}
		return p.addBytes(value.Len())
	}
	if customKind, unsupported := unsupportedWorkflowJSONMarshaler(typ); unsupported {
		return fmt.Errorf(
			"temporal: workflow JSON value contains unsupported custom %s marshaler %s",
			customKind,
			typ,
		)
	}
	if isWorkflowByteSlice(typ) {
		if value.IsNil() {
			return nil
		}
		return p.addBytes(value.Len())
	}

	container, tracked, err := p.enter(value)
	if err != nil {
		return err
	}
	if tracked {
		defer delete(p.active, container)
	}

	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return p.walk(value.Elem(), depth+1)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.Tag.Get("json") == "-" {
				continue
			}
			if field.PkgPath != "" && !isEmbeddedWorkflowJSONStruct(field) {
				continue
			}
			if err := p.walk(value.Field(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if p.visits+value.Len() > maxWorkflowJSONVisits {
			return fmt.Errorf("temporal: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
		}
		for index := 0; index < value.Len(); index++ {
			if err := p.walk(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if p.visits+2*value.Len() > maxWorkflowJSONVisits {
			return fmt.Errorf("temporal: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := p.walk(iter.Key(), depth+1); err != nil {
				return err
			}
			if err := p.walk(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		return p.addBytes(value.Len())
	case reflect.Invalid,
		reflect.Bool,
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
		reflect.Uintptr:
		return nil
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("temporal: workflow JSON value contains non-finite number")
		}
		return nil
	case
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return fmt.Errorf("temporal: workflow JSON value contains unsupported %s", value.Kind())
	}
	return nil
}

// isEmbeddedWorkflowJSONStruct matches encoding/json's treatment of a private
// anonymous struct: its exported descendants remain part of the encoded object.
func isEmbeddedWorkflowJSONStruct(field reflect.StructField) bool {
	if !field.Anonymous {
		return false
	}
	typ := field.Type
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Kind() == reflect.Struct
}

// addBytes bounds caller-owned strings and byte sequences before a converter
// allocates their encoded form. Exact encoded bytes and metadata are checked
// afterward.
func (p *workflowJSONPreflight) addBytes(size int) error {
	if size < 0 || size > engine.MaxPayloadBytes-p.sourceBytes {
		return fmt.Errorf(
			"temporal: payloads exceed maximum aggregate size %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	p.sourceBytes += size
	return nil
}

// enter tracks reference values on the active recursion path so cyclic values
// fail with a bounded error.
func (p *workflowJSONPreflight) enter(
	value reflect.Value,
) (workflowJSONContainer, bool, error) {
	var pointer uintptr
	switch value.Kind() {
	case reflect.Map:
		if value.IsNil() {
			return workflowJSONContainer{}, false, nil
		}
		pointer = uintptr(value.UnsafePointer())
	case reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return workflowJSONContainer{}, false, nil
		}
		pointer = value.Pointer()
	case reflect.Invalid,
		reflect.Bool,
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
		reflect.Uintptr,
		reflect.Float32,
		reflect.Float64,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Array,
		reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.String,
		reflect.Struct,
		reflect.UnsafePointer:
		return workflowJSONContainer{}, false, nil
	}
	if pointer == 0 {
		return workflowJSONContainer{}, false, nil
	}
	container := workflowJSONContainer{
		kind:    value.Kind(),
		typ:     value.Type(),
		pointer: pointer,
	}
	if p.active == nil {
		p.active = make(map[workflowJSONContainer]struct{})
	}
	if _, exists := p.active[container]; exists {
		return workflowJSONContainer{}, false, fmt.Errorf(
			"temporal: workflow JSON value contains a %s reference cycle",
			value.Kind(),
		)
	}
	p.active[container] = struct{}{}
	return container, true, nil
}

// unsupportedWorkflowJSONMarshaler rejects encoders whose output can conceal a
// workflow-only typed value. The established model.Message encoder remains
// visible to recursive traversal.
func unsupportedWorkflowJSONMarshaler(typ reflect.Type) (string, bool) {
	if typ == modelMessageType ||
		(typ.Kind() == reflect.Pointer && typ.Elem() == modelMessageType) {
		return "", false
	}
	if typ.Implements(jsonMarshalerType) ||
		(typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(jsonMarshalerType)) {
		return "JSON", true
	}
	if typ.Implements(textMarshalerType) ||
		(typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(textMarshalerType)) {
		return "text", true
	}
	return "", false
}

// isWorkflowRawJSONType identifies the two established opaque byte values whose
// custom JSON encoding is exactly their validated canonical payload.
func isWorkflowRawJSONType(typ reflect.Type) bool {
	if typ == rawJSONMessageType || typ == standardRawJSONType {
		return true
	}
	return typ.Kind() == reflect.Pointer &&
		(typ.Elem() == rawJSONMessageType || typ.Elem() == standardRawJSONType)
}

// isWorkflowByteSlice matches byte slices that encoding/json writes as one
// base64 string. A named byte element with custom encoding is inspected
// element by element so the custom encoder is rejected.
func isWorkflowByteSlice(typ reflect.Type) bool {
	if typ.Kind() != reflect.Slice || typ.Elem().Kind() != reflect.Uint8 {
		return false
	}
	element := typ.Elem()
	return !element.Implements(jsonMarshalerType) &&
		!reflect.PointerTo(element).Implements(jsonMarshalerType) &&
		!element.Implements(textMarshalerType) &&
		!reflect.PointerTo(element).Implements(textMarshalerType)
}
