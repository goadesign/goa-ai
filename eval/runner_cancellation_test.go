// These tests cancel assessment after a successful scenario and verify that
// callers can save the completed evidence alongside the cancellation error.
package eval

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// cancelOnFinishedReporter cancels the assessment after its last scenario
	// completes, preserving that successful scenario for the report assertions.
	cancelOnFinishedReporter struct {
		cancel   context.CancelFunc
		finished []ScenarioReport
	}
)

func TestAssessmentLateCancellationPreservesSaveableResults(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reporter := &cancelOnFinishedReporter{cancel: cancel}
	runner, err := NewRunner(nil, RunnerConfig{MaxConcurrency: 1, Reporter: reporter})
	require.NoError(t, err)
	suite := Suite{ID: "cancelled", Scenarios: []Scenario{exactScenario("completed")}}
	archive, err := runner.Capture(ctx, suite)
	require.NoError(t, err)

	report, err := runner.Assess(ctx, suite, archive)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, report.Passed)
	assert.Equal(t, context.Canceled.Error(), report.Error)
	require.Len(t, report.Scenarios, 1)
	assert.True(t, report.Scenarios[0].Passed)
	assert.Equal(t, reporter.finished, report.Scenarios)
	require.NoError(t, report.Validate())

	var saved bytes.Buffer
	_, err = report.WriteTo(&saved)
	require.NoError(t, err)
	loaded, err := ReadReports(&saved)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, report.ID, loaded[0].ID)
	require.Len(t, loaded[0].Scenarios, 1)
	assert.Equal(t, report.Scenarios[0].ID, loaded[0].Scenarios[0].ID)
	assert.Equal(t, report.Scenarios[0].ObservationID, loaded[0].Scenarios[0].ObservationID)
	assert.Equal(t, report.Scenarios[0].Checks, loaded[0].Scenarios[0].Checks)
	assert.True(t, report.Scenarios[0].StartedAt.Equal(loaded[0].Scenarios[0].StartedAt))
	assert.True(t, loaded[0].Scenarios[0].Passed)
	assert.False(t, loaded[0].Passed)
}

func (*cancelOnFinishedReporter) ScenarioStarted(string, time.Time) {}

func (r *cancelOnFinishedReporter) ScenarioFinished(report ScenarioReport) {
	r.finished = append(r.finished, report)
	r.cancel()
}
