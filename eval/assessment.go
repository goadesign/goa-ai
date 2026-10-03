// Package eval defines semantic assessor contracts and the three distinct decision
// forms returned by the engine. A probability-only model never invents a
// rationale, and an assessor's inability to decide never becomes a product label.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// EvaluatorConfig records all settings that can change an assessor's decisions.
	// Secrets and transport addresses must not appear in this value.
	EvaluatorConfig struct {
		// ID identifies the implementation and its protocol revision.
		ID string `json:"id"`
		// Model is the requested model version or the reasoner's routing class.
		Model string `json:"model"`
		// Instructions contains the exact common semantic instructions.
		Instructions string `json:"instructions"`
		// Settings contains additional decision-affecting settings.
		Settings map[string]string `json:"settings,omitempty"`
	}

	// ModelCall records one actual provider invocation, including corrections.
	ModelCall struct {
		// Stage identifies classification, reasoning, adjudication, or sanity work.
		Stage string `json:"stage"`
		// Duration measures this invocation, not the product run.
		Duration time.Duration `json:"duration"`
		// Usage is the provider's validated usage and actual model identity. Nil
		// means usage was unavailable, not that the call was free.
		Usage *model.TokenUsage `json:"usage,omitempty"`
		// Error retains an invocation failure even when a format correction succeeds.
		Error string `json:"error,omitempty"`
	}

	// Prediction is a native classifier's complete distribution for one claim.
	Prediction struct {
		// ClaimID identifies the request-local requirement instance.
		ClaimID string `json:"claim_id"`
		// Label is the provider's selected most-probable semantic label.
		Label Label `json:"label"`
		// Probabilities contains exactly the four semantic labels and sums to one.
		Probabilities map[Label]float64 `json:"probabilities"`
	}

	// Classification preserves the complete predictions and paid provider work.
	Classification struct {
		// Predictions covers every requested claim exactly once.
		Predictions []Prediction
		// Calls retains actual calls, including on failure.
		Calls []ModelCall
	}

	// Classifier answers typed classification questions without generating prose.
	// Implementations must be safe for concurrent calls and pin their model version.
	Classifier interface {
		// Config returns the complete, immutable decision configuration.
		Config() EvaluatorConfig
		// Classify evaluates claims sharing the same subject and reference.
		Classify(context.Context, string, []Claim, string) (Classification, error)
	}

	// Abstention records why a reasoner could not decide one requirement.
	Abstention struct {
		// ClaimID identifies the unresolved instance.
		ClaimID string `json:"claim_id"`
		// Reason explains what prevented a decision.
		Reason string `json:"reason"`
	}

	// Reasoning covers each requested claim with either a judgment or abstention.
	Reasoning struct {
		// Judgments contains substantive decisions with their explanations.
		Judgments []Judgment
		// Abstentions contains explicit unresolved decisions.
		Abstentions []Abstention
		// Calls retains actual invocations, including failed format corrections.
		Calls []ModelCall
	}

	// Disagreement gives an adjudicator two conflicting assessments and the
	// qualification that makes the classifier's decision eligible for comparison.
	Disagreement struct {
		// Prediction contains the native classifier's original probabilities.
		Prediction Prediction `json:"prediction"`
		// Judgment is the independent, initially blind reasoned decision.
		Judgment Judgment `json:"judgment"`
		// QualificationID identifies the held-out evidence for the decision band.
		QualificationID string `json:"qualification_id"`
	}

	// Reasoner assesses evidence and resolves a supplied disagreement. The first
	// call receives no classifier prediction. Adjudicate is called at most once
	// per conflicting instance and may explicitly abstain.
	Reasoner interface {
		// Config describes the reasoner's instructions and model settings.
		Config() EvaluatorConfig
		// Reason makes an independent decision from the original evidence.
		Reason(context.Context, string, []Claim, string) (Reasoning, error)
		// Adjudicate receives original evidence plus the conflicting decisions.
		Adjudicate(context.Context, string, []Claim, string, []Disagreement) (Reasoning, error)
	}

	// Decision is a closed set of calibrated, reasoned, and unresolved outcomes.
	// Each concrete form retains only the evidence that its evaluator produced.
	Decision interface {
		decisionLabel() (Label, bool)
	}

	// Calibrated is an automatic decision supported by a qualified probability band.
	Calibrated struct {
		// Prediction contains the full native probability distribution.
		Prediction Prediction `json:"prediction"`
		// Model is the exact classifier version used.
		Model string `json:"model"`
		// QualificationID identifies the reviewed held-out evidence.
		QualificationID string `json:"qualification_id"`
	}

	// Reasoned is a substantive decision backed by an explanation.
	Reasoned struct {
		// Judgment contains the semantic label and original rationale.
		Judgment Judgment `json:"judgment"`
	}

	// Unresolved records an assessor's explicit inability to decide.
	Unresolved struct {
		// Reason explains what prevented a substantive decision.
		Reason string `json:"reason"`
	}

	// Assessment records one requirement instance and every substantive decision.
	Assessment struct {
		// ID is derived from the requirement and its captured array index.
		ID string `json:"id"`
		// Decision is absent only when an infrastructure or protocol error occurred.
		Decision Decision `json:"decision,omitempty"`
		// Prediction retains the classifier's distribution even after reasoning.
		Prediction *Prediction `json:"prediction,omitempty"`
		// Initial retains the blind reasoner's decision when adjudication followed.
		Initial *Judgment `json:"initial,omitempty"`
		// Audited reports that a reproducibly selected automatic pass was reasoned.
		Audited bool `json:"audited"`
		// Disagreement reports a qualified pass/fail conflict requiring adjudication.
		Disagreement bool `json:"disagreement"`
	}

	// PolicyRecord makes an assessment's routing decisions reproducible.
	PolicyRecord struct {
		// Strategy is exact, reasoning, disagreement, or selective.
		Strategy string `json:"strategy"`
		// Classifier describes the native probabilistic model, when used.
		Classifier *EvaluatorConfig `json:"classifier,omitempty"`
		// Reasoner describes the reasoning model, when used.
		Reasoner *EvaluatorConfig `json:"reasoner,omitempty"`
		// AuditFraction is the inclusive probability of auditing an automatic pass.
		AuditFraction float64 `json:"audit_fraction"`
		// Qualifications records the exact reviewed qualifications used.
		Qualifications []Qualification `json:"qualifications,omitempty"`
	}
)

// Passed reports whether a completed instance established its requirement.
func (a Assessment) Passed() bool {
	if a.Decision == nil {
		return false
	}
	label, resolved := a.Decision.decisionLabel()
	return resolved && label == Entailed
}

// Label returns the substantive label, or false for errors and abstentions.
func (a Assessment) Label() (Label, bool) {
	if a.Decision == nil {
		return "", false
	}
	return a.Decision.decisionLabel()
}

// MarshalJSON identifies the probability-only form without fabricating a rationale.
func (d Calibrated) MarshalJSON() ([]byte, error) {
	type value Calibrated
	return json.Marshal(struct {
		Kind string `json:"kind"`
		value
	}{Kind: "calibrated", value: value(d)})
}

// MarshalJSON identifies the form containing an explanation of the decision.
func (d Reasoned) MarshalJSON() ([]byte, error) {
	type value Reasoned
	return json.Marshal(struct {
		Kind string `json:"kind"`
		value
	}{Kind: "reasoned", value: value(d)})
}

// MarshalJSON identifies an unresolved assessment separately from product labels.
func (d Unresolved) MarshalJSON() ([]byte, error) {
	type value Unresolved
	return json.Marshal(struct {
		Kind string `json:"kind"`
		value
	}{Kind: "unresolved", value: value(d)})
}

func (d Calibrated) decisionLabel() (Label, bool) {
	return d.Prediction.Label, true
}

func (d Reasoned) decisionLabel() (Label, bool) {
	return d.Judgment.Label, true
}

func (Unresolved) decisionLabel() (Label, bool) {
	return "", false
}

// ValidateClassification rejects incomplete, non-finite, or inconsistent model
// output. The sum tolerance covers floating-point serialization of one four-way
// distribution; it does not change, clip, or normalize the supplied probabilities.
func ValidateClassification(claims []Claim, result Classification) error {
	if err := ValidateClaims(claims); err != nil {
		return err
	}
	if len(result.Predictions) != len(claims) {
		return fmt.Errorf("got %d predictions for %d claims", len(result.Predictions), len(claims))
	}
	wanted := claimIDs(claims)
	seen := make(map[string]bool, len(claims))
	for _, prediction := range result.Predictions {
		if _, exists := wanted[prediction.ClaimID]; !exists || seen[prediction.ClaimID] {
			return fmt.Errorf("unknown or duplicate prediction %q", prediction.ClaimID)
		}
		seen[prediction.ClaimID] = true
		if !validLabel(prediction.Label) || len(prediction.Probabilities) != 4 {
			return fmt.Errorf("prediction %q requires four semantic probabilities", prediction.ClaimID)
		}
		sum, largest := 0.0, 0.0
		for _, label := range []Label{Entailed, Contradicted, NotAddressed, Indeterminate} {
			probability, exists := prediction.Probabilities[label]
			if !exists || !unitInterval(probability) {
				return fmt.Errorf("prediction %q has invalid probability for %q", prediction.ClaimID, label)
			}
			sum += probability
			largest = max(largest, probability)
		}
		if math.Abs(sum-1) > 1e-6 {
			return fmt.Errorf("prediction %q probabilities sum to %g, want one", prediction.ClaimID, sum)
		}
		if prediction.Probabilities[prediction.Label] != largest {
			return fmt.Errorf("prediction %q selected a less probable label", prediction.ClaimID)
		}
	}
	return validateCalls(result.Calls)
}

// validateReasoning requires exactly one substantive or unresolved outcome for
// every claim. A malformed batch cannot turn into a partial passing assessment.
func validateReasoning(claims []Claim, result Reasoning) error {
	if err := ValidateClaims(claims); err != nil {
		return err
	}
	byID := make(map[string]Claim, len(claims))
	for _, claim := range claims {
		byID[claim.ID] = claim
	}
	if len(result.Judgments)+len(result.Abstentions) != len(claims) {
		return errors.New("reasoning must cover every claim exactly once")
	}
	for _, judgment := range result.Judgments {
		claim, exists := byID[judgment.ClaimID]
		if !exists {
			return fmt.Errorf("unknown or duplicate judgment %q", judgment.ClaimID)
		}
		if err := ValidateJudgments([]Claim{claim}, []Judgment{judgment}); err != nil {
			return err
		}
		delete(byID, judgment.ClaimID)
	}
	for _, abstention := range result.Abstentions {
		if _, exists := byID[abstention.ClaimID]; !exists || abstention.Reason == "" {
			return fmt.Errorf("invalid abstention for %q", abstention.ClaimID)
		}
		delete(byID, abstention.ClaimID)
	}
	return validateCalls(result.Calls)
}

func unitInterval(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
