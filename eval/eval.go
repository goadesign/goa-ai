// Package eval captures typed product observations and assesses saved evidence.
// Generated suites own assertion identities and field selection. Application
// hooks own product execution and exact predicates; an Engine owns semantic
// assessment. Product failures, unresolved assessments, and execution errors
// remain distinct in reports.
package eval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// Label describes the relationship between observed content and a requirement.
	Label string

	// Check records one deterministic assertion over saved evidence.
	Check struct {
		// Name identifies the assertion declared in the scenario design.
		Name string `json:"name"`
		// Passed reports whether the exact predicate held.
		Passed bool `json:"passed"`
		// Diagnostic explains a failed assertion and is empty on success.
		Diagnostic string `json:"diagnostic,omitempty"`
	}

	// Claim supplies a statement and a request-local identity to an assessor.
	// Generated requirements, rather than capture hooks, produce these values.
	Claim struct {
		// ID identifies one requirement instance in the request.
		ID string `json:"id"`
		// Text states what the subject must establish.
		Text string `json:"text"`
	}

	// Requirement fixes an assertion and the fields that supply its evidence.
	Requirement struct {
		// ID is the generated suite/scenario/requirement identity.
		ID string `json:"id"`
		// SchemaID is the SHA-256 identity of the captured observation schema.
		// Changing field shapes or descriptions requires new qualification.
		SchemaID string `json:"schema_id"`
		// Statement describes the required behavior.
		Statement string `json:"statement"`
		// Subject is the field selector compiled into the suite.
		Subject string `json:"subject"`
		// Evidence lists the compiled factual-context selectors.
		Evidence []string `json:"evidence,omitempty"`
		// ForEach is the compiled array selector, or empty for one instance.
		ForEach string `json:"for_each,omitempty"`
	}

	// Subject keeps assessed content separate from factual context. Generated
	// bindings create one value per selected observation or array element.
	Subject struct {
		// Content is the captured answer, action, or other selected value.
		Content string `json:"content"`
		// Reference contains only the selected captured facts.
		Reference string `json:"reference,omitempty"`
	}

	// Binding contains exact-check results and the subjects selected by generated
	// code. Every declared requirement has a map entry, including empty arrays.
	Binding struct {
		// Checks contains exactly the declared checks, in declaration order.
		Checks []Check
		// Subjects maps requirement identities to independently assessed values.
		Subjects map[string][]Subject
	}

	// Scenario is the compiled capture and assessment contract for one case.
	Scenario struct {
		// ID is the stable scenario identifier.
		ID string
		// Description explains the product behavior being exercised.
		Description string
		// Tags classify the scenario for selection.
		Tags []string
		// Timeout bounds one capture or assessment operation, independently.
		Timeout time.Duration
		// Schema is the generated observation JSON Schema used for identity checks.
		Schema string
		// Input is the validated, encoded capture input, when the scenario has one.
		Input []byte
		// CheckNames fixes the exact predicates this scenario must execute.
		CheckNames []string
		// Requirements fixes every semantic assertion in declaration order.
		Requirements []Requirement
		// Capture invokes the typed hook and its generated observation encoder.
		// Assessment-only suites omit this function.
		Capture func(context.Context) (rawjson.Message, error)
		// Bind validates saved bytes, runs typed exact predicates, and selects
		// subjects. It must use only those bytes, never live product data.
		Bind func(rawjson.Message) (Binding, error)
	}

	// Suite groups compiled scenarios in their declaration order.
	Suite struct {
		// ID is the stable suite identifier.
		ID string
		// Description explains the capability the suite exercises.
		Description string
		// Scenarios contains the compiled cases.
		Scenarios []Scenario
	}

	// Judgment is a reasoned semantic decision, with an explanation.
	Judgment struct {
		// ClaimID identifies the assessed requirement instance.
		ClaimID string `json:"claim_id"`
		// Label is the subject-to-requirement relationship.
		Label Label `json:"label"`
		// Rationale explains the decision using supplied evidence.
		Rationale string `json:"rationale"`
	}

	// Reporter observes assessment progress. Independent scenarios can call it
	// concurrently; a canceled scenario may finish without starting.
	Reporter interface {
		// ScenarioStarted reports that assessment is about to begin.
		ScenarioStarted(scenarioID string, startedAt time.Time)
		// ScenarioFinished reports all completed checks and any assessment error.
		ScenarioFinished(ScenarioReport)
	}

	// RequirementReport keeps empty quantified requirements visible in reports.
	RequirementReport struct {
		// Requirement is the exact definition used in this assessment.
		Requirement Requirement `json:"requirement"`
		// Instances contains decisions in captured array order, or one decision
		// when the requirement has no ForEach selector.
		Instances []Assessment `json:"instances"`
	}

	// ScenarioReport records an assessment without changing its observation.
	ScenarioReport struct {
		// ID identifies the scenario.
		ID string `json:"id"`
		// ObservationID identifies the exact captured record.
		ObservationID string `json:"observation_id"`
		// StartedAt is zero when cancellation prevented assessment from starting.
		StartedAt time.Time `json:"started_at"`
		// Duration measures assessment work only, including audits and adjudication.
		Duration time.Duration `json:"duration"`
		// CaptureDuration measures the original product capture operation.
		CaptureDuration time.Duration `json:"capture_duration"`
		// Checks contains every completed exact predicate.
		Checks []Check `json:"checks,omitempty"`
		// Requirements retains definitions and all completed instance decisions.
		Requirements []RequirementReport `json:"requirements,omitempty"`
		// Calls records model work, including failed calls and structural corrections.
		Calls []ModelCall `json:"calls,omitempty"`
		// Error describes a capture, binding, protocol, or assessor failure.
		Error string `json:"error,omitempty"`
		// Passed requires every exact and semantic assertion to pass without error.
		Passed bool `json:"passed"`
	}

	// Report is one independent assessment of an immutable capture archive.
	Report struct {
		// ID is the SHA-256 identity of all other assessment fields.
		ID string `json:"id"`
		// SuiteID identifies the compiled suite.
		SuiteID string `json:"suite_id"`
		// ArchiveID identifies the exact capture archive.
		ArchiveID string `json:"archive_id"`
		// StartedAt records when this assessment began.
		StartedAt time.Time `json:"started_at"`
		// Duration is the assessment's wall-clock duration.
		Duration time.Duration `json:"duration"`
		// Policy records the actual evaluators, qualifications, and audit policy.
		Policy PolicyRecord `json:"policy"`
		// Provenance records the caller's evaluation revision and configuration.
		// It is separate from the product revision in the capture archive.
		Provenance map[string]string `json:"provenance,omitempty"`
		// Scenarios contains results in suite declaration order.
		Scenarios []ScenarioReport `json:"scenarios"`
		// Error describes a suite-level selection, identity, or cancellation failure.
		Error string `json:"error,omitempty"`
		// Passed requires every selected scenario to pass.
		Passed bool `json:"passed"`
	}
)

const (
	// Entailed means the subject establishes the requirement.
	Entailed Label = "entailed"
	// Contradicted means the subject establishes that the requirement is false.
	Contradicted Label = "contradicted"
	// NotAddressed means the subject neither establishes nor contradicts it.
	NotAddressed Label = "not_addressed"
	// Indeterminate means the product content itself is ambiguous. It does not
	// mean that an evaluator is uncertain; that is an Unresolved decision.
	Indeterminate Label = "indeterminate"
)

// ValidateJudgments checks complete, unique decisions for the supplied claims.
func ValidateJudgments(claims []Claim, judgments []Judgment) error {
	if err := ValidateClaims(claims); err != nil {
		return err
	}
	wanted := claimIDs(claims)
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

// ValidateClaims requires a unique identity and a statement for each claim.
func ValidateClaims(claims []Claim) error {
	seen := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if claim.ID == "" || claim.Text == "" {
			return errors.New("claim ID and text are required")
		}
		if _, exists := seen[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		seen[claim.ID] = struct{}{}
	}
	return nil
}

func claimIDs(claims []Claim) map[string]struct{} {
	ids := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		ids[claim.ID] = struct{}{}
	}
	return ids
}

// validateBinding rejects missing assertions at the generated-code boundary.
// An empty quantified requirement is distinct from an omitted requirement.
func validateBinding(scenario Scenario, binding Binding) error {
	if len(binding.Checks) != len(scenario.CheckNames) {
		return errors.New("binding must return every declared check")
	}
	for i, name := range scenario.CheckNames {
		check := binding.Checks[i]
		if check.Name != name || check.Passed != (check.Diagnostic == "") {
			return fmt.Errorf("invalid result for declared check %q", name)
		}
	}
	if len(binding.Subjects) != len(scenario.Requirements) {
		return errors.New("binding must include every declared requirement")
	}
	for _, requirement := range scenario.Requirements {
		subjects, exists := binding.Subjects[requirement.ID]
		if !exists {
			return fmt.Errorf("binding omitted requirement %q", requirement.ID)
		}
		if requirement.ForEach == "" && len(subjects) != 1 {
			return fmt.Errorf("requirement %q requires exactly one subject", requirement.ID)
		}
	}
	return nil
}

func validLabel(label Label) bool {
	switch label {
	case Entailed, Contradicted, NotAddressed, Indeterminate:
		return true
	default:
		return false
	}
}
