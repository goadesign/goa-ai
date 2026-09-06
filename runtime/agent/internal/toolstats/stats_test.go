package toolstats

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestAccumulatorPreservesStatistics(t *testing.T) {
	for _, test := range []struct {
		name  string
		input []*telemetry.ToolTelemetry
		want  *telemetry.ToolTelemetry
	}{
		{"empty", nil, nil},
		{"nil and zero", []*telemetry.ToolTelemetry{nil, {}}, nil},
		{"same model", []*telemetry.ToolTelemetry{{TokensUsed: 2, DurationMs: 3, Model: "m", Extra: map[string]any{"private": "detail"}}, {TokensUsed: 4, DurationMs: 5, Model: "m"}}, &telemetry.ToolTelemetry{TokensUsed: 6, DurationMs: 8, Model: "m"}},
		{"mixed model", []*telemetry.ToolTelemetry{{Model: "a"}, {Model: "b"}, {Model: "a"}}, &telemetry.ToolTelemetry{}},
		{"empty model ignored", []*telemetry.ToolTelemetry{{Model: "a"}, {TokensUsed: 1}}, &telemetry.ToolTelemetry{TokensUsed: 1, Model: "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stats Accumulator
			for _, input := range test.input {
				require.NoError(t, stats.Add(input))
			}
			count, got := stats.Result()
			assert.Equal(t, len(test.input), count)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestAccumulatorRejectsOverflow(t *testing.T) {
	for _, test := range []struct {
		name  string
		start Accumulator
		input *telemetry.ToolTelemetry
	}{
		{"count", Accumulator{count: math.MaxInt}, nil},
		{"tokens", Accumulator{tokens: math.MaxInt}, &telemetry.ToolTelemetry{TokensUsed: 1}},
		{"duration", Accumulator{duration: math.MaxInt64}, &telemetry.ToolTelemetry{DurationMs: 1}},
		{"negative tokens", Accumulator{}, &telemetry.ToolTelemetry{TokensUsed: -1}},
		{"negative duration", Accumulator{}, &telemetry.ToolTelemetry{DurationMs: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := test.start
			require.Error(t, test.start.Add(test.input))
			assert.Equal(t, before, test.start)
		})
	}
}
