package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestLimitFinalizationActivityOutputRoundTripsEveryAction(t *testing.T) {
	tests := []struct {
		name        string
		output      *LimitFinalizationActivityOutput
		disposition LimitFinalizationDisposition
		hasPlan     bool
	}{
		{
			name: "terminal plan",
			output: TerminalLimitFinalizationActivityOutput(planner.PlanResult{
				FinalResponse: &planner.FinalResponse{},
			}),
			disposition: LimitFinalizationDispositionTerminalPlan,
			hasPlan:     true,
		},
		{
			name:        "saved messages required",
			output:      HistoryRequiredLimitFinalizationActivityOutput(),
			disposition: LimitFinalizationDispositionHistoryRequired,
		},
		{
			name:        "session ended",
			output:      SessionEndedLimitFinalizationActivityOutput(),
			disposition: LimitFinalizationDispositionSessionEnded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.output)
			require.NoError(t, err)

			var decoded LimitFinalizationActivityOutput
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, test.disposition, decoded.Disposition())
			if test.hasPlan {
				require.NotNil(t, decoded.TerminalPlan())
			} else {
				require.Nil(t, decoded.TerminalPlan())
			}
		})
	}
}

func TestLimitFinalizationActivityOutputRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		want   string
		direct bool
	}{
		{
			name: "missing action",
			data: `{}`,
			want: "unknown limit finalization disposition",
		},
		{
			name: "unknown action",
			data: `{"disposition":"retry"}`,
			want: "unknown limit finalization disposition",
		},
		{
			name: "terminal plan missing result",
			data: `{"disposition":"terminal_plan"}`,
			want: "missing its plan",
		},
		{
			name: "saved messages action contains result",
			data: `{"disposition":"history_required","result":{}}`,
			want: "contains a terminal plan",
		},
		{
			name: "session ended action contains result",
			data: `{"disposition":"session_ended","result":{}}`,
			want: "contains a terminal plan",
		},
		{
			name: "unknown field",
			data: `{"disposition":"history_required","retry":true}`,
			want: "unknown field",
		},
		{
			name:   "multiple values",
			data:   `{"disposition":"history_required"} {}`,
			want:   "multiple JSON values",
			direct: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output LimitFinalizationActivityOutput
			var err error
			if test.direct {
				err = output.UnmarshalJSON([]byte(test.data))
			} else {
				err = json.Unmarshal([]byte(test.data), &output)
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestLimitFinalizationActivityOutputRejectsInvalidGoValues(t *testing.T) {
	var missing *LimitFinalizationActivityOutput
	_, err := missing.MarshalJSON()
	require.ErrorContains(t, err, "output is required")

	tests := []struct {
		name   string
		output *LimitFinalizationActivityOutput
		want   string
	}{
		{
			name: "terminal plan missing result",
			output: &LimitFinalizationActivityOutput{
				disposition: LimitFinalizationDispositionTerminalPlan,
			},
			want: "missing its plan",
		},
		{
			name: "saved messages action contains result",
			output: &LimitFinalizationActivityOutput{
				disposition: LimitFinalizationDispositionHistoryRequired,
				result:      &planner.PlanResult{},
			},
			want: "contains a terminal plan",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := json.Marshal(test.output)
			require.ErrorContains(t, err, test.want)
		})
	}
}
