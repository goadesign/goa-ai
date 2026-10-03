// Package eval compares assessment policies against the same captured bytes.
// Product hooks never run here. Every repetition retains its complete report;
// totals include classification, reasoning, audits, and format corrections.
package eval

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// ComparisonConfig selects repetitions and optional application-owned pricing.
	ComparisonConfig struct {
		// Repetitions is the positive number of assessments per configured runner.
		// Each assessment keeps the runner's per-scenario deadlines and concurrency.
		Repetitions int
		// Currency names the common unit returned by Price, for example "USD".
		// It must be empty when Price is nil.
		Currency string
		// Price returns the complete cost of one provider invocation from its
		// validated usage. The caller owns model tariffs and provider-specific
		// cache accounting. Nil disables cost estimates; errors stop comparison.
		Price func(model.TokenUsage) (float64, error)
	}

	// StrategyComparison retains repeated assessments and their total work.
	// Counts and costs cover all repetitions; duration bounds describe one run.
	StrategyComparison struct {
		// Reports retains each independent assessment in repetition order.
		Reports []Report `json:"reports"`
		// ModelCalls counts actual provider invocations, including failed ones.
		ModelCalls int `json:"model_calls"`
		// CallsByStage separates classification, reasoning, and adjudication work.
		CallsByStage map[string]int `json:"calls_by_stage"`
		// UsageByModel sums reported counts under the actual provider model.
		// An empty key means the provider omitted its model identity.
		UsageByModel map[string]model.TokenUsage `json:"usage_by_model"`
		// MissingUsageCalls counts invocations without reported usage.
		MissingUsageCalls int `json:"missing_usage_calls"`
		// AutomaticPasses counts decisions accepted from a qualified native band.
		AutomaticPasses int `json:"automatic_passes"`
		// Audits counts qualified passes selected for independent reasoning.
		Audits int `json:"audits"`
		// Disagreements counts qualified conflicts sent for adjudication.
		Disagreements int `json:"disagreements"`
		// Unresolved counts explicit terminal abstentions.
		Unresolved int `json:"unresolved"`
		// ScenarioErrors counts capture or assessment failures, not failed assertions.
		ScenarioErrors int `json:"scenario_errors"`
		// VariableOutcomes counts assertion or scenario-error outcomes that differ
		// across repetitions, including a missing outcome after an error.
		VariableOutcomes int `json:"variable_outcomes"`
		// AssessmentDuration sums assessment wall time, excluding product capture.
		AssessmentDuration time.Duration `json:"assessment_duration"`
		// MinDuration is the shortest complete assessment duration.
		MinDuration time.Duration `json:"min_duration"`
		// MaxDuration is the longest complete assessment duration.
		MaxDuration time.Duration `json:"max_duration"`
		// Cost is nil if pricing was disabled or any invocation lacked usage.
		// Zero means known zero cost, such as an assessment with no model calls.
		Cost *float64 `json:"cost,omitempty"`
	}

	// Comparison measures policy work on one immutable capture archive. It does
	// not treat one policy's decisions as gold; use Challenge for reviewed accuracy.
	Comparison struct {
		// ArchiveID identifies the exact observations used by every assessment.
		ArchiveID string `json:"archive_id"`
		// CaptureDuration is the original product wall time, counted only once.
		CaptureDuration time.Duration `json:"capture_duration"`
		// Currency is the configured common cost unit, or empty without pricing.
		Currency string `json:"currency,omitempty"`
		// Strategies retains work and reports under caller-supplied policy names.
		Strategies map[string]StrategyComparison `json:"strategies"`
	}
)

// Compare repeatedly assesses one archive with each named runner. Runners and
// repetitions execute serially in name order; each runner owns scenario
// concurrency. The operation never invokes Capture or changes qualification.
// Protocol failures remain in reports; cancellation or pricing errors return
// the reports completed so far and the error.
func Compare(ctx context.Context, suite Suite, archive Archive, runners map[string]*Runner, config ComparisonConfig) (Comparison, error) {
	if len(runners) == 0 || config.Repetitions <= 0 {
		return Comparison{}, errors.New("comparison requires named runners and positive repetitions")
	}
	if (config.Price == nil) != (config.Currency == "") {
		return Comparison{}, errors.New("comparison pricing and currency must be supplied together")
	}
	if _, _, err := assessmentSelection(suite, archive); err != nil {
		return Comparison{}, err
	}
	for name, runner := range runners {
		if name == "" || runner == nil {
			return Comparison{}, errors.New("comparison runner names and instances must be nonempty")
		}
	}
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.compare")
	defer span.End()
	comparison := Comparison{
		ArchiveID: archive.ID, CaptureDuration: archive.Duration, Currency: config.Currency,
		Strategies: make(map[string]StrategyComparison, len(runners)),
	}
	for _, name := range slices.Sorted(maps.Keys(runners)) {
		reports := make([]Report, 0, config.Repetitions)
		var assessErr error
		for range config.Repetitions {
			if assessErr = ctx.Err(); assessErr != nil {
				break
			}
			var report Report
			report, assessErr = runners[name].Assess(ctx, suite, archive)
			reports = append(reports, report)
			if assessErr != nil {
				break
			}
		}
		summary, summaryErr := summarizeReports(reports, config)
		comparison.Strategies[name] = summary
		if err := errors.Join(assessErr, summaryErr); err != nil {
			recordError(span, err)
			return comparison, err
		}
	}
	span.SetAttributes(attribute.Int("eval.comparison.strategies", len(runners)), attribute.Int("eval.comparison.repetitions", config.Repetitions))
	return comparison, nil
}

// summarizeReports validates usage before adding it and only returns a complete
// cost when every invocation can be priced. Raw reports remain available when
// accounting fails.
func summarizeReports(reports []Report, config ComparisonConfig) (StrategyComparison, error) {
	summary := StrategyComparison{
		Reports: reports, CallsByStage: make(map[string]int),
		UsageByModel: make(map[string]model.TokenUsage),
	}
	cost, knownCost := 0.0, config.Price != nil
	outcomes := make([]map[string]string, 0, len(reports))
	for i, report := range reports {
		if err := report.Validate(); err != nil {
			return summary, err
		}
		summary.AssessmentDuration += report.Duration
		if i == 0 {
			summary.MinDuration = report.Duration
		} else {
			summary.MinDuration = min(summary.MinDuration, report.Duration)
		}
		summary.MaxDuration = max(summary.MaxDuration, report.Duration)
		outcomes = append(outcomes, reportOutcomes(report))
		for _, scenario := range report.Scenarios {
			if scenario.Error != "" {
				summary.ScenarioErrors++
			}
			for _, requirement := range scenario.Requirements {
				for _, instance := range requirement.Instances {
					switch instance.Decision.(type) {
					case Calibrated:
						summary.AutomaticPasses++
					case Unresolved:
						summary.Unresolved++
					}
					if instance.Audited {
						summary.Audits++
					}
					if instance.Disagreement {
						summary.Disagreements++
					}
				}
			}
			for _, call := range scenario.Calls {
				summary.ModelCalls++
				summary.CallsByStage[call.Stage]++
				if call.Usage == nil {
					summary.MissingUsageCalls++
					knownCost = false
					continue
				}
				usage := *call.Usage
				total, err := model.AddTokenUsage(summary.UsageByModel[usage.Model], usage)
				if err != nil {
					return summary, err
				}
				total.Model = usage.Model
				summary.UsageByModel[usage.Model] = total
				if config.Price == nil {
					knownCost = false
					continue
				}
				amount, err := config.Price(usage)
				if err != nil {
					return summary, fmt.Errorf("price model %q: %w", usage.Model, err)
				}
				if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) || math.IsInf(cost+amount, 0) {
					return summary, errors.New("model pricing must return a finite nonnegative cost")
				}
				cost += amount
			}
		}
	}
	summary.VariableOutcomes = variableOutcomes(outcomes)
	if knownCost {
		summary.Cost = &cost
	}
	return summary, nil
}

func reportOutcomes(report Report) map[string]string {
	outcomes := make(map[string]string)
	for _, scenario := range report.Scenarios {
		prefix := scenario.ID + "\x00"
		outcomes[prefix+"error"] = "none"
		if scenario.Error != "" {
			outcomes[prefix+"error"] = "error"
		}
		for _, check := range scenario.Checks {
			value := "failed"
			if check.Passed {
				value = "passed"
			}
			outcomes[prefix+"check:"+check.Name] = value
		}
		for _, requirement := range scenario.Requirements {
			for _, instance := range requirement.Instances {
				value := "error"
				if label, resolved := instance.Label(); resolved {
					value = string(label)
				} else if instance.Decision != nil {
					value = "unresolved"
				}
				outcomes[prefix+"requirement:"+instance.ID] = value
			}
		}
	}
	return outcomes
}

func variableOutcomes(repetitions []map[string]string) int {
	keys := make(map[string]struct{})
	for _, outcomes := range repetitions {
		for key := range outcomes {
			keys[key] = struct{}{}
		}
	}
	var changed int
	for key := range keys {
		first := repetitions[0][key]
		for _, outcomes := range repetitions[1:] {
			if outcomes[key] != first {
				changed++
				break
			}
		}
	}
	return changed
}
