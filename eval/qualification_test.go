package eval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQualificationUsesSeparateDecisionCandidatesAndHeldOutGroups(t *testing.T) {
	requirement := testScenario("answer", Subject{}).Requirements[0]
	qualification := qualifiedFixture(t, requirement)
	require.NotNil(t, qualification.Pass)
	require.NotNil(t, qualification.Fail)
	assert.InDelta(t, .97, qualification.Pass.Threshold, 1e-12)
	assert.InDelta(t, .03, qualification.Fail.Threshold, 1e-12)
	assert.True(t, qualification.Pass.Qualified)
	assert.True(t, qualification.Fail.Qualified)
	assert.Equal(t, 40, qualification.Pass.Validation.Groups)
	assert.InDelta(t, .5, qualification.Pass.Validation.Coverage, 1e-12)
	assert.InDelta(t, 1-math.Pow(.025, 1.0/40), qualification.Pass.Validation.UpperError, 1e-14)
	assert.Equal(t, 80, qualification.Calibration.Examples)
	assert.InDelta(t, .0018, qualification.Calibration.BrierScore, 1e-12)
	assert.InDelta(t, .5, qualification.Calibration.MeanEntailedProbability, 1e-12)
	assert.InDelta(t, .5, qualification.Calibration.EntailedRate, 1e-12)
	require.NoError(t, qualification.Validate())

	for _, probability := range []float64{.969, .97, .971} {
		prediction := predictedLabels([]Claim{{ID: "case", Text: "Required."}}, probability).Predictions[0]
		assert.Equal(t, probability >= .97, qualifiedPass(&qualification, prediction))
	}
	for _, probability := range []float64{.029, .03, .031} {
		prediction := predictedLabels([]Claim{{ID: "case", Text: "Required."}}, probability).Predictions[0]
		assert.Equal(t, probability <= .03, qualifiedFail(&qualification, prediction))
	}
}

func TestHeldOutFailureCannotRetuneTheThreshold(t *testing.T) {
	requirement := testScenario("answer", Subject{}).Requirements[0]
	examples, classifier := reviewedFixture()
	for i := range examples {
		if examples[i].Partition == Validation && examples[i].Expected == Entailed {
			examples[i].Expected = Contradicted
		}
	}
	qualification, err := Qualify(t.Context(), classifier, requirement, examples, qualityTarget())
	require.NoError(t, err)
	require.NotNil(t, qualification.Pass)
	assert.InDelta(t, .97, qualification.Pass.Threshold, 1e-12)
	assert.False(t, qualification.Pass.Qualified)
	assert.Equal(t, 40, qualification.Pass.Validation.IncorrectGroups)
	assert.InDelta(t, 1.0, qualification.Pass.Validation.UpperError, 1e-12)
	assert.NoError(t, qualification.Validate())
}

func TestDependentVariantsDoNotCreateIndependentEvidence(t *testing.T) {
	requirement := testScenario("answer", Subject{}).Requirements[0]
	examples, classifier := reviewedFixture()
	for i := range examples {
		examples[i].Group = string(examples[i].Partition) + "/" + string(examples[i].Expected)
	}
	qualification, err := Qualify(t.Context(), classifier, requirement, examples, qualityTarget())
	require.NoError(t, err)
	assert.Nil(t, qualification.Pass)
	assert.Nil(t, qualification.Fail)
	require.NoError(t, qualification.Validate())

	samples := []QualificationSample{
		{Example: LabeledExample{Group: "one", Partition: Validation, Expected: Entailed}, Prediction: predictedLabels([]Claim{{ID: "a", Text: "Required."}}, .97).Predictions[0]},
		{Example: LabeledExample{Group: "one", Partition: Validation, Expected: NotAddressed}, Prediction: predictedLabels([]Claim{{ID: "b", Text: "Required."}}, .97).Predictions[0]},
	}
	band := bandStatistics(samples, Validation, .97, true, .95)
	assert.Equal(t, 1, band.Groups)
	assert.Equal(t, 1, band.IncorrectGroups)
	assert.Equal(t, 2, band.Examples)
	assert.Equal(t, 1, band.IncorrectExamples)
}

func TestQualificationRejectsLeakingOrUnreviewedCorpus(t *testing.T) {
	requirement := testScenario("answer", Subject{}).Requirements[0]
	for _, test := range []struct {
		name   string
		change func([]LabeledExample)
		want   string
	}{
		{"missing review", func(examples []LabeledExample) { examples[0].ReviewedBy = "" }, "invalid reviewed"},
		{"crossing split", func(examples []LabeledExample) { examples[80].Group = examples[0].Group }, "crosses tuning"},
		{"duplicate evidence", func(examples []LabeledExample) { examples[80].Subject = examples[0].Subject }, "identical subject"},
		{"duplicate identity", func(examples []LabeledExample) { examples[1].ID = examples[0].ID }, "invalid reviewed"},
		{"unknown label", func(examples []LabeledExample) { examples[0].Expected = "unsure" }, "invalid reviewed"},
		{"unknown split", func(examples []LabeledExample) { examples[0].Partition = "test" }, "no tuning/validation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			examples, classifier := reviewedFixture()
			test.change(examples)
			_, err := Qualify(t.Context(), classifier, requirement, examples, qualityTarget())
			require.ErrorContains(t, err, test.want)
			assert.Empty(t, classifier.requests)
		})
	}
}

func TestQualificationRejectsTamperedEvidenceAndNonfiniteStatistics(t *testing.T) {
	original := qualifiedFixture(t, testScenario("answer", Subject{}).Requirements[0])
	for _, test := range []struct {
		name   string
		change func(*Qualification)
		want   string
	}{
		{"threshold", func(q *Qualification) { q.Pass.Threshold = .5 }, "statistics"},
		{"qualification flag", func(q *Qualification) { q.Pass.Qualified = false }, "statistics"},
		{"nonfinite threshold", func(q *Qualification) { q.Pass.Threshold = math.NaN() }, "statistics"},
		{"nonfinite statistic", func(q *Qualification) { q.Calibration.BrierScore = math.Inf(1) }, "statistics"},
		{"nonfinite prediction", func(q *Qualification) { q.Samples[0].Prediction.Probabilities[Entailed] = math.NaN() }, "invalid probability"},
		{"changed evidence", func(q *Qualification) { q.Samples[0].Example.Subject.Reference = "Changed facts." }, "content hash"},
		{"negative duration", func(q *Qualification) { q.Duration = -1 }, "duration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			qualification := cloneRecord(original)
			test.change(&qualification)
			assert.ErrorContains(t, qualification.Validate(), test.want)
		})
	}
}

func TestQualificationPreservesPartialEvidenceOnFailure(t *testing.T) {
	examples, classifier := reviewedFixture()
	classifier.classify = func(_ context.Context, request reasonRequest) (Classification, error) {
		if request.claims[0].ID == examples[1].ID {
			return Classification{Calls: []ModelCall{{Stage: "classification", Error: "synthetic failure"}}}, fmt.Errorf("synthetic failure")
		}
		return predictedLabels(request.claims, .97), nil
	}
	result, err := Qualify(t.Context(), classifier, testScenario("answer", Subject{}).Requirements[0], examples, qualityTarget())
	require.ErrorContains(t, err, "synthetic failure")
	assert.Len(t, result.Samples, 1)
	assert.Len(t, result.Calls, 1)
	assert.Empty(t, result.ID)
	assert.Nil(t, result.Pass)
}

func TestQualificationRejectsConfigurationChangesDuringCollection(t *testing.T) {
	examples, classifier := reviewedFixture()
	classifier.settings = map[string]string{"choice_order": "original"}
	classify := classifier.classify
	classifier.classify = func(ctx context.Context, request reasonRequest) (Classification, error) {
		classifier.settings["choice_order"] = "changed"
		return classify(ctx, request)
	}
	result, err := Qualify(t.Context(), classifier, testScenario("answer", Subject{}).Requirements[0], examples, qualityTarget())
	require.ErrorContains(t, err, "configuration changed")
	assert.Equal(t, "original", result.Evaluator.Settings["choice_order"])
	assert.Len(t, result.Samples, len(examples))
	assert.Positive(t, result.Duration)
	assert.Empty(t, result.ID)
	assert.Nil(t, result.Pass)
}

func TestBinomialUpper(t *testing.T) {
	for _, test := range []struct {
		name             string
		incorrect, total int
		alpha, expected  float64
	}{
		{"no evidence", 0, 0, .05, 1},
		{"all wrong", 5, 5, .05, 1},
		{"no errors", 0, 100, .05, 1 - math.Pow(.05, .01)},
		{"one of two", 1, 2, .05, math.Sqrt(.95)},
		{"ninety nine of one hundred", 99, 100, .05, math.Pow(.95, .01)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.InDelta(t, test.expected, binomialUpper(test.incorrect, test.total, test.alpha), 1e-12)
		})
	}
}

func TestQualificationNeedsBothTuningAndValidation(t *testing.T) {
	for _, partition := range []Partition{Tuning, Validation} {
		t.Run(string(partition), func(t *testing.T) {
			examples, classifier := reviewedFixture()
			for i := range examples {
				examples[i].Partition = partition
			}
			result, err := Qualify(t.Context(), classifier, testScenario("answer", Subject{}).Requirements[0], examples, qualityTarget())
			require.NoError(t, err)
			if partition == Validation {
				assert.Nil(t, result.Pass)
			} else {
				require.NotNil(t, result.Pass)
				assert.False(t, result.Pass.Qualified)
				assert.Zero(t, result.Pass.Validation.Groups)
				assert.InDelta(t, 1.0, result.Pass.Validation.UpperError, 1e-12)
			}
		})
	}
}

func qualifiedFixture(t *testing.T, requirement Requirement) Qualification {
	t.Helper()
	examples, classifier := reviewedFixture()
	qualification, err := Qualify(t.Context(), classifier, requirement, examples, qualityTarget())
	require.NoError(t, err)
	return qualification
}

// reviewedFixture supplies synthetic, independent evidence to test statistics,
// not a deployable qualification or evidence of any real model's performance.
func reviewedFixture() ([]LabeledExample, *testClassifier) {
	examples := make([]LabeledExample, 0, 160)
	for _, partition := range []Partition{Tuning, Validation} {
		for _, expected := range []Label{Entailed, Contradicted} {
			for i := range 40 {
				id := fmt.Sprintf("%s/%s/%d", partition, expected, i)
				examples = append(examples, LabeledExample{
					ID: id, Group: id, Partition: partition, Expected: expected,
					Subject: Subject{Content: string(expected) + " reviewed synthetic example " + id}, ReviewedBy: "synthetic-test-author",
				})
			}
		}
	}
	return examples, &testClassifier{classify: func(_ context.Context, request reasonRequest) (Classification, error) {
		probability := .03
		if strings.HasPrefix(request.subject, "entailed ") {
			probability = .97
		}
		return predictedLabels(request.claims, probability), nil
	}}
}

func qualityTarget() QualificationConfig {
	return QualificationConfig{MaxErrorRate: .1, Confidence: .95, Timeout: time.Second}
}
