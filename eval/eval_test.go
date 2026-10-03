// These tests exercise immutable capture, offline assessment, and exact coverage.
package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestValidateJudgments(t *testing.T) {
	claims := []Claim{{ID: "first", Text: "First."}, {ID: "second", Text: "Second."}}
	tests := []struct {
		name      string
		judgments []Judgment
		wantErr   string
	}{
		{
			name: "complete",
			judgments: []Judgment{
				{ClaimID: "second", Label: NotAddressed, Rationale: "Absent."},
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
			},
		},
		{
			name:      "missing",
			judgments: []Judgment{{ClaimID: "first", Label: Entailed, Rationale: "Explicit."}},
			wantErr:   "1 judgments for 2 claims",
		},
		{
			name: "unknown",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
				{ClaimID: "third", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "unknown claim",
		},
		{
			name: "duplicate",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "duplicate judgment",
		},
		{
			name: "invalid label",
			judgments: []Judgment{
				{ClaimID: "first", Label: "maybe", Rationale: "Unclear."},
				{ClaimID: "second", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "invalid label",
		},
		{
			name: "missing rationale",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed},
				{ClaimID: "second", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "requires a rationale",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateJudgments(claims, test.judgments)
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateJudgmentsRejectsDuplicateClaims(t *testing.T) {
	claims := []Claim{{ID: "claim", Text: "First."}, {ID: "claim", Text: "Second."}}

	err := ValidateJudgments(claims, []Judgment{{ClaimID: "claim", Label: Entailed, Rationale: "Explicit."}})

	assert.ErrorContains(t, err, "duplicate claim")
}

type (
	reasonRequest struct {
		subject   string
		claims    []Claim
		reference string
		conflicts []Disagreement
	}
	testReasoner struct {
		mu         sync.Mutex
		requests   []reasonRequest
		reason     func(context.Context, reasonRequest) (Reasoning, error)
		adjudicate func(context.Context, reasonRequest) (Reasoning, error)
	}
	testClassifier struct {
		mu       sync.Mutex
		requests []reasonRequest
		settings map[string]string
		classify func(context.Context, reasonRequest) (Classification, error)
	}
	recordingReporter struct {
		mu       sync.Mutex
		started  []string
		finished []ScenarioReport
	}
)

func TestCaptureThenOfflineAssessmentPreservesEvidence(t *testing.T) {
	var captures atomic.Int32
	scenario := testScenario("answer", Subject{Content: "Done.", Reference: "Captured facts."})
	capture := scenario.Capture
	scenario.Capture = func(ctx context.Context) (rawjson.Message, error) {
		captures.Add(1)
		return capture(ctx)
	}
	reasoner := &testReasoner{}
	engine, err := NewReasoningEngine(reasoner)
	require.NoError(t, err)
	runner := mustRunner(t, engine, 2)
	suite := Suite{ID: "suite", Scenarios: []Scenario{scenario}}
	archive, err := runner.Capture(t.Context(), suite)
	require.NoError(t, err)
	assert.EqualValues(t, 1, captures.Load())
	assert.Empty(t, reasoner.requests, "capture cannot invoke an assessor")
	var saved bytes.Buffer
	_, err = archive.WriteTo(&saved)
	require.NoError(t, err)
	loaded, err := ReadArchive(&saved)
	require.NoError(t, err)
	assert.Equal(t, archive.ID, loaded.ID)
	assert.Equal(t, archive.Observations[0].Data, loaded.Observations[0].Data)
	suite.Scenarios[0].Capture = nil
	for range 2 {
		report, err := runner.Assess(t.Context(), suite, loaded)
		require.NoError(t, err)
		assert.True(t, report.Passed)
		assert.Equal(t, archive.ID, report.ArchiveID)
		assert.Equal(t, archive.Observations[0].Duration, report.Scenarios[0].CaptureDuration)
	}
	assert.EqualValues(t, 1, captures.Load())
	require.Len(t, reasoner.requests, 2)
	for _, request := range reasoner.requests {
		assert.Equal(t, "Done.", request.subject)
		assert.Equal(t, "Captured facts.", request.reference)
		assert.Empty(t, request.conflicts)
	}
	assert.NoError(t, loaded.Validate())
}

func TestArchiveRejectsChangedEvidenceAndSchema(t *testing.T) {
	runner := mustRunner(t, nil, 1)
	suite := Suite{ID: "suite", Scenarios: []Scenario{exactScenario("exact")}}
	archive, err := runner.Capture(t.Context(), suite)
	require.NoError(t, err)
	altered := cloneRecord(archive)
	altered.Observations[0].Data = []byte(`"changed"`)
	require.ErrorContains(t, altered.Validate(), "content hash")
	suite.Scenarios[0].Schema = `{"type":"number"}`
	_, err = runner.Assess(t.Context(), suite, archive)
	assert.ErrorContains(t, err, "schema changed")
}

func TestBindingCannotDropAssertions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Binding)
	}{
		{"missing check", func(b *Binding) { b.Checks = nil }},
		{"renamed check", func(b *Binding) { b.Checks[0].Name = "other" }},
		{"inconsistent check", func(b *Binding) { b.Checks[0].Diagnostic = "failed" }},
		{"missing requirement", func(b *Binding) { b.Subjects = nil }},
		{"extra requirement", func(b *Binding) { b.Subjects["other"] = nil }},
		{"missing singleton", func(b *Binding) { b.Subjects["answer/complete"] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := testScenario("answer", Subject{Content: "Done."})
			bind := scenario.Bind
			scenario.Bind = func(data rawjson.Message) (Binding, error) {
				binding, err := bind(data)
				if err != nil {
					return Binding{}, err
				}
				test.change(&binding)
				return binding, nil
			}
			_, report, err := mustRunner(t, nil, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
			require.NoError(t, err)
			assert.False(t, report.Passed)
			assert.Contains(t, report.Scenarios[0].Error, "bind captured observation")
		})
	}
}

func TestEmptyOutputAndExactFailureNeverBecomePasses(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			subject := "Done."
			if empty {
				subject = ""
			}
			scenario := testScenario("answer", Subject{Content: subject, Reference: "This reference alone says the work is complete."})
			bind := scenario.Bind
			scenario.Bind = func(data rawjson.Message) (Binding, error) {
				binding, err := bind(data)
				if err != nil {
					return Binding{}, err
				}
				binding.Checks[0] = Check{Name: "exact", Diagnostic: "An exact invariant failed."}
				return binding, nil
			}
			reasoner := &testReasoner{}
			engine, err := NewReasoningEngine(reasoner)
			require.NoError(t, err)
			_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
			require.NoError(t, err)
			assert.False(t, report.Passed)
			assert.Empty(t, report.Scenarios[0].Error)
			label, ok := report.Scenarios[0].Requirements[0].Instances[0].Label()
			assert.True(t, ok)
			if empty {
				assert.Equal(t, NotAddressed, label)
				assert.Empty(t, reasoner.requests)
			} else {
				assert.Equal(t, Entailed, label)
				assert.Len(t, reasoner.requests, 1)
			}
		})
	}
}

func TestQuantifiedEmptyCollectionIsVisible(t *testing.T) {
	scenario := testScenario("answer", Subject{})
	scenario.Requirements[0].ForEach = "items"
	scenario.Bind = func(rawjson.Message) (Binding, error) {
		return Binding{Checks: []Check{{Name: "exact", Passed: true}}, Subjects: map[string][]Subject{"answer/complete": {}}}, nil
	}
	engine, err := NewReasoningEngine(&testReasoner{})
	require.NoError(t, err)
	_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
	require.NoError(t, err)
	assert.True(t, report.Passed)
	require.Len(t, report.Scenarios[0].Requirements, 1)
	assert.Empty(t, report.Scenarios[0].Requirements[0].Instances)
}

func TestRunnerSelectionAndSerialOrdering(t *testing.T) {
	runner := mustRunner(t, nil, 1)
	suite := Suite{ID: "suite", Scenarios: []Scenario{exactScenario("first"), exactScenario("second"), exactScenario("third")}}
	suite.Scenarios[0].Tags = []string{"smoke"}
	suite.Scenarios[1].Tags = []string{"full"}
	suite.Scenarios[2].Tags = []string{"smoke", "full"}
	for _, test := range []struct {
		name string
		ids  []string
		want []string
		bad  bool
	}{
		{"declaration order", []string{"third", "first"}, []string{"first", "third"}, false},
		{"empty", nil, nil, true},
		{"duplicate", []string{"first", "first"}, nil, true},
		{"unknown", []string{"missing"}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive, report, err := runner.RunScenarios(t.Context(), suite, test.ids...)
			if test.bad {
				require.Error(t, err)
				assert.Empty(t, archive.Observations)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, reportIDs(report))
			assert.True(t, report.Passed)
		})
	}
	archive, report, err := runner.RunTags(t.Context(), suite, "full")
	require.NoError(t, err)
	assert.Equal(t, []string{"second", "third"}, reportIDs(report))
	require.Len(t, archive.Observations, 2)
	assert.Equal(t, "second", archive.Observations[0].ScenarioID)
	assert.Equal(t, "third", archive.Observations[1].ScenarioID)
	for _, tags := range [][]string{nil, {"smoke", "smoke"}, {"unknown"}} {
		_, _, err := runner.RunTags(t.Context(), suite, tags...)
		require.Error(t, err)
	}
	assert.Equal(t, []int{0, 1, 2}, scenarioSchedule(suite.Scenarios, 1))
	suite.Scenarios[1].Timeout *= 3
	suite.Scenarios[2].Timeout *= 2
	assert.Equal(t, []int{1, 2, 0}, scenarioSchedule(suite.Scenarios, 2))
}

func TestBoundedConcurrencyAndCancellation(t *testing.T) {
	var active, peak, captures atomic.Int32
	started := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(t.Context())
	scenarios := make([]Scenario, 6)
	for i := range scenarios {
		scenarios[i] = exactScenario(fmt.Sprintf("case%d", i))
		scenarios[i].Capture = func(ctx context.Context) (rawjson.Message, error) {
			captures.Add(1)
			current := active.Add(1)
			for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
			}
			started <- struct{}{}
			<-ctx.Done()
			active.Add(-1)
			return nil, ctx.Err()
		}
	}
	reporter := &recordingReporter{}
	runner, err := NewRunner(nil, RunnerConfig{MaxConcurrency: 2, Reporter: reporter})
	require.NoError(t, err)
	type finished struct {
		archive Archive
		report  Report
		err     error
	}
	result := make(chan finished, 1)
	go func() {
		archive, report, err := runner.Run(ctx, Suite{ID: "suite", Scenarios: scenarios})
		result <- finished{archive, report, err}
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("captures did not start")
		}
	}
	cancel()
	completed := <-result
	require.ErrorIs(t, completed.err, context.Canceled)
	assert.EqualValues(t, 2, peak.Load())
	assert.EqualValues(t, 2, captures.Load())
	require.Len(t, completed.archive.Observations, 6)
	require.NoError(t, completed.archive.Validate())
	assert.Len(t, reporter.finished, 6)
	assert.Empty(t, reporter.started)
	for _, scenario := range completed.report.Scenarios {
		assert.False(t, scenario.Passed)
		assert.NotEmpty(t, scenario.Error)
	}
}

func TestAssessmentHasOwnDeadlineAndPreservesPartialOutcomes(t *testing.T) {
	reasoner := &testReasoner{reason: func(ctx context.Context, _ reasonRequest) (Reasoning, error) {
		_, exists := ctx.Deadline()
		assert.True(t, exists)
		<-ctx.Done()
		return Reasoning{}, ctx.Err()
	}}
	engine, err := NewReasoningEngine(reasoner)
	require.NoError(t, err)
	slow := testScenario("slow", Subject{Content: "Done."})
	slow.Timeout = 10 * time.Millisecond
	_, report, err := mustRunner(t, engine, 2).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{slow, exactScenario("exact")}})
	require.NoError(t, err)
	assert.False(t, report.Passed)
	assert.Contains(t, report.Scenarios[0].Error, "deadline exceeded")
	assert.True(t, report.Scenarios[1].Passed)
	assert.GreaterOrEqual(t, report.Scenarios[0].Duration, slow.Timeout)
	assert.NotEmpty(t, report.Scenarios[0].Checks)
}

func TestRunnerRejectsInvalidConfigurationAndReportsCaptureErrors(t *testing.T) {
	_, err := NewRunner(nil, RunnerConfig{})
	require.ErrorContains(t, err, "concurrency")
	runner := mustRunner(t, nil, 1)
	_, _, err = runner.Run(t.Context(), Suite{})
	require.Error(t, err)
	broken := exactScenario("broken")
	broken.Capture = func(context.Context) (rawjson.Message, error) { return nil, errors.New("capture unavailable") }
	archive, report, err := runner.Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{broken, exactScenario("good")}})
	require.NoError(t, err)
	assert.Equal(t, "capture unavailable", archive.Observations[0].Error)
	assert.Equal(t, "capture unavailable", report.Scenarios[0].Error)
	assert.True(t, report.Scenarios[1].Passed)
	_, report, err = runner.Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{testScenario("semantic", Subject{Content: "Done."})}})
	require.NoError(t, err)
	assert.Contains(t, report.Scenarios[0].Error, "engine is required")
}

func (r *testReasoner) Config() EvaluatorConfig {
	return EvaluatorConfig{ID: "scripted-reasoner", Model: "reasoner-1", Instructions: "Synthetic fixture decisions."}
}

func (r *testReasoner) Reason(ctx context.Context, subject string, claims []Claim, reference string) (Reasoning, error) {
	request := reasonRequest{subject: subject, claims: claims, reference: reference}
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	if r.reason != nil {
		return r.reason(ctx, request)
	}
	return reasonedLabels(claims, Entailed), nil
}

func (r *testReasoner) Adjudicate(ctx context.Context, subject string, claims []Claim, reference string, conflicts []Disagreement) (Reasoning, error) {
	request := reasonRequest{subject: subject, claims: claims, reference: reference, conflicts: conflicts}
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	if r.adjudicate != nil {
		return r.adjudicate(ctx, request)
	}
	return reasonedLabels(claims, Entailed), nil
}

func (c *testClassifier) Config() EvaluatorConfig {
	return EvaluatorConfig{ID: "scripted-classifier", Model: "classifier-1.0.0", Instructions: "Synthetic four-way classification.", Settings: c.settings}
}

func (c *testClassifier) Classify(ctx context.Context, subject string, claims []Claim, reference string) (Classification, error) {
	request := reasonRequest{subject: subject, claims: claims, reference: reference}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	if c.classify != nil {
		return c.classify(ctx, request)
	}
	return predictedLabels(claims, .97), nil
}

func (r *recordingReporter) ScenarioStarted(id string, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, id)
}

func (r *recordingReporter) ScenarioFinished(report ScenarioReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, report)
}

func testScenario(id string, subject Subject) Scenario {
	const schema = `{"type":"object","properties":{"content":{"type":"string"},"reference":{"type":"string"}},"required":["content"]}`
	return Scenario{
		ID: id, Description: "Synthetic observation.", Timeout: time.Second,
		Schema:       schema,
		CheckNames:   []string{"exact"},
		Requirements: []Requirement{{ID: id + "/complete", SchemaID: bytesDigest([]byte(schema)), Statement: "The work is complete.", Subject: "content", Evidence: []string{"reference"}}},
		Capture:      func(context.Context) (rawjson.Message, error) { return json.Marshal(subject) },
		Bind: func(data rawjson.Message) (Binding, error) {
			var saved Subject
			if err := json.Unmarshal(data, &saved); err != nil {
				return Binding{}, err
			}
			return Binding{Checks: []Check{{Name: "exact", Passed: true}}, Subjects: map[string][]Subject{id + "/complete": {saved}}}, nil
		},
	}
}

func exactScenario(id string) Scenario {
	scenario := testScenario(id, Subject{})
	scenario.Requirements = nil
	scenario.Bind = func(rawjson.Message) (Binding, error) {
		return Binding{Checks: []Check{{Name: "exact", Passed: true}}}, nil
	}
	return scenario
}

func mustRunner(t *testing.T, engine *Engine, concurrency int) *Runner {
	t.Helper()
	runner, err := NewRunner(engine, RunnerConfig{MaxConcurrency: concurrency})
	require.NoError(t, err)
	return runner
}

func reportIDs(report Report) []string {
	ids := make([]string, len(report.Scenarios))
	for i, scenario := range report.Scenarios {
		ids[i] = scenario.ID
	}
	return ids
}

func reasonedLabels(claims []Claim, label Label) Reasoning {
	result := Reasoning{}
	for _, claim := range claims {
		result.Judgments = append(result.Judgments, Judgment{ClaimID: claim.ID, Label: label, Rationale: "Synthetic fixture assessment."})
	}
	return result
}

func predictedLabels(claims []Claim, pass float64) Classification {
	result := Classification{}
	label := Entailed
	if pass < .5 {
		label = Contradicted
	}
	for _, claim := range claims {
		result.Predictions = append(result.Predictions, Prediction{ClaimID: claim.ID, Label: label,
			Probabilities: map[Label]float64{Entailed: pass, Contradicted: 1 - pass, NotAddressed: 0, Indeterminate: 0}})
	}
	return result
}
