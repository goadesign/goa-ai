// Package eval tests fixed requirements against reviewed examples and authored
// variants. It reuses normal assessment routing but never calls product hooks,
// changes qualification thresholds, or treats another model's answer as gold.
package eval

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type (
	// ChallengeResult compares one reviewed example with its actual assessment.
	ChallengeResult struct {
		// Example retains the reviewed content, gold label, and variant group.
		Example LabeledExample `json:"example"`
		// Assessment retains probabilities, reasoning, and any adjudication.
		Assessment Assessment `json:"assessment"`
		// Calls retains all provider work for this example.
		Calls []ModelCall `json:"calls,omitempty"`
		// Duration measures the complete example assessment.
		Duration time.Duration `json:"duration"`
		// Error records an infrastructure or protocol failure.
		Error string `json:"error,omitempty"`
		// Matches requires the exact reviewed label, without errors or abstention.
		Matches bool `json:"matches"`
	}

	// ChallengeReport measures decisions against reviewed labels. Counts refer
	// to individual examples, not statistically independent groups.
	ChallengeReport struct {
		// Requirement fixes the statement and evidence-selection contract.
		Requirement Requirement `json:"requirement"`
		// Policy records the exact evaluators and qualification policy.
		Policy PolicyRecord `json:"policy"`
		// Results contains every example in caller order.
		Results []ChallengeResult `json:"results"`
		// FalsePasses counts entailed decisions where the gold label is not entailed.
		FalsePasses int `json:"false_passes"`
		// FalseFailures counts substantive failures where the gold label is entailed.
		FalseFailures int `json:"false_failures"`
		// Unresolved counts explicit assessor abstentions, separately from failures.
		Unresolved int `json:"unresolved"`
		// Errors counts examples whose assessment could not finish correctly.
		Errors int `json:"errors"`
		// Duration measures all challenge work, including audits and adjudication.
		Duration time.Duration `json:"duration"`
		// Passed requires every example to match its reviewed label.
		Passed bool `json:"passed"`
	}
)

// Challenge assesses an application-owned corpus with the normal engine policy.
// Meaning-preserving variants share a group and gold label; deliberate defects
// have explicit reviewed labels for the changed meaning. Existing corpus split
// rules still apply, but this operation never selects or adjusts thresholds.
// Timeout bounds one example; at most one example runs at a time.
func (e *Engine) Challenge(ctx context.Context, requirement Requirement, examples []LabeledExample, timeout time.Duration) (ChallengeReport, error) {
	if err := validateRequirement(requirement); err != nil {
		return ChallengeReport{}, err
	}
	if err := validateExamples(examples); err != nil {
		return ChallengeReport{}, err
	}
	if timeout <= 0 {
		return ChallengeReport{}, errors.New("challenge requires a positive per-example timeout")
	}
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.challenge")
	defer span.End()
	started := time.Now()
	report := ChallengeReport{Requirement: requirement, Policy: e.Policy(), Passed: true}
	for _, example := range examples {
		result := ChallengeResult{Example: example}
		caseStarted := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		reports, calls, err := e.assess(callCtx, digest(example), []Requirement{requirement}, map[string][]Subject{requirement.ID: {example.Subject}})
		if err == nil {
			err = callCtx.Err()
		}
		cancel()
		result.Duration = time.Since(caseStarted)
		result.Calls = calls
		if len(reports) > 0 && len(reports[0].Instances) > 0 {
			result.Assessment = reports[0].Instances[0]
		}
		if err != nil {
			result.Error = err.Error()
			report.Errors++
		} else if label, resolved := result.Assessment.Label(); resolved {
			result.Matches = label == example.Expected
			if label == Entailed && example.Expected != Entailed {
				report.FalsePasses++
			}
			if label != Entailed && example.Expected == Entailed {
				report.FalseFailures++
			}
		} else {
			report.Unresolved++
		}
		report.Passed = report.Passed && result.Matches
		report.Results = append(report.Results, result)
	}
	report.Duration = time.Since(started)
	span.SetAttributes(attribute.Int("eval.challenge.count", len(examples)), attribute.Bool("eval.passed", report.Passed))
	if err := ctx.Err(); err != nil {
		recordError(span, err)
		return report, err
	}
	return report, nil
}
