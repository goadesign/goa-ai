// The runner forwards scenario reference evidence separately from candidate
// output. Calibration and empty-output handling retain their existing meaning.
package eval

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerPassesSharedReferenceAndPreservesReport(t *testing.T) {
	const reference = "  Reference facts: 12.5 °C\n"
	const output = "Candidate without a measurement."
	judge := &scriptedJudge{
		responses: [][]Judgment{calibrationJudgments(), {{ClaimID: "reading", Label: NotAddressed, Rationale: "The candidate omits the reading."}}},
		errors:    []error{nil, nil},
	}
	suite := Suite{ID: "references", Scenarios: []Scenario{{
		ID: "reading", Timeout: time.Second,
		Run: func(context.Context) (Result, error) {
			return Result{Output: output, Reference: reference, Claims: []Claim{{ID: "reading", Text: "The candidate states the reading."}}}, nil
		},
	}}}
	report, err := mustRunner(t, judge, 1).Run(t.Context(), suite)
	require.NoError(t, err)
	require.Len(t, judge.requests, 2)
	assert.Empty(t, judge.requests[0].reference, "calibration has no reference")
	assert.Equal(t, reference, judge.requests[1].reference)
	assert.Equal(t, output, judge.requests[1].output)
	assert.Equal(t, reference, report.Scenarios[0].Result.Reference)
	assert.Equal(t, output, report.Scenarios[0].Result.Output)
	encoded, err := json.Marshal(report.Scenarios[0].Result)
	require.NoError(t, err)
	var decoded Result
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, *report.Scenarios[0].Result, decoded)
}

func TestResultWithoutReferenceRetainsJSONShape(t *testing.T) {
	encoded, err := json.Marshal(Result{Output: "Candidate."})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"reference"`)
	var decoded Result
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Empty(t, decoded.Reference)
}
