package temporal

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// agentJSONPayloadConverter rejects runtime-only values that have no
	// workflow-safe representation, then delegates canonical payloads directly
	// to Temporal's JSON converter.
	//
	// Temporal's default JSON converter decodes `any` fields as JSON-shaped values.
	// Tool results and artifacts therefore cross workflow boundaries as canonical
	// JSON bytes (api.ToolEvent / api.ToolArtifact), not planner.ToolResult.
	//
	// This converter fails fast when code attempts to send planner.ToolResult
	// across a Temporal boundary; callers must use api.ToolEvent.
	agentJSONPayloadConverter struct {
		*converter.JSONPayloadConverter
	}

	// workflowJSONContainer identifies one active reference value so recursive
	// preflight rejects cycles before encoding/json reaches a custom marshaler.
	workflowJSONContainer struct {
		kind    reflect.Kind
		typ     reflect.Type
		pointer uintptr
	}

	// workflowJSONPreflight bounds reflection work while proving that no
	// workflow-unsafe value can be hidden below an API envelope.
	workflowJSONPreflight struct {
		visits int
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

// NewAgentDataConverter returns a Temporal data converter that enforces goa-ai
// workflow boundary contracts.
//
// Tool values use canonical JSON bytes and generated codecs rather than
// interface-valued planner.ToolResult payloads. Other JSON-shaped metadata is
// decoded with json.Number so numeric values remain lossless.
//
// This converter:
//   - Provides stable encoding/decoding for goa-ai API envelopes.
//   - Fails fast if planner.ToolResult crosses a Temporal boundary (use
//     api.ToolEvent instead).
func NewAgentDataConverter() converter.DataConverter {
	base := converter.NewJSONPayloadConverter()
	return converter.NewCompositeDataConverter(
		converter.NewNilPayloadConverter(),
		converter.NewByteSlicePayloadConverter(),
		converter.NewProtoPayloadConverter(),
		converter.NewProtoJSONPayloadConverter(),
		&agentJSONPayloadConverter{
			JSONPayloadConverter: base,
		},
	)
}

func (c *agentJSONPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if err := (&workflowJSONPreflight{}).walk(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	return c.JSONPayloadConverter.ToPayload(value)
}

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

// walk recursively inspects the exact value graph before encoding/json may
// invoke custom code. API envelopes use ordinary structs and are traversed
// directly. model.Message is the one established custom object encoder and is
// still traversed through its fields; raw JSON messages are established opaque
// canonical byte values.
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
		return nil
	}
	if customKind, unsupported := unsupportedWorkflowJSONMarshaler(typ); unsupported {
		return fmt.Errorf(
			"temporal: workflow JSON value contains unsupported custom %s marshaler %s",
			customKind,
			typ,
		)
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
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
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
		reflect.Chan,
		reflect.Func,
		reflect.String,
		reflect.UnsafePointer:
	}
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
