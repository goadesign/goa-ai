package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectiveEngineRoutesByQualificationAndAudits(t *testing.T) {
	scenario := testScenario("answer", Subject{Content: "The task is complete.", Reference: "Captured task."})
	qualification := qualifiedFixture(t, scenario.Requirements[0])
	for _, test := range []struct {
		name        string
		qualified   bool
		probability float64
		audit       float64
		reasoned    int
		calibrated  bool
		audited     bool
	}{
		{"qualified pass", true, .98, 0, 0, true, false},
		{"threshold is inclusive", true, .97, 0, 0, true, false},
		{"below threshold", true, .969, 0, 1, false, false},
		{"unqualified", false, .999, 0, 1, false, false},
		{"audited", true, .98, 1, 1, false, true},
		{"likely failure", true, .1, 0, 1, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			classifier := &testClassifier{classify: func(_ context.Context, request reasonRequest) (Classification, error) {
				return predictedLabels(request.claims, test.probability), nil
			}}
			reasoner := &testReasoner{}
			policy := SelectivePolicy{AuditFraction: test.audit}
			if test.qualified {
				policy.Qualifications = []Qualification{qualification}
			}
			engine, err := NewSelectiveEngine(classifier, reasoner, policy)
			require.NoError(t, err)
			_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
			require.NoError(t, err)
			require.Empty(t, report.Scenarios[0].Error)
			assert.True(t, report.Passed)
			assert.Len(t, classifier.requests, 1)
			assert.Len(t, reasoner.requests, test.reasoned)
			assessment := report.Scenarios[0].Requirements[0].Instances[0]
			_, calibrated := assessment.Decision.(Calibrated)
			assert.Equal(t, test.calibrated, calibrated)
			assert.Equal(t, test.audited, assessment.Audited)
			for _, request := range reasoner.requests {
				assert.Equal(t, "The task is complete.", request.subject)
				assert.Equal(t, "Captured task.", request.reference)
				assert.Empty(t, request.conflicts)
			}
		})
	}
}

func TestQualifiedConflictGetsOneAdjudicationAndMayRemainUnresolved(t *testing.T) {
	scenario := testScenario("answer", Subject{Content: "Observed outcome.", Reference: "Captured evidence."})
	qualification := qualifiedFixture(t, scenario.Requirements[0])
	for _, test := range []struct {
		name        string
		probability float64
		initial     Label
		abstain     bool
	}{
		{"false positive", .98, NotAddressed, false},
		{"false negative", .02, Entailed, false},
		{"unresolved", .98, Contradicted, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			classifier := &testClassifier{classify: func(_ context.Context, request reasonRequest) (Classification, error) {
				return predictedLabels(request.claims, test.probability), nil
			}}
			reasoner := &testReasoner{
				reason: func(_ context.Context, request reasonRequest) (Reasoning, error) {
					assert.Empty(t, request.conflicts)
					return reasonedLabels(request.claims, test.initial), nil
				},
				adjudicate: func(_ context.Context, request reasonRequest) (Reasoning, error) {
					assert.Equal(t, "Observed outcome.", request.subject)
					assert.Equal(t, "Captured evidence.", request.reference)
					require.Len(t, request.conflicts, 1)
					assert.Equal(t, qualification.ID, request.conflicts[0].QualificationID)
					assert.Equal(t, test.initial, request.conflicts[0].Judgment.Label)
					if test.abstain {
						return Reasoning{Abstentions: []Abstention{{ClaimID: request.claims[0].ID, Reason: "The supplied evidence is insufficient."}}}, nil
					}
					return reasonedLabels(request.claims, Contradicted), nil
				},
			}
			engine, err := NewDisagreementEngine(classifier, reasoner, []Qualification{qualification})
			require.NoError(t, err)
			_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
			require.NoError(t, err)
			assert.Empty(t, report.Scenarios[0].Error)
			assert.False(t, report.Passed)
			assert.Len(t, reasoner.requests, 2)
			assessment := report.Scenarios[0].Requirements[0].Instances[0]
			assert.True(t, assessment.Disagreement)
			require.NotNil(t, assessment.Initial)
			assert.Equal(t, test.initial, assessment.Initial.Label)
			if test.abstain {
				assert.IsType(t, Unresolved{}, assessment.Decision)
				_, resolved := assessment.Label()
				assert.False(t, resolved)
			} else {
				label, resolved := assessment.Label()
				assert.True(t, resolved)
				assert.Equal(t, Contradicted, label)
			}
		})
	}
}

func TestUnqualifiedDisagreementAndAbstentionDoNotEscalate(t *testing.T) {
	scenario := testScenario("answer", Subject{Content: "Captured answer."})
	for _, abstain := range []bool{false, true} {
		reasoner := &testReasoner{reason: func(_ context.Context, request reasonRequest) (Reasoning, error) {
			if abstain {
				return Reasoning{Abstentions: []Abstention{{ClaimID: request.claims[0].ID, Reason: "Cannot assess."}}}, nil
			}
			return reasonedLabels(request.claims, NotAddressed), nil
		}}
		engine, err := NewDisagreementEngine(&testClassifier{}, reasoner, nil)
		require.NoError(t, err)
		_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
		require.NoError(t, err)
		assert.False(t, report.Passed)
		assert.Len(t, reasoner.requests, 1)
		assert.False(t, report.Scenarios[0].Requirements[0].Instances[0].Disagreement)
	}
}

func TestClassifierFailureNeverBecomesAReasonedFallback(t *testing.T) {
	scenario := testScenario("answer", Subject{Content: "Captured answer."})
	for _, test := range []struct {
		name     string
		classify func(context.Context, reasonRequest) (Classification, error)
		want     string
	}{
		{"provider error", func(context.Context, reasonRequest) (Classification, error) {
			return Classification{Calls: []ModelCall{{Stage: "classification", Error: "unavailable"}}}, errors.New("unavailable")
		}, "unavailable"},
		{"missing decisions", func(context.Context, reasonRequest) (Classification, error) {
			return Classification{}, nil
		}, "0 predictions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reasoner := &testReasoner{}
			engine, err := NewSelectiveEngine(&testClassifier{classify: test.classify}, reasoner, SelectivePolicy{})
			require.NoError(t, err)
			_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{scenario}})
			require.NoError(t, err)
			assert.Contains(t, report.Scenarios[0].Error, test.want)
			assert.False(t, report.Passed)
			assert.Empty(t, reasoner.requests)
			assert.Nil(t, report.Scenarios[0].Requirements[0].Instances[0].Decision)
		})
	}
}

func TestQualificationsCannotOutliveTheirExactContract(t *testing.T) {
	scenario := testScenario("answer", Subject{Content: "Captured answer."})
	qualification := qualifiedFixture(t, scenario.Requirements[0])
	t.Run("changed statement", func(t *testing.T) {
		engine, err := NewSelectiveEngine(&testClassifier{}, &testReasoner{}, SelectivePolicy{Qualifications: []Qualification{qualification}})
		require.NoError(t, err)
		changed := scenario
		changed.Requirements = append([]Requirement(nil), scenario.Requirements...)
		changed.Requirements[0].Statement = "An unrelated requirement."
		_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{changed}})
		require.NoError(t, err)
		assert.Contains(t, report.Scenarios[0].Error, "stale")
	})
	t.Run("changed schema", func(t *testing.T) {
		engine, err := NewSelectiveEngine(&testClassifier{}, &testReasoner{}, SelectivePolicy{Qualifications: []Qualification{qualification}})
		require.NoError(t, err)
		changed := scenario
		changed.Schema += "\n"
		changed.Requirements = append([]Requirement(nil), scenario.Requirements...)
		changed.Requirements[0].SchemaID = bytesDigest([]byte(changed.Schema))
		_, report, err := mustRunner(t, engine, 1).Run(t.Context(), Suite{ID: "suite", Scenarios: []Scenario{changed}})
		require.NoError(t, err)
		assert.Contains(t, report.Scenarios[0].Error, "stale")
	})
	t.Run("changed model", func(t *testing.T) {
		changed := cloneRecord(qualification)
		changed.Evaluator.Model = "classifier-2.0.0"
		changed.ID = ""
		changed.ID = digest(changed)
		_, err := NewSelectiveEngine(&testClassifier{}, &testReasoner{}, SelectivePolicy{Qualifications: []Qualification{changed}})
		assert.ErrorContains(t, err, "different classifier")
	})
}

func TestAuditSelectionIsReproducibleAndMonotone(t *testing.T) {
	var selected int
	for _, observation := range []string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth"} {
		assert.False(t, auditSelected(observation, "requirement", "qualification", 0))
		assert.True(t, auditSelected(observation, "requirement", "qualification", 1))
		atHalf := auditSelected(observation, "requirement", "qualification", .5)
		assert.Equal(t, atHalf, auditSelected(observation, "requirement", "qualification", .5))
		if atHalf {
			selected++
			assert.True(t, auditSelected(observation, "requirement", "qualification", .75))
		}
	}
	assert.Positive(t, selected)
	assert.Less(t, selected, 8)
}
