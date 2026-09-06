// These tests send scalar and list attributes through the Clue span adapter.
// They check both the private conversion and events recorded by OpenTelemetry.
package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type clueAttributeCase struct {
	name  string
	input any
	want  attribute.KeyValue
}

func TestClueAttributeValues(t *testing.T) {
	for _, test := range clueAttributeCases() {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []attribute.KeyValue{test.want}, kvSliceToAttrs([]any{"value", test.input}))
		})
	}
}

func TestClueSpanAddEventPreservesAttributeValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	tracer := &ClueTracer{tracer: provider.Tracer("clue-attribute-test")}
	_, span := tracer.Start(t.Context(), "record attributes")
	tests := clueAttributeCases()
	for _, test := range tests {
		span.AddEvent(test.name, "value", test.input)
	}
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	events := spans[0].Events()
	require.Len(t, events, len(tests))
	for index, test := range tests {
		assert.Equal(t, test.name, events[index].Name)
		assert.Equal(t, []attribute.KeyValue{test.want}, events[index].Attributes)
	}
}

func TestClueAttributeLegacyInputBehavior(t *testing.T) {
	tests := []struct {
		name  string
		input []any
		want  []attribute.KeyValue
	}{
		{name: "no attributes"},
		{name: "odd length", input: []any{"value"}, want: []attribute.KeyValue{attribute.String("value", "")}},
		{name: "nonstring key", input: []any{42, "text"}, want: []attribute.KeyValue{attribute.String("", "text")}},
		{name: "unsupported value", input: []any{"value", struct{}{}}, want: []attribute.KeyValue{attribute.String("value", "")}},
		{name: "nil value", input: []any{"value", nil}, want: []attribute.KeyValue{attribute.String("value", "")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, kvSliceToAttrs(test.input))
		})
	}
}

// clueAttributeCases covers each supported scalar and list type. Lists include
// repeated values, empty elements, and both empty and nil slices.
func clueAttributeCases() []clueAttributeCase {
	return []clueAttributeCase{
		{name: "string", input: "value", want: attribute.String("value", "value")},
		{name: "bool", input: true, want: attribute.Bool("value", true)},
		{name: "int", input: -7, want: attribute.Int("value", -7)},
		{name: "int64", input: int64(1 << 40), want: attribute.Int64("value", 1<<40)},
		{name: "float64", input: 1.25, want: attribute.Float64("value", 1.25)},
		{name: "strings", input: []string{"first", "", "first", "last"}, want: attribute.StringSlice("value", []string{"first", "", "first", "last"})},
		{name: "bools", input: []bool{true, false, true}, want: attribute.BoolSlice("value", []bool{true, false, true})},
		{name: "ints", input: []int{-7, 0, -7, 9}, want: attribute.IntSlice("value", []int{-7, 0, -7, 9})},
		{name: "int64s", input: []int64{1 << 40, -1, 1 << 40}, want: attribute.Int64Slice("value", []int64{1 << 40, -1, 1 << 40})},
		{name: "float64s", input: []float64{1.25, -2.5, 1.25}, want: attribute.Float64Slice("value", []float64{1.25, -2.5, 1.25})},
		{name: "empty strings", input: []string{}, want: attribute.StringSlice("value", []string{})},
		{name: "empty bools", input: []bool{}, want: attribute.BoolSlice("value", []bool{})},
		{name: "empty ints", input: []int{}, want: attribute.IntSlice("value", []int{})},
		{name: "empty int64s", input: []int64{}, want: attribute.Int64Slice("value", []int64{})},
		{name: "empty float64s", input: []float64{}, want: attribute.Float64Slice("value", []float64{})},
		{name: "nil strings", input: []string(nil), want: attribute.StringSlice("value", nil)},
		{name: "nil bools", input: []bool(nil), want: attribute.BoolSlice("value", nil)},
		{name: "nil ints", input: []int(nil), want: attribute.IntSlice("value", nil)},
		{name: "nil int64s", input: []int64(nil), want: attribute.Int64Slice("value", nil)},
		{name: "nil float64s", input: []float64(nil), want: attribute.Float64Slice("value", nil)},
	}
}
