// Package eval qualifies native classifier decisions against reviewed examples.
// Tuning chooses thresholds; disjoint held-out groups test them once. Variants
// from one original count as one independent group, and any wrong accepted
// variant makes that group's decision incorrect. Qualifications authorize only
// the exact requirement and model configuration whose evidence they retain.
package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type (
	// Partition identifies a corpus's threshold-tuning or held-out portion.
	Partition string

	// LabeledExample is an application-owned, reviewed semantic example.
	LabeledExample struct {
		// ID identifies the example in the application's corpus.
		ID string `json:"id"`
		// Group identifies the original shared by dependent variants.
		Group string `json:"group"`
		// Partition fixes the split before classifier predictions are collected.
		Partition Partition `json:"partition"`
		// Subject contains the captured content and its factual context.
		Subject Subject `json:"subject"`
		// Expected is the reviewed gold label, not another model's inferred label.
		Expected Label `json:"expected"`
		// ReviewedBy identifies the application's review provenance.
		ReviewedBy string `json:"reviewed_by"`
	}

	// QualificationConfig supplies explicit statistical targets and a per-example
	// call deadline. Qualify issues at most one classifier request at a time.
	QualificationConfig struct {
		// MaxErrorRate is the inclusive tolerated fraction of incorrect independent
		// groups in each accepted band, between zero and one, excluding one.
		MaxErrorRate float64 `json:"max_error_rate"`
		// Confidence is the desired simultaneous confidence for both decision bands,
		// strictly between zero and one. Each band receives half the error budget.
		Confidence float64 `json:"confidence"`
		// Timeout bounds one complete example classification, in nanoseconds.
		Timeout time.Duration `json:"timeout"`
	}

	// QualificationSample retains reviewed evidence and its original prediction.
	QualificationSample struct {
		// Example contains the reviewed subject, label, group, and partition.
		Example LabeledExample `json:"example"`
		// Prediction retains the unmodified four-label distribution.
		Prediction Prediction `json:"prediction"`
	}

	// BandStatistics reports coverage and a conservative group error bound.
	BandStatistics struct {
		// Groups counts independent groups with at least one accepted example.
		Groups int `json:"groups"`
		// IncorrectGroups counts accepted groups with any incorrect decision.
		IncorrectGroups int `json:"incorrect_groups"`
		// Examples counts individual accepted variants.
		Examples int `json:"examples"`
		// IncorrectExamples counts individual incorrect accepted variants.
		IncorrectExamples int `json:"incorrect_examples"`
		// Coverage is the fraction of partition examples accepted by this band.
		Coverage float64 `json:"coverage"`
		// UpperError is the one-sided exact binomial upper bound across groups.
		UpperError float64 `json:"upper_error"`
	}

	// DecisionBand is a tuned threshold and its independent validation result.
	DecisionBand struct {
		// Threshold is inclusive: P(entailed) >= it for passes, <= it for failures.
		Threshold float64 `json:"threshold"`
		// Tuning describes the data that selected the threshold.
		Tuning BandStatistics `json:"tuning"`
		// Validation describes held-out data, which never changes the threshold.
		Validation BandStatistics `json:"validation"`
		// Qualified requires nonempty held-out groups and an acceptable upper bound.
		Qualified bool `json:"qualified"`
	}

	// CalibrationStatistics measures held-out probability quality without turning
	// a probability estimate into a per-example guarantee.
	CalibrationStatistics struct {
		// Examples counts the held-out examples contributing to these statistics.
		Examples int `json:"examples"`
		// BrierScore is the mean sum of squared errors over all four probabilities.
		BrierScore float64 `json:"brier_score"`
		// MeanEntailedProbability is the average predicted probability of passing.
		MeanEntailedProbability float64 `json:"mean_entailed_probability"`
		// EntailedRate is the observed fraction of gold labels that pass.
		EntailedRate float64 `json:"entailed_rate"`
	}

	// Qualification retains the reviewed evidence for one exact assessor contract.
	// It is not a claim about another requirement, model version, or population.
	Qualification struct {
		// ID is the content identity verified before this record authorizes decisions.
		ID string `json:"id"`
		// Requirement fixes the statement and captured-field selectors.
		Requirement Requirement `json:"requirement"`
		// Evaluator fixes the classifier version, instructions, and settings.
		Evaluator EvaluatorConfig `json:"evaluator"`
		// Config records the application's quality target and call deadline.
		Config QualificationConfig `json:"config"`
		// Samples retains tuning and validation evidence for independent review.
		Samples []QualificationSample `json:"samples"`
		// Pass is the tuned automatic-pass band, or nil when tuning was insufficient.
		Pass *DecisionBand `json:"pass,omitempty"`
		// Fail identifies confident failures for disagreement detection only.
		Fail *DecisionBand `json:"fail,omitempty"`
		// Calibration reports held-out probability quality.
		Calibration CalibrationStatistics `json:"calibration"`
		// Calls retains all classifier usage needed to establish this qualification.
		Calls []ModelCall `json:"calls"`
		// Duration measures the full qualification operation.
		Duration time.Duration `json:"duration"`
	}
)

const (
	// Tuning selects examples used only to choose thresholds.
	Tuning Partition = "tuning"
	// Validation selects examples reserved for testing the chosen thresholds.
	Validation Partition = "validation"
)

// Qualify classifies a reviewed corpus, chooses thresholds using tuning groups,
// and validates them against held-out groups. Insufficient statistical evidence
// returns an unqualified record, not an error and not a default confidence cutoff.
// Classifier failures return the partial evidence and the original error.
func Qualify(ctx context.Context, classifier Classifier, requirement Requirement, examples []LabeledExample, config QualificationConfig) (result Qualification, err error) {
	if classifier == nil {
		return Qualification{}, errors.New("qualification requires a native classifier")
	}
	if err := validateCorpus(requirement, examples, config); err != nil {
		return Qualification{}, err
	}
	configuration := classifier.Config()
	if err := validateEvaluator(configuration); err != nil {
		return Qualification{}, err
	}
	configuration = cloneRecord(configuration)
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.qualify")
	defer span.End()
	defer func() {
		if err != nil {
			recordError(span, err)
		}
		span.SetAttributes(attribute.Int("eval.qualification.samples", len(result.Samples)))
	}()
	started := time.Now()
	result = Qualification{Requirement: requirement, Evaluator: configuration, Config: config}
	for _, example := range examples {
		if err := ctx.Err(); err != nil {
			result.Duration = time.Since(started)
			return result, err
		}
		callCtx, cancel := context.WithTimeout(ctx, config.Timeout)
		claims := []Claim{{ID: example.ID, Text: requirement.Statement}}
		classified, err := classifier.Classify(callCtx, example.Subject.Content, claims, example.Subject.Reference)
		if err == nil {
			err = callCtx.Err()
		}
		cancel()
		result.Calls = append(result.Calls, classified.Calls...)
		if err == nil {
			err = ValidateClassification(claims, classified)
		}
		if err != nil {
			result.Duration = time.Since(started)
			return result, fmt.Errorf("qualify example %q: %w", example.ID, err)
		}
		result.Samples = append(result.Samples, QualificationSample{Example: example, Prediction: classified.Predictions[0]})
	}
	if digest(classifier.Config()) != digest(configuration) {
		result.Duration = time.Since(started)
		return result, errors.New("classifier configuration changed during qualification")
	}
	result.Pass, result.Fail, result.Calibration = qualificationStatistics(result.Samples, config)
	result.Duration = time.Since(started)
	result.ID = digest(result)
	return result, nil
}

// Validate verifies the saved evidence, grouping rules, derived statistics, and
// identity. A hand-edited threshold cannot authorize an automatic assessment.
func (q Qualification) Validate() error {
	examples := make([]LabeledExample, len(q.Samples))
	for i, sample := range q.Samples {
		examples[i] = sample.Example
		if err := ValidateClassification([]Claim{{ID: sample.Example.ID, Text: q.Requirement.Statement}}, Classification{Predictions: []Prediction{sample.Prediction}}); err != nil {
			return err
		}
	}
	if err := validateCorpus(q.Requirement, examples, q.Config); err != nil {
		return err
	}
	if err := validateEvaluator(q.Evaluator); err != nil {
		return err
	}
	pass, fail, calibration := qualificationStatistics(q.Samples, q.Config)
	if !reflect.DeepEqual(q.Pass, pass) || !reflect.DeepEqual(q.Fail, fail) || q.Calibration != calibration {
		return errors.New("qualification statistics do not match retained evidence")
	}
	if q.Duration < 0 {
		return errors.New("qualification duration cannot be negative")
	}
	if err := validateCalls(q.Calls); err != nil {
		return err
	}
	id := q.ID
	q.ID = ""
	if id != digest(q) {
		return errors.New("qualification content hash does not match")
	}
	return nil
}

// qualificationStatistics chooses bands from tuning data and evaluates each
// fixed candidate once on held-out data. Validation never influences tuning.
func qualificationStatistics(samples []QualificationSample, config QualificationConfig) (*DecisionBand, *DecisionBand, CalibrationStatistics) {
	pass := tuneBand(samples, config, true)
	fail := tuneBand(samples, config, false)
	return pass, fail, calibrationStatistics(samples)
}

func tuneBand(samples []QualificationSample, config QualificationConfig, pass bool) *DecisionBand {
	thresholds := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.Example.Partition == Tuning && (sample.Prediction.Label == Entailed) == pass {
			thresholds = append(thresholds, sample.Prediction.Probabilities[Entailed])
		}
	}
	sort.Float64s(thresholds)
	thresholds = slices.Compact(thresholds)
	if !pass {
		slices.Reverse(thresholds)
	}
	for _, threshold := range thresholds {
		tuning := bandStatistics(samples, Tuning, threshold, pass, config.Confidence)
		if tuning.Groups == 0 || tuning.UpperError > config.MaxErrorRate {
			continue
		}
		validation := bandStatistics(samples, Validation, threshold, pass, config.Confidence)
		return &DecisionBand{
			Threshold: threshold, Tuning: tuning, Validation: validation,
			Qualified: validation.Groups > 0 && validation.UpperError <= config.MaxErrorRate,
		}
	}
	return nil
}

// bandStatistics treats correlated variants conservatively: accepting one wrong
// variant marks its independent group wrong even when other variants are correct.
func bandStatistics(samples []QualificationSample, partition Partition, threshold float64, pass bool, confidence float64) BandStatistics {
	groups := make(map[string]bool)
	statistics := BandStatistics{}
	total := 0
	for _, sample := range samples {
		if sample.Example.Partition != partition {
			continue
		}
		total++
		if !inBand(sample.Prediction, threshold, pass) {
			continue
		}
		incorrect := (sample.Example.Expected == Entailed) != pass
		statistics.Examples++
		if incorrect {
			statistics.IncorrectExamples++
		}
		groups[sample.Example.Group] = groups[sample.Example.Group] || incorrect
	}
	statistics.Groups = len(groups)
	for _, incorrect := range groups {
		if incorrect {
			statistics.IncorrectGroups++
		}
	}
	if total > 0 {
		statistics.Coverage = float64(statistics.Examples) / float64(total)
	}
	// Splitting the allowed statistical error equally between the two bands
	// makes the requested confidence apply to both together for this requirement.
	statistics.UpperError = binomialUpper(statistics.IncorrectGroups, statistics.Groups, (1-confidence)/2)
	return statistics
}

func inBand(prediction Prediction, threshold float64, pass bool) bool {
	probability := prediction.Probabilities[Entailed]
	if pass {
		return prediction.Label == Entailed && probability >= threshold
	}
	return prediction.Label != Entailed && probability <= threshold
}

func calibrationStatistics(samples []QualificationSample) CalibrationStatistics {
	statistics := CalibrationStatistics{}
	for _, sample := range samples {
		if sample.Example.Partition != Validation {
			continue
		}
		statistics.Examples++
		statistics.MeanEntailedProbability += sample.Prediction.Probabilities[Entailed]
		if sample.Example.Expected == Entailed {
			statistics.EntailedRate++
		}
		for _, label := range []Label{Entailed, Contradicted, NotAddressed, Indeterminate} {
			probability := sample.Prediction.Probabilities[label]
			target := 0.0
			if label == sample.Example.Expected {
				target = 1
			}
			statistics.BrierScore += (probability - target) * (probability - target)
		}
	}
	if statistics.Examples > 0 {
		n := float64(statistics.Examples)
		statistics.BrierScore /= n
		statistics.MeanEntailedProbability /= n
		statistics.EntailedRate /= n
	}
	return statistics
}

// binomialUpper inverts the binomial cumulative probability to obtain the
// one-sided Clopper-Pearson bound. Empty evidence permits an error rate of one.
// Bisection stops when floating-point endpoints can no longer move.
func binomialUpper(incorrect, total int, alpha float64) float64 {
	if total == 0 || incorrect == total {
		return 1
	}
	if incorrect == 0 {
		return -math.Expm1(math.Log(alpha) / float64(total))
	}
	low, high := float64(incorrect)/float64(total), 1.0
	for {
		middle := (low + high) / 2
		if middle == low || middle == high {
			return high
		}
		if binomialCDF(incorrect, total, middle) > alpha {
			low = middle
		} else {
			high = middle
		}
	}
}

// binomialCDF adds log-scaled terms to avoid underflow for large corpora.
func binomialCDF(incorrect, total int, probability float64) float64 {
	logTerm := float64(total) * math.Log1p(-probability)
	logSum := logTerm
	for k := 1; k <= incorrect; k++ {
		logTerm += math.Log(float64(total-k+1)/float64(k)) + math.Log(probability) - math.Log1p(-probability)
		largest, smallest := max(logSum, logTerm), min(logSum, logTerm)
		logSum = largest + math.Log1p(math.Exp(smallest-largest))
	}
	return math.Exp(logSum)
}

func validateCorpus(requirement Requirement, examples []LabeledExample, config QualificationConfig) error {
	if err := validateRequirement(requirement); err != nil {
		return err
	}
	if !unitInterval(config.MaxErrorRate) || config.MaxErrorRate == 1 || !unitInterval(config.Confidence) || config.Confidence == 0 || config.Confidence == 1 || config.Timeout <= 0 {
		return errors.New("qualification requires an error rate in [0,1), confidence in (0,1), and a positive per-example timeout")
	}
	return validateExamples(examples)
}

func validateRequirement(requirement Requirement) error {
	if requirement.ID == "" || requirement.SchemaID == "" || requirement.Statement == "" || requirement.Subject == "" {
		return errors.New("assessment requires a complete declared requirement and observation schema identity")
	}
	return nil
}

// validateExamples checks review provenance and keeps dependent variants in one
// partition. Neither a model nor a corpus consumer may invent a reviewed label.
func validateExamples(examples []LabeledExample) error {
	if len(examples) == 0 {
		return errors.New("assessment requires reviewed examples")
	}
	ids := make(map[string]bool, len(examples))
	groups := make(map[string]Partition)
	contents := make(map[string]string)
	for _, example := range examples {
		if example.ID == "" || ids[example.ID] || example.Group == "" || example.ReviewedBy == "" || !validLabel(example.Expected) {
			return fmt.Errorf("invalid reviewed example %q", example.ID)
		}
		if example.Partition != Tuning && example.Partition != Validation {
			return fmt.Errorf("example %q has no tuning/validation partition", example.ID)
		}
		ids[example.ID] = true
		if partition, exists := groups[example.Group]; exists && partition != example.Partition {
			return fmt.Errorf("dependent group %q crosses tuning and validation", example.Group)
		}
		groups[example.Group] = example.Partition
		identity := digest(example.Subject)
		if group, exists := contents[identity]; exists && group != example.Group {
			return fmt.Errorf("identical subject evidence appears in independent groups %q and %q", group, example.Group)
		}
		contents[identity] = example.Group
	}
	return nil
}

func validateEvaluator(config EvaluatorConfig) error {
	if config.ID == "" || config.Model == "" || config.Instructions == "" {
		return errors.New("evaluator requires its implementation, model, and exact instructions")
	}
	return nil
}
