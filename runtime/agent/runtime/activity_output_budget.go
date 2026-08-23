package runtime

// This file bounds planner activity results before Temporal encodes them. The
// runtime walks the typed output directly so model text, tool JSON, event
// payloads, and dynamic metadata share one allocation-safe activity budget.

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const (
	maxPlanActivityOutputBytes  = 1 << 20
	maxPlanActivityOutputVisits = 100_000
	maxPlanActivityOutputDepth  = 64

	maxJSONTypeEnvelopeBytes = 64
	maxJSONFloatBytes        = 32
	maxJSONIntegerBytes      = 20
)

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	modelMessageType  = reflect.TypeFor[model.Message]()
	rawJSONType       = reflect.TypeFor[rawjson.Message]()
	stdRawJSONType    = reflect.TypeFor[json.RawMessage]()
)

type (
	// planActivityOutputBudgetError identifies a value that cannot safely cross
	// the planner activity boundary.
	planActivityOutputBudgetError struct {
		reason string
	}

	// planActivityOutputContainer identifies a reference value on the current
	// reflection path so cycles fail without retaining the whole object graph.
	planActivityOutputContainer struct {
		kind    reflect.Kind
		typ     reflect.Type
		pointer uintptr
	}

	// planActivityOutputBudget counts a conservative upper bound for encoded
	// JSON bytes and traversal work across one complete PlanActivityOutput.
	planActivityOutputBudget struct {
		bytes  int
		visits int
		active map[planActivityOutputContainer]struct{}
	}
)

// Error states the activity-envelope contract that rejected the output.
func (e *planActivityOutputBudgetError) Error() string {
	return e.reason
}

// checkPlanActivityOutputBudget verifies a conservative 1 MiB pre-encoding
// bound for the complete typed activity envelope without serializing or copying
// its nested collections.
func checkPlanActivityOutputBudget(output *PlanActivityOutput) error {
	return (&planActivityOutputBudget{}).add(output)
}

// add includes one output branch in the shared byte and traversal budget.
func (b *planActivityOutputBudget) add(value any) error {
	return b.walk(reflect.ValueOf(value), 0)
}

// walk visits typed values without converting interfaces or allocating map-key
// snapshots. Every charge is at least the largest JSON representation the
// corresponding supported Go value can produce.
func (b *planActivityOutputBudget) walk(value reflect.Value, depth int) error {
	if depth > maxPlanActivityOutputDepth {
		return newPlanActivityOutputBudgetError(
			"planner activity output exceeds maximum depth %d",
			maxPlanActivityOutputDepth,
		)
	}
	if err := b.visit(); err != nil {
		return err
	}
	if !value.IsValid() {
		return b.addBytes(len("null"))
	}
	if isRawJSON(value) {
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		return b.addScaledBytes(value.Len(), 6)
	}
	if isByteCollection(value) {
		if value.Kind() == reflect.Slice && value.IsNil() {
			return b.addBytes(len("null"))
		}
		return b.addBytes(base64JSONBytes(value.Len()))
	}
	if marshalerKind, unsupported := unsupportedActivityOutputMarshaler(value); unsupported {
		return newPlanActivityOutputBudgetError(
			"planner activity output contains custom %s marshaler %s whose encoded size cannot be bounded without serialization",
			marshalerKind,
			value.Type(),
		)
	}

	container, tracked, err := b.enter(value)
	if err != nil {
		return err
	}
	if tracked {
		defer delete(b.active, container)
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		if err := b.addBytes(maxJSONTypeEnvelopeBytes); err != nil {
			return err
		}
		return b.walk(value.Elem(), depth+1)
	case reflect.Pointer:
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		return b.walk(value.Elem(), depth+1)
	case reflect.Struct:
		if err := b.addBytes(2); err != nil {
			return err
		}
		if err := b.checkChildren(value.NumField()); err != nil {
			return err
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := b.addJSONStringBytes(jsonFieldName(field)); err != nil {
				return err
			}
			if err := b.addBytes(2); err != nil {
				return err
			}
			if err := b.walk(value.Field(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return b.addBytes(len("null"))
		}
		if err := b.addCollectionBytes(value.Len()); err != nil {
			return err
		}
		if err := b.checkChildren(value.Len()); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if err := b.walk(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		if err := b.addCollectionBytes(value.Len()); err != nil {
			return err
		}
		if err := b.checkChildren(value.Len(), value.Len()); err != nil {
			return err
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := b.visit(); err != nil {
				return err
			}
			if err := b.addMapKey(iter.Key()); err != nil {
				return err
			}
			if err := b.addBytes(1); err != nil {
				return err
			}
			if err := b.walk(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		return b.addJSONStringBytes(value.String())
	case reflect.Bool:
		return b.addBytes(len("false"))
	case reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64:
		return b.addBytes(maxJSONIntegerBytes)
	case reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr:
		return b.addBytes(maxJSONIntegerBytes)
	case reflect.Float32,
		reflect.Float64:
		return b.addBytes(maxJSONFloatBytes)
	case reflect.Invalid:
		return b.addBytes(len("null"))
	case reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return newPlanActivityOutputBudgetError(
			"planner activity output contains unsupported JSON value %s",
			value.Kind(),
		)
	}
	return nil
}

// visit charges one reflected value before descending into it.
func (b *planActivityOutputBudget) visit() error {
	b.visits++
	if b.visits > maxPlanActivityOutputVisits {
		return newPlanActivityOutputBudgetError(
			"planner activity output exceeds maximum visited values %d",
			maxPlanActivityOutputVisits,
		)
	}
	return nil
}

// addBytes charges a proven encoded-size upper bound without integer overflow.
func (b *planActivityOutputBudget) addBytes(count int) error {
	if count < 0 || count > maxPlanActivityOutputBytes-b.bytes {
		return newPlanActivityOutputBudgetError(
			"planner activity output exceeds conservative encoded-size bound %d bytes",
			maxPlanActivityOutputBytes,
		)
	}
	b.bytes += count
	return nil
}

// addScaledBytes charges multiplier*count without evaluating an overflowing
// multiplication.
func (b *planActivityOutputBudget) addScaledBytes(count, multiplier int) error {
	if count < 0 || multiplier < 0 || count > (maxPlanActivityOutputBytes-b.bytes)/multiplier {
		return newPlanActivityOutputBudgetError(
			"planner activity output exceeds conservative encoded-size bound %d bytes",
			maxPlanActivityOutputBytes,
		)
	}
	return b.addBytes(count * multiplier)
}

// addJSONStringBytes charges the maximum six-byte escape for every source byte,
// plus the surrounding quotes.
func (b *planActivityOutputBudget) addJSONStringBytes(value string) error {
	if err := b.addBytes(2); err != nil {
		return err
	}
	return b.addScaledBytes(len(value), 6)
}

// addCollectionBytes charges braces or brackets and every possible comma.
func (b *planActivityOutputBudget) addCollectionBytes(length int) error {
	if err := b.addBytes(2); err != nil {
		return err
	}
	if length > 1 {
		return b.addBytes(length - 1)
	}
	return nil
}

// addMapKey charges the quoted JSON spelling of one supported map key.
func (b *planActivityOutputBudget) addMapKey(key reflect.Value) error {
	switch key.Kind() {
	case reflect.String:
		if key.Type().Implements(textMarshalerType) ||
			(reflect.PointerTo(key.Type()).Implements(textMarshalerType)) {
			return newPlanActivityOutputBudgetError(
				"planner activity output contains custom text map key %s whose encoded size cannot be bounded without serialization",
				key.Type(),
			)
		}
		return b.addJSONStringBytes(key.String())
	case reflect.Int,
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
		return b.addBytes(maxJSONIntegerBytes + 2)
	case reflect.Invalid,
		reflect.Bool,
		reflect.Float32,
		reflect.Float64,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Array,
		reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice,
		reflect.Struct,
		reflect.UnsafePointer:
		return newPlanActivityOutputBudgetError(
			"planner activity output contains unsupported JSON map key %s",
			key.Type(),
		)
	default:
		return newPlanActivityOutputBudgetError(
			"planner activity output contains unsupported JSON map key %s",
			key.Type(),
		)
	}
}

// checkChildren rejects large collections before walking their elements.
func (b *planActivityOutputBudget) checkChildren(counts ...int) error {
	remaining := maxPlanActivityOutputVisits - b.visits
	for _, count := range counts {
		if count < 0 || count > remaining {
			return newPlanActivityOutputBudgetError(
				"planner activity output exceeds maximum visited values %d",
				maxPlanActivityOutputVisits,
			)
		}
		remaining -= count
	}
	return nil
}

// enter detects reference cycles on the active path.
func (b *planActivityOutputBudget) enter(
	value reflect.Value,
) (planActivityOutputContainer, bool, error) {
	var pointer uintptr
	switch value.Kind() {
	case reflect.Map:
		if value.IsNil() {
			return planActivityOutputContainer{}, false, nil
		}
		pointer = uintptr(value.UnsafePointer())
	case reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return planActivityOutputContainer{}, false, nil
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
		return planActivityOutputContainer{}, false, nil
	}
	if pointer == 0 {
		return planActivityOutputContainer{}, false, nil
	}
	container := planActivityOutputContainer{
		kind:    value.Kind(),
		typ:     value.Type(),
		pointer: pointer,
	}
	if b.active == nil {
		b.active = make(map[planActivityOutputContainer]struct{})
	}
	if _, exists := b.active[container]; exists {
		return planActivityOutputContainer{}, false, newPlanActivityOutputBudgetError(
			"planner activity output contains a %s reference cycle",
			value.Kind(),
		)
	}
	b.active[container] = struct{}{}
	return container, true, nil
}

// isByteCollection reports the string-like byte containers charged by length
// instead of by each element.
func isByteCollection(value reflect.Value) bool {
	kind := value.Kind()
	return (kind == reflect.Slice || kind == reflect.Array) &&
		value.Type().Elem().Kind() == reflect.Uint8
}

// isRawJSON identifies byte slices whose JSON marshaler emits the bytes as a
// JSON value instead of base64 text.
func isRawJSON(value reflect.Value) bool {
	return value.Type() == rawJSONType || value.Type() == stdRawJSONType
}

// unsupportedActivityOutputMarshaler rejects arbitrary JSON and text encoders
// because their output can be unrelated to reflected fields and cannot be
// bounded without first allocating the encoded value. model.Message is handled
// structurally; interface overhead covers its generated part discriminators.
func unsupportedActivityOutputMarshaler(value reflect.Value) (string, bool) {
	typ := value.Type()
	if typ == modelMessageType || (typ.Kind() == reflect.Pointer && typ.Elem() == modelMessageType) {
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

// base64JSONBytes returns quotes plus the padded base64 length used for an
// ordinary byte slice.
func base64JSONBytes(length int) int {
	if length > (maxPlanActivityOutputBytes/4)*3 {
		return maxPlanActivityOutputBytes + 1
	}
	return 2 + 4*((length+2)/3)
}

// jsonFieldName returns the name encoding/json uses for a struct field.
func jsonFieldName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "" {
		return field.Name
	}
	return name
}

// newPlanActivityOutputBudgetError formats one bounded internal rejection.
func newPlanActivityOutputBudgetError(format string, args ...any) error {
	return &planActivityOutputBudgetError{reason: fmt.Sprintf(format, args...)}
}
