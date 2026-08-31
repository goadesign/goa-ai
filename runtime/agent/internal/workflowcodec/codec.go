// Package workflowcodec owns the exact value encoding used by every workflow
// engine. It validates and copies raw payloads, applies one shared byte limit,
// and rejects values that cannot safely cross a workflow boundary.
package workflowcodec

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"unicode/utf8"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// dataConverter limits the combined bytes for every workflow or
	// activity argument list, including raw bytes and protobuf messages.
	dataConverter struct {
		inner converter.DataConverter
	}

	// strictJSONPayloadConverter preserves integer precision and rejects
	// unknown fields when workflow values are decoded.
	strictJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
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
		visits int
		budget *Budget
		active map[workflowJSONContainer]struct{}
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

// NewDataConverter returns the data converter shared by every workflow engine.
//
// Tool values use validated JSON bytes and generated codecs rather than
// interface-valued planner.ToolResult payloads. Other JSON-shaped metadata is
// decoded with json.Number so numeric values remain lossless.
//
// This converter:
//   - Provides stable encoding/decoding for goa-ai API envelopes.
//   - Fails fast if planner.ToolResult crosses a workflow boundary (use
//     api.ToolEvent instead).
func NewDataConverter() converter.DataConverter {
	return &dataConverter{
		inner: converter.NewCompositeDataConverter(
			converter.NewNilPayloadConverter(),
			converter.NewByteSlicePayloadConverter(),
			converter.NewProtoPayloadConverter(),
			converter.NewProtoJSONPayloadConverter(),
			&strictJSONPayloadConverter{
				JSONPayloadConverter: converter.NewJSONPayloadConverter(),
			},
		),
	}
}

// ToPayload encodes one value and rejects a payload that exceeds the
// shared workflow byte limit.
func (c *dataConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if raw, ok := value.(converter.RawValue); ok {
		if raw.Payload() == nil {
			return nil, errors.New("workflow codec: raw payload is nil")
		}
		if err := ValidatePayloads(singlePayload(raw.Payload())); err != nil {
			return nil, err
		}
		return copyPayload(raw.Payload()), nil
	}
	if err := preflightValues(value); err != nil {
		return nil, err
	}
	payload, err := c.inner.ToPayload(value)
	if err != nil {
		return nil, err
	}
	if err := ValidatePayloads(singlePayload(payload)); err != nil {
		return nil, err
	}
	return payload, nil
}

// FromPayload rejects an oversized saved payload before decoding it.
func (c *dataConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if payload == nil {
		return errors.New("workflow codec: payload is nil")
	}
	if err := ValidatePayloads(singlePayload(payload)); err != nil {
		return err
	}
	if raw, ok := valuePtr.(*converter.RawValue); ok {
		*raw = converter.NewRawValue(copyPayload(payload))
		return nil
	}
	return c.inner.FromPayload(payload, valuePtr)
}

// ToPayloads encodes all arguments under one aggregate byte limit.
func (c *dataConverter) ToPayloads(values ...any) (*commonpb.Payloads, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if err := preflightValues(values...); err != nil {
		return nil, err
	}
	payloads := &commonpb.Payloads{Payloads: make([]*commonpb.Payload, len(values))}
	for index, value := range values {
		if raw, ok := value.(converter.RawValue); ok {
			payloads.Payloads[index] = copyPayload(raw.Payload())
			continue
		}
		payload, err := c.inner.ToPayload(value)
		if err != nil {
			return nil, fmt.Errorf("values[%d]: %w", index, err)
		}
		payloads.Payloads[index] = payload
	}
	if err := ValidatePayloads(payloads); err != nil {
		return nil, err
	}
	return payloads, nil
}

// FromPayloads rejects oversized saved arguments before decoding them.
func (c *dataConverter) FromPayloads(payloads *commonpb.Payloads, valuePtrs ...any) error {
	if err := ValidatePayloads(payloads); err != nil {
		return err
	}
	if len(payloads.GetPayloads()) != len(valuePtrs) {
		return fmt.Errorf(
			"workflow codec: payload count %d does not match destination count %d",
			len(payloads.GetPayloads()),
			len(valuePtrs),
		)
	}
	for index, payload := range payloads.GetPayloads() {
		if err := c.FromPayload(payload, valuePtrs[index]); err != nil {
			return fmt.Errorf("payload item %d: %w", index, err)
		}
	}
	return nil
}

// ToString delegates the underlying converter's diagnostic rendering.
func (c *dataConverter) ToString(input *commonpb.Payload) string {
	return c.inner.ToString(input)
}

// ToStrings delegates the underlying converter's diagnostic rendering.
func (c *dataConverter) ToStrings(input *commonpb.Payloads) []string {
	return c.inner.ToStrings(input)
}

// preflightValues limits strings, byte sequences, and collection sizes before
// encoding allocates payloads. A second check counts the exact
// encoded bytes and the metadata added by encoding.
func preflightValues(values ...any) error {
	return new(Budget).AddSource(values...)
}

// add counts one source value without retaining it.
func (p *workflowJSONPreflight) add(value any) error {
	switch actual := value.(type) {
	case converter.RawValue:
		if actual.Payload() == nil {
			return errors.New("workflow codec: raw payload is nil")
		}
		return p.budget.AddPayload(actual.Payload())
	case []byte:
		return p.budget.addBytes(len(actual))
	case proto.Message:
		return p.budget.addBytes(proto.Size(actual))
	default:
		return p.walk(reflect.ValueOf(value), 0)
	}
}

// FromPayload decodes canonical JSON without losing integer precision,
// accepting unknown fields, or ignoring trailing bytes.
func (c *strictJSONPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if payload == nil {
		return errors.New("workflow codec: payload is nil")
	}
	if !utf8.Valid(payload.Data) {
		return errors.New("workflow codec: canonical JSON payload contains invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload.Data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(valuePtr); err != nil {
		return fmt.Errorf("workflow codec: decode canonical JSON payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("workflow codec: canonical JSON payload has trailing data")
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
		return fmt.Errorf("workflow codec: workflow JSON value exceeds maximum depth %d", maxWorkflowJSONDepth)
	}
	p.visits++
	if p.visits > maxWorkflowJSONVisits {
		return fmt.Errorf("workflow codec: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
	}
	if !value.IsValid() {
		return nil
	}
	typ := value.Type()
	if typ == plannerToolResultType ||
		(typ.Kind() == reflect.Pointer && typ.Elem() == plannerToolResultType) {
		return fmt.Errorf("workflow codec: planner.ToolResult must not cross workflow boundaries (use api.ToolEvent)")
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
		if !utf8.Valid(value.Bytes()) {
			return errors.New("workflow codec: workflow JSON value contains invalid UTF-8")
		}
		if !json.Valid(value.Bytes()) {
			return errors.New("workflow codec: workflow JSON value contains invalid raw JSON")
		}
		return p.budget.addBytes(value.Len())
	}
	if customKind, unsupported := unsupportedWorkflowJSONMarshaler(typ); unsupported {
		return fmt.Errorf(
			"workflow codec: workflow JSON value contains unsupported custom %s marshaler %s",
			customKind,
			typ,
		)
	}
	if isWorkflowByteSlice(typ) {
		if value.IsNil() {
			return nil
		}
		return p.budget.addBytes(value.Len())
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
			return fmt.Errorf("workflow codec: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
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
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf(
				"workflow codec: workflow JSON map key type %s must have underlying kind string",
				value.Type().Key(),
			)
		}
		if p.visits+2*value.Len() > maxWorkflowJSONVisits {
			return fmt.Errorf("workflow codec: workflow JSON value exceeds maximum visited values %d", maxWorkflowJSONVisits)
		}
		iter := value.MapRange()
		for iter.Next() {
			p.visits++
			key := iter.Key().String()
			if !utf8.ValidString(key) {
				return errors.New("workflow codec: workflow JSON string contains invalid UTF-8")
			}
			if err := p.budget.addBytes(len(key)); err != nil {
				return err
			}
			if err := p.walk(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("workflow codec: workflow JSON string contains invalid UTF-8")
		}
		return p.budget.addBytes(value.Len())
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
			return errors.New("workflow codec: workflow JSON value contains non-finite number")
		}
		return nil
	case
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return fmt.Errorf("workflow codec: workflow JSON value contains unsupported %s", value.Kind())
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
			"workflow codec: workflow JSON value contains a %s reference cycle",
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
