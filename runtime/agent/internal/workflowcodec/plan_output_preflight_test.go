package workflowcodec

// These tests combine a provider certificate with other converter arguments.
// Each argument fits alone, but their encoded bytes must be checked together
// before the converter renders the diagnostic or allocates any payload.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// observedPayloadEncoder counts calls after the combined preflight so tests
	// can prove rejection happened before any payload encoder ran.
	observedPayloadEncoder struct {
		converter.DataConverter
		calls int
	}

	preflightTaggedText struct {
		Text string `json:"text,string"`
	}

	preflightNamedString string

	preflightNamedPointer *string

	preflightNamedBoolPointer *bool

	preflightNamedIntegerPointer *int64

	preflightNamedFloatPointer *float64

	preflightTaggedPointer struct {
		Text preflightNamedPointer `json:"text,string"`
	}

	preflightEmbeddedFields struct {
		Text string `json:"text,string"`
	}
)

const (
	taggedPreflightString  = "tagged string"
	taggedPreflightPointer = "tagged named pointer"
)

func TestPlanOutputCertificateBoundsEscapedSiblingsBeforeEncoding(t *testing.T) {
	for _, name := range []string{"string", "nested metadata", taggedPreflightString, taggedPreflightPointer} {
		t.Run(name, func(t *testing.T) {
			output := certificateOutput()
			output.PublicationBatchID = "12345678-1234-4234-8234-123456789abc"
			output.ProviderFailure = model.NewProviderError("synthetic", "complete", 503,
				model.ProviderErrorKindUnavailable, "capacity", strings.Repeat("x", 100000),
				"request-1", true, nil)
			length := 150000
			if name == taggedPreflightString || name == taggedPreflightPointer {
				length = 130000
			}
			sibling := escapedPreflightSibling(name, strings.Repeat("\x00", length))
			codec := NewDataConverter()
			outputPayload, err := codec.ToPayload(output)
			require.NoError(t, err)
			siblingPayload, err := codec.ToPayload(sibling)
			require.NoError(t, err)
			largestSiblingPayload := siblingPayload
			if name == taggedPreflightPointer {
				// A named pointer may use either ordinary or quoted string JSON.
				// The primitive field supplies the larger supported encoding, so
				// this rejection must hold with either encoding/json implementation.
				largestSiblingPayload, err = codec.ToPayload(preflightTaggedText{
					Text: strings.Repeat("\x00", length),
				})
				require.NoError(t, err)
				assert.LessOrEqual(t, len(siblingPayload.Data), len(largestSiblingPayload.Data))
				assert.Len(t, largestSiblingPayload.Data, 910015)
			}
			if name == "string" {
				assert.Len(t, siblingPayload.Data, 900002)
			}
			if name == taggedPreflightString {
				assert.Len(t, siblingPayload.Data, 910015)
				assert.Greater(t, len(outputPayload.Data)+len(siblingPayload.Data), 1110015)
			}
			assert.Greater(t, len(outputPayload.Data)+len(largestSiblingPayload.Data), 1100000)
			assert.Less(t, len(outputPayload.Data), engine.MaxPayloadBytes)
			assert.Less(t, len(siblingPayload.Data), engine.MaxPayloadBytes)

			for _, certificateFirst := range []bool{true, false} {
				record := planOutputView(output)
				values := []any{record, sibling}
				if !certificateFirst {
					values[0], values[1] = values[1], values[0]
				}
				require.NoError(t, new(Budget).AddSource(values...))
				require.ErrorContains(t, preflightValues(values...), "conservative encoded-size")
				assert.Empty(t, record.ProviderFailure.Diagnostic)

				values = []any{output, sibling}
				if !certificateFirst {
					values[0], values[1] = values[1], values[0]
				}
				owned := NewDataConverter().(*dataConverter)
				observed := &observedPayloadEncoder{DataConverter: owned.inner}
				owned.inner = observed
				_, err = owned.ToPayloads(values...)
				require.ErrorContains(t, err, "conservative encoded-size")
				assert.Zero(t, observed.calls)
			}

			sibling = escapedPreflightSibling(name, strings.Repeat("\x00", 100000))
			combined, err := codec.ToPayloads(output, sibling)
			require.NoError(t, err)
			smallerPayload, err := codec.ToPayload(sibling)
			require.NoError(t, err)
			assert.True(t, proto.Equal(outputPayload, combined.Payloads[0]))
			assert.True(t, proto.Equal(smallerPayload, combined.Payloads[1]))
		})
	}
}

func TestPlanOutputCertificateJSONFieldOptions(t *testing.T) {
	escaped := "\"\\\b\f\n\r\t\x00<>&\u2028\u2029世界"
	boolean, integer, floating := true, int64(math.MinInt64), -math.MaxFloat64
	variants := []any{
		true, false, int(math.MinInt), int8(math.MinInt8), int16(math.MinInt16),
		int32(math.MinInt32), int64(math.MinInt64), uint(math.MaxUint), uint8(math.MaxUint8),
		uint16(math.MaxUint16), uint32(math.MaxUint32), uint64(math.MaxUint64), ^uintptr(0),
		float32(math.SmallestNonzeroFloat32), -math.MaxFloat64, float64(0),
		escaped, "", preflightNamedString(escaped), json.Number("1e100000"), json.Number(""),
		[]string{escaped}, [1]string{escaped}, map[string]string{"text": escaped},
		preflightNamedPointer(&escaped), preflightNamedBoolPointer(&boolean),
		preflightNamedIntegerPointer(&integer), preflightNamedFloatPointer(&floating),
		preflightTaggedText{Text: escaped},
	}
	for _, value := range variants {
		for _, pointer := range []string{"value", "pointer", "nil pointer", "double pointer"} {
			for _, tag := range []string{
				"text", "text,string", "text,string,omitempty", "text,omitzero,string",
				"text,string,unknown,string", "-", "-,string", ",string", "\x7f,string",
			} {
				t.Run(fmt.Sprintf("%T/%s/%s", value, pointer, tag), func(t *testing.T) {
					fieldValue := reflect.ValueOf(value)
					if pointer == "nil pointer" {
						fieldValue = reflect.Zero(reflect.PointerTo(fieldValue.Type()))
					} else if pointer != "value" {
						address := reflect.New(fieldValue.Type())
						address.Elem().Set(fieldValue)
						fieldValue = address
						if pointer == "double pointer" {
							address = reflect.New(fieldValue.Type())
							address.Elem().Set(fieldValue)
							fieldValue = address
						}
					}
					typ := reflect.StructOf([]reflect.StructField{{
						Name: "LongerDefaultFieldName", Type: fieldValue.Type(),
						Tag: reflect.StructTag("json:" + strconv.Quote(tag)),
					}})
					envelope := reflect.New(typ).Elem()
					envelope.Field(0).Set(fieldValue)
					assertPreflightJSONBound(t, envelope.Interface())
				})
			}
		}
	}
	interfaceType := reflect.StructOf([]reflect.StructField{{
		Name: "Value", Type: reflect.TypeFor[any](), Tag: reflect.StructTag(`json:"value,string"`),
	}})
	interfaceValue := reflect.New(interfaceType).Elem()
	interfaceValue.Field(0).Set(reflect.ValueOf(escaped))
	for _, value := range []any{
		interfaceValue.Interface(),
		struct {
			preflightEmbeddedFields `json:"named_embedded_fields"`
		}{preflightEmbeddedFields{Text: escaped}},
		struct {
			preflightEmbeddedFields
		}{preflightEmbeddedFields{Text: escaped}},
	} {
		assertPreflightJSONBound(t, value)
	}
}

func TestPlanOutputCertificateQuotedStringCountMatchesJSON(t *testing.T) {
	ascii := make([]byte, 128)
	for index := range ascii {
		ascii[index] = byte(index)
	}
	for _, text := range []string{"", string(ascii), "\u2028\u2029世界😀", strings.Repeat("\x00", 130000)} {
		once, err := json.Marshal(text)
		require.NoError(t, err)
		twice, err := json.Marshal(string(once))
		require.NoError(t, err)
		for _, quoted := range []bool{false, true} {
			var budget encodedOutputBudget
			require.NoError(t, budget.addJSONString(quoted, text))
			expected := once
			if quoted {
				expected = twice
			}
			assert.Equal(t, len(expected), budget.bytes)
		}
	}
}

func TestPlanOutputCertificateInvalidStringCountBoundsJSON(t *testing.T) {
	for _, text := range []string{"\xff", "\xfe\xff", "before\x80after", "\xc0\xaf", "世界\xf0\x9f"} {
		once, err := json.Marshal(text)
		require.NoError(t, err)
		twice, err := json.Marshal(string(once))
		require.NoError(t, err)
		for _, quoted := range []bool{false, true} {
			var budget encodedOutputBudget
			require.NoError(t, budget.addJSONString(quoted, text))
			encoded := once
			if quoted {
				encoded = twice
			}
			// JSON encoders may emit the replacement rune directly or escaped.
			// The size check must cover either spelling; source preflight still
			// rejects the original invalid text before an encoder can run.
			assert.GreaterOrEqual(t, budget.bytes, len(encoded))
		}
		require.ErrorContains(t, new(Budget).AddSource(text), "invalid UTF-8")
	}
}

func TestPlanOutputCertificatePreservesSiblingRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "nil"},
		{name: "typed nil", value: (*string)(nil)},
		{name: "binary bytes", value: []byte{0, 1, 255}},
		{name: "named bytes", value: workflowByteSlice{0, 1, 255}},
		{name: "byte array", value: [4]byte{255, 255, 255, 255}},
		{name: "protobuf", value: &commonpb.WorkflowType{Name: "synthetic"}},
		{name: "gogo protobuf", value: &gogotypes.BytesValue{Value: []byte("synthetic")}},
		{name: "raw JSON", value: rawjson.Message(`"<>&世界"`)},
		{name: "raw payload", value: converter.NewRawValue(&commonpb.Payload{
			Data: []byte("synthetic"), Metadata: map[string][]byte{"encoding": []byte("binary/plain")},
		})},
		{name: "versioned result", value: &api.RunOutput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := NewDataConverter()
			alone, err := codec.ToPayload(tc.value)
			require.NoError(t, err)
			combined, err := codec.ToPayloads(certificateOutput(), tc.value)
			require.NoError(t, err)
			assert.True(t, proto.Equal(alone, combined.Payloads[1]))
			var exact, bounded Budget
			require.NoError(t, exact.AddPayload(alone))
			require.NoError(t, bounded.addEncodedPayload(tc.value))
			assert.GreaterOrEqual(t, bounded.used, exact.used)
		})
	}
}

func TestPlanOutputCertificatePreservesSiblingSourceGuards(t *testing.T) {
	for _, sibling := range []any{
		converter.NewRawValue(nil),
		hidingJSONMarshaler{},
		"\xff",
		map[int]string{1: "invalid key"},
		rawjson.Message(`{"invalid"`),
	} {
		owned := NewDataConverter().(*dataConverter)
		observed := &observedPayloadEncoder{DataConverter: owned.inner}
		owned.inner = observed
		_, err := owned.ToPayloads(certificateOutput(), sibling)
		require.Error(t, err)
		assert.Zero(t, observed.calls)
	}
}

func (encoder *observedPayloadEncoder) ToPayload(any) (*commonpb.Payload, error) {
	encoder.calls++
	return nil, errors.New("test encoder must not run for rejected arguments")
}

// escapedPreflightSibling puts identical text in supported converter inputs so
// aggregate tests can compare the real encoder's size and preserved bytes.
func escapedPreflightSibling(kind, text string) any {
	switch kind {
	case "nested metadata":
		return map[string]any{"metadata": map[string]any{"text": text}}
	case taggedPreflightString:
		return preflightTaggedText{Text: text}
	case taggedPreflightPointer:
		return preflightTaggedPointer{Text: &text}
	default:
		return text
	}
}

func assertPreflightJSONBound(t *testing.T, value any) {
	t.Helper()
	want, err := json.Marshal(value)
	require.NoError(t, err)
	var budget Budget
	require.NoError(t, budget.AddEncodedSource(value))
	assert.GreaterOrEqual(t, budget.used, len(want))
	codec := NewDataConverter()
	combined, err := codec.ToPayloads(certificateOutput(), value)
	require.NoError(t, err)
	assert.Equal(t, want, combined.Payloads[1].Data)
}
