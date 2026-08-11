// Package eval defines the runtime contracts shared by generated evaluation
// suites, application-owned hooks, runners, and semantic judges. This file
// owns the contract vocabulary — scenarios, results, checks, claims,
// judgments, and reports — together with the validity rules that give those
// types meaning. The execution engine lives in runner.go.
package eval

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type (
	// Label is the semantic relationship between an answer and a claim.
	Label string

	// Check records one deterministic assertion made by an application hook.
	Check struct {
		// Name identifies the asserted invariant within its scenario.
		Name string `json:"name"`
		// Passed reports whether the invariant held.
		Passed bool `json:"passed"`
		// Diagnostic explains a failed assertion.
		Diagnostic string `json:"diagnostic,omitempty"`
	}

	// Claim describes one semantic assertion to judge against model output.
	Claim struct {
		// ID uniquely identifies the claim within its scenario.
		ID string `json:"id"`
		// Text is the proposition the output is expected to entail.
		Text string `json:"text"`
	}

	// Artifact links supporting evidence produced while executing a scenario.
	Artifact struct {
		// Name identifies the evidence within its scenario.
		Name string `json:"name"`
		// URI locates the durable evidence.
		URI string `json:"uri"`
	}

	// Result is returned by an application hook after executing one scenario.
	Result struct {
		// Checks are deterministic assertions over typed application evidence.
		Checks []Check `json:"checks,omitempty"`
		// Claims are semantic assertions to judge against Output.
		Claims []Claim `json:"claims,omitempty"`
		// Output is the model-authored answer evaluated by Claims.
		Output string `json:"output,omitempty"`
		// Artifacts link durable evidence used to diagnose this result.
		Artifacts []Artifact `json:"artifacts,omitempty"`
	}

	// Scenario is one generated evaluation case and its application hook.
	Scenario struct {
		// ID is the stable lower_snake_case scenario identifier.
		ID string
		// Description states the behavior the scenario evaluates.
		Description string
		// Tags classify the scenario for explicit runner selection.
		Tags []string
		// Timeout is the maximum duration of the hook and its judgments.
		Timeout time.Duration
		// Run executes the application-owned scenario behavior.
		Run func(context.Context) (Result, error)
	}

	// Suite is a generated collection of scenarios.
	Suite struct {
		// ID is the stable lower_snake_case suite identifier.
		ID string
		// Description states the capability evaluated by the suite.
		Description string
		// Scenarios are the generated cases in declaration order.
		Scenarios []Scenario
	}

	// Judgment is the semantic label assigned to one claim.
	Judgment struct {
		// ClaimID identifies the judged claim.
		ClaimID string `json:"claim_id"`
		// Label is the answer-to-claim relationship.
		Label Label `json:"label"`
		// Rationale concisely explains the label.
		Rationale string `json:"rationale"`
	}

	// Assertion binds one output to one semantic claim for batched judging.
	Assertion struct {
		// ClaimID uniquely identifies the assertion within the request.
		ClaimID string `json:"claim_id"`
		// Output is the model-authored text being assessed.
		Output string `json:"output"`
		// Claim is the proposition to classify against Output.
		Claim string `json:"claim"`
	}

	// Judge assigns exactly one semantic judgment to each supplied claim. A
	// runner may call Judge concurrently for independent scenarios.
	Judge interface {
		Judge(context.Context, []Assertion) ([]Judgment, error)
	}

	// Reporter observes scenario lifecycle events. Runner may call its methods
	// concurrently for independent scenarios.
	Reporter interface {
		// ScenarioStarted reports that the hook identified by scenarioID is
		// about to run.
		ScenarioStarted(scenarioID string, startedAt time.Time)
		// ScenarioFinished reports the terminal outcome. It may be called
		// without ScenarioStarted when cancellation prevents the hook from
		// starting.
		ScenarioFinished(ScenarioReport)
	}

	// ScenarioReport records the outcome and evidence for one scenario.
	ScenarioReport struct {
		// ID identifies the scenario.
		ID string `json:"id"`
		// StartedAt is when execution began. It is zero when caller
		// cancellation prevented the hook from starting.
		StartedAt time.Time `json:"started_at"`
		// Duration is the total scenario duration.
		Duration time.Duration `json:"duration"`
		// Result is the validated hook result, when execution reached it.
		Result *Result `json:"result,omitempty"`
		// Judgments contains one entry per semantic claim.
		Judgments []Judgment `json:"judgments,omitempty"`
		// Error describes an execution, protocol, or judging failure.
		Error string `json:"error,omitempty"`
		// Passed reports whether all deterministic and semantic assertions passed.
		Passed bool `json:"passed"`
	}

	// Report records a complete suite run.
	Report struct {
		// SuiteID identifies the generated suite.
		SuiteID string `json:"suite_id"`
		// StartedAt is when suite execution began.
		StartedAt time.Time `json:"started_at"`
		// Duration is the total suite duration.
		Duration time.Duration `json:"duration"`
		// Scenarios contains results in generated declaration order.
		Scenarios []ScenarioReport `json:"scenarios"`
		// Error describes a suite-level selection, calibration, or caller
		// cancellation failure.
		Error string `json:"error,omitempty"`
		// Passed reports whether calibration and all selected scenarios passed.
		Passed bool `json:"passed"`
	}
)

const (
	// Entailed means the answer establishes the claim.
	Entailed Label = "entailed"
	// Contradicted means the answer establishes the claim is false.
	Contradicted Label = "contradicted"
	// NotAddressed means the answer neither establishes nor contradicts the claim.
	NotAddressed Label = "not_addressed"
	// Indeterminate means the answer is too ambiguous to classify.
	Indeterminate Label = "indeterminate"
)

// ValidateJudgments enforces an exact one-to-one relationship between claims
// and semantic judge output.
func ValidateJudgments(claims []Claim, judgments []Judgment) error {
	wanted := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if claim.ID == "" || claim.Text == "" {
			return errors.New("claim ID and text are required")
		}
		if _, exists := wanted[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		wanted[claim.ID] = struct{}{}
	}
	if len(judgments) != len(wanted) {
		return fmt.Errorf("got %d judgments for %d claims", len(judgments), len(wanted))
	}
	seen := make(map[string]struct{}, len(judgments))
	for _, judgment := range judgments {
		if _, exists := wanted[judgment.ClaimID]; !exists {
			return fmt.Errorf("judgment references unknown claim %q", judgment.ClaimID)
		}
		if _, exists := seen[judgment.ClaimID]; exists {
			return fmt.Errorf("duplicate judgment for claim %q", judgment.ClaimID)
		}
		if !validLabel(judgment.Label) {
			return fmt.Errorf("judgment for claim %q has invalid label %q", judgment.ClaimID, judgment.Label)
		}
		if judgment.Rationale == "" {
			return fmt.Errorf("judgment for claim %q requires a rationale", judgment.ClaimID)
		}
		seen[judgment.ClaimID] = struct{}{}
	}
	return nil
}

// validateResult enforces the hook-to-runner boundary contract.
func validateResult(result Result) error {
	if len(result.Checks) == 0 && len(result.Claims) == 0 {
		return errors.New("result must contain at least one check or claim")
	}
	checks := make(map[string]struct{}, len(result.Checks))
	for _, check := range result.Checks {
		if check.Name == "" {
			return errors.New("check name is required")
		}
		if _, exists := checks[check.Name]; exists {
			return fmt.Errorf("duplicate check %q", check.Name)
		}
		checks[check.Name] = struct{}{}
		if check.Passed && check.Diagnostic != "" {
			return fmt.Errorf("passed check %q must not have a diagnostic", check.Name)
		}
		if !check.Passed && check.Diagnostic == "" {
			return fmt.Errorf("failed check %q requires a diagnostic", check.Name)
		}
	}
	if len(result.Claims) > 0 && result.Output == "" {
		return errors.New("claims require output")
	}
	claims := make(map[string]struct{}, len(result.Claims))
	for _, claim := range result.Claims {
		if claim.ID == "" || claim.Text == "" {
			return errors.New("claim ID and text are required")
		}
		if _, exists := claims[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		claims[claim.ID] = struct{}{}
	}
	artifacts := make(map[string]struct{}, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		if artifact.Name == "" || artifact.URI == "" {
			return errors.New("artifact name and URI are required")
		}
		if _, exists := artifacts[artifact.Name]; exists {
			return fmt.Errorf("duplicate artifact %q", artifact.Name)
		}
		artifacts[artifact.Name] = struct{}{}
	}
	return nil
}

// validLabel reports whether a label belongs to the closed judge vocabulary.
func validLabel(label Label) bool {
	switch label {
	case Entailed, Contradicted, NotAddressed, Indeterminate:
		return true
	default:
		return false
	}
}
