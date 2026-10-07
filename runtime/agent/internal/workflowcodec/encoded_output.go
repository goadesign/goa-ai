package workflowcodec

// This file counts the conservative JSON size of activity outputs and sibling
// arguments before allocation. Runtime output checks and the planner-output
// converter share the walk, including provider facts and rendered diagnostics.

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	encodedOutputBudget struct {
		bytes  int
		visits int
		active workflowJSONContainers
	}
)

const (
	maxJSONTypeEnvelopeBytes = 64
	maxJSONFloatBytes        = 32
	maxJSONIntegerBytes      = 20
)

// AddEncodedSource counts a conservative JSON size without allocating JSON.
// Calls share this budget's byte and visit totals, so an output and its later
// event records cannot independently spend the complete activity allowance.
func (b *Budget) AddEncodedSource(values ...any) error {
	encoded := &encodedOutputBudget{bytes: b.used, visits: b.sourceVisits}
	for _, value := range values {
		if err := encoded.walk(reflect.ValueOf(value), 0, false); err != nil {
			return err
		}
	}
	b.used, b.sourceVisits = encoded.bytes, encoded.visits
	return nil
}

// walk visits typed values without converting interfaces or allocating map-key
// snapshots. Every charge is at least the largest JSON representation the
// corresponding supported Go value can produce.
func (b *encodedOutputBudget) walk(value reflect.Value, depth int, quoted bool) error {
	if depth > maxWorkflowJSONDepth {
		return fmt.Errorf(
			"planner activity output exceeds maximum depth %d",
			maxWorkflowJSONDepth,
		)
	}
	if err := b.visit(); err != nil {
		return err
	}
	if !value.IsValid() {
		return b.addBytes(len("null"))
	}
	if value.Type() == reflect.TypeFor[api.PlanActivityOutput]() {
		value = reflect.ValueOf(*planOutputView(value.Interface().(api.PlanActivityOutput)))
	}
	if value.Type() == reflect.TypeFor[planOutputRecord]() {
		record := value.Interface().(planOutputRecord)
		if err := record.validate(); err != nil {
			return err
		}
	}
	if isRawJSON(value) {
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		if !json.Valid(value.Bytes()) {
			return fmt.Errorf(
				"planner activity output contains invalid raw JSON",
			)
		}
		if err := b.addBytes(value.Len()); err != nil {
			return err
		}
		// encoding/json escapes HTML and line separators even inside a raw
		// JSON value. Count that expansion before it copies the saved bytes.
		raw := value.Bytes()
		for index := 0; index < len(raw); {
			char, size := utf8.DecodeRune(raw[index:])
			index += size
			switch char {
			case '<', '>', '&':
				if err := b.addBytes(5); err != nil {
					return err
				}
			case '\u2028', '\u2029':
				if err := b.addBytes(3); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if isByteCollection(value) {
		if value.Kind() == reflect.Slice && value.IsNil() {
			return b.addBytes(len("null"))
		}
		return b.addBytes(base64JSONBytes(value.Len()))
	}
	if marshalerKind, unsupported := unsupportedWorkflowJSONMarshaler(value.Type()); unsupported {
		return fmt.Errorf(
			"planner activity output contains custom %s marshaler %s whose encoded size cannot be bounded without serialization",
			marshalerKind,
			value.Type(),
		)
	}

	container, tracked, err := b.active.enter(value)
	if err != nil {
		return fmt.Errorf("planner activity output %w", err)
	}
	if tracked {
		defer delete(b.active, container)
	}
	scalarQuotes := 0
	if quoted {
		scalarQuotes = 2
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		if err := b.addBytes(maxJSONTypeEnvelopeBytes); err != nil {
			return err
		}
		return b.walk(value.Elem(), depth+1, quoted)
	case reflect.Pointer:
		if value.IsNil() {
			return b.addBytes(len("null"))
		}
		return b.walk(value.Elem(), depth+1, quoted)
	case reflect.Struct:
		if err := b.addBytes(2); err != nil {
			return err
		}
		if err := b.checkChildren(value.NumField()); err != nil {
			return err
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.Tag.Get("json") == "-" {
				continue
			}
			if field.PkgPath != "" && !isEmbeddedWorkflowJSONStruct(field) {
				continue
			}
			// Counting every eligible field also bounds fields that omitempty,
			// omitzero, or embedded-field selection would leave out of JSON.
			name, fieldQuoted := jsonFieldEncoding(field)
			if err := b.addJSONString(false, name); err != nil {
				return err
			}
			if err := b.addBytes(2); err != nil {
				return err
			}
			if value.Type() == reflect.TypeFor[providerFailureRecord]() && field.Name == "Diagnostic" && value.Field(index).String() == "" {
				failure := value.Interface().(providerFailureRecord)
				if err := b.visit(); err != nil {
					return err
				}
				if err := b.addJSONString(false, failure.diagnosticParts()...); err != nil {
					return err
				}
				continue
			}
			if err := b.walk(value.Field(index), depth+1, fieldQuoted); err != nil {
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
			if err := b.walk(value.Index(index), depth+1, false); err != nil {
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
			if err := b.walk(iter.Value(), depth+1, false); err != nil {
				return err
			}
		}
	case reflect.String:
		return b.addJSONString(quoted, value.String())
	case reflect.Bool:
		return b.addBytes(len("false") + scalarQuotes)
	case reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64:
		return b.addBytes(maxJSONIntegerBytes + scalarQuotes)
	case reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr:
		return b.addBytes(maxJSONIntegerBytes + scalarQuotes)
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf(
				"planner activity output contains non-finite number",
			)
		}
		return b.addBytes(maxJSONFloatBytes + scalarQuotes)
	case reflect.Invalid:
		return b.addBytes(len("null"))
	case reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.UnsafePointer:
		return fmt.Errorf(
			"planner activity output contains unsupported JSON value %s",
			value.Kind(),
		)
	}
	return nil
}

// visit charges one reflected value before descending into it.
func (b *encodedOutputBudget) visit() error {
	b.visits++
	if b.visits > maxWorkflowJSONVisits {
		return fmt.Errorf(
			"planner activity output exceeds maximum visited values %d",
			maxWorkflowJSONVisits,
		)
	}
	return nil
}

// addBytes charges a proven encoded-size upper bound without integer overflow.
func (b *encodedOutputBudget) addBytes(count int) error {
	if count < 0 || count > engine.MaxPayloadBytes-b.bytes {
		return fmt.Errorf(
			"planner activity output exceeds conservative encoded-size bound %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	b.bytes += count
	return nil
}

// addJSONString counts JSON string bytes directly from the original text. For a
// ,string field, encoding/json quotes the escaped string again: its surrounding
// quotes and each escape need another escape. Neither pass allocates text here.
// Diagnostic pieces use the ordinary case before the converter joins them.
func (b *encodedOutputBudget) addJSONString(quoted bool, parts ...string) error {
	quotes := 2
	if quoted {
		quotes = 6
	}
	if err := b.addBytes(quotes); err != nil {
		return err
	}
	for _, value := range parts {
		for index := 0; index < len(value); {
			char := value[index]
			if char < utf8.RuneSelf {
				index++
				switch char {
				case '"', '\\':
					size := 2
					if quoted {
						size = 4
					}
					if err := b.addBytes(size); err != nil {
						return err
					}
				case '\b', '\f', '\n', '\r', '\t':
					size := 2
					if quoted {
						size++
					}
					if err := b.addBytes(size); err != nil {
						return err
					}
				case '<', '>', '&':
					size := 6
					if quoted {
						size++
					}
					if err := b.addBytes(size); err != nil {
						return err
					}
				default:
					size := 1
					if char < 0x20 {
						size = 6
						if quoted {
							size++
						}
					}
					if err := b.addBytes(size); err != nil {
						return err
					}
				}
				continue
			}
			r, size := utf8.DecodeRuneInString(value[index:])
			index += size
			if (r == utf8.RuneError && size == 1) || r == '\u2028' || r == '\u2029' {
				escapedSize := 6
				if quoted {
					escapedSize++
				}
				if err := b.addBytes(escapedSize); err != nil {
					return err
				}
				continue
			}
			if err := b.addBytes(size); err != nil {
				return err
			}
		}
	}
	return nil
}

// addCollectionBytes charges braces or brackets and every possible comma.
func (b *encodedOutputBudget) addCollectionBytes(length int) error {
	if err := b.addBytes(2); err != nil {
		return err
	}
	if length > 1 {
		return b.addBytes(length - 1)
	}
	return nil
}

// addMapKey charges the quoted JSON spelling of one supported map key.
func (b *encodedOutputBudget) addMapKey(key reflect.Value) error {
	switch key.Kind() {
	case reflect.String:
		if key.Type().Implements(textMarshalerType) ||
			(reflect.PointerTo(key.Type()).Implements(textMarshalerType)) {
			return fmt.Errorf(
				"planner activity output contains custom text map key %s whose encoded size cannot be bounded without serialization",
				key.Type(),
			)
		}
		return b.addJSONString(false, key.String())
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
		return fmt.Errorf(
			"planner activity output contains unsupported JSON map key %s",
			key.Type(),
		)
	default:
		return fmt.Errorf(
			"planner activity output contains unsupported JSON map key %s",
			key.Type(),
		)
	}
}

// checkChildren rejects large collections before walking their elements.
func (b *encodedOutputBudget) checkChildren(counts ...int) error {
	remaining := maxWorkflowJSONVisits - b.visits
	for _, count := range counts {
		if count < 0 || count > remaining {
			return fmt.Errorf(
				"planner activity output exceeds maximum visited values %d",
				maxWorkflowJSONVisits,
			)
		}
		remaining -= count
	}
	return nil
}

// isByteCollection identifies slices encoded as base64 strings. Byte arrays
// remain JSON arrays and are counted element by element.
func isByteCollection(value reflect.Value) bool {
	return value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8
}

// isRawJSON identifies byte slices whose JSON marshaler emits the bytes as a
// JSON value instead of base64 text.
func isRawJSON(value reflect.Value) bool {
	return value.Type() == rawJSONMessageType || value.Type() == standardRawJSONType
}

// base64JSONBytes returns quotes plus the padded base64 length used for an
// ordinary byte slice.
func base64JSONBytes(length int) int {
	if length > (engine.MaxPayloadBytes/4)*3 {
		return engine.MaxPayloadBytes + 1
	}
	return 2 + 4*((length+2)/3)
}

// jsonFieldEncoding follows encoding/json's field-name and ,string rules.
// Invalid names use the Go name. Only primitive fields and their unnamed
// pointer forms can request a quoted value; other field types ignore ,string.
func jsonFieldEncoding(field reflect.StructField) (string, bool) {
	name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
	for _, char := range name {
		if !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", char) &&
			!unicode.IsLetter(char) && !unicode.IsDigit(char) {
			name = ""
			break
		}
	}
	if name == "" {
		name = field.Name
	}
	typ := field.Type
	if typ.Name() == "" && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for option := range strings.SplitSeq(options, ",") {
		if option != "string" {
			continue
		}
		switch typ.Kind() {
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64, reflect.String:
			return name, true
		case reflect.Invalid, reflect.Complex64, reflect.Complex128,
			reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
			reflect.Pointer, reflect.Slice, reflect.Struct, reflect.UnsafePointer:
			return name, false
		}
	}
	return name, false
}
