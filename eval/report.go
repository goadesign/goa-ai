// Package eval persists independent assessment records. Each JSON document retains
// its concrete decision forms and content identity; concatenated documents can
// be appended to a caller-owned file without rewriting any captured observation.
package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/model"
)

// ReadReports reads appended assessment documents in order and verifies each
// content hash and outcome. On failure, it returns earlier verified reports.
// The caller owns the reader and any file or network size limit.
func ReadReports(reader io.Reader) ([]Report, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var reports []Report
	for {
		var report Report
		if err := decoder.Decode(&report); err != nil {
			if errors.Is(err, io.EOF) {
				return reports, nil
			}
			return reports, fmt.Errorf("read assessment: %w", err)
		}
		if err := report.Validate(); err != nil {
			return reports, err
		}
		reports = append(reports, report)
	}
}

// WriteTo verifies and writes one assessment as a JSON line. Callers can open a
// file for append and write later assessments without changing earlier records.
func (r Report) WriteTo(writer io.Writer) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return 0, fmt.Errorf("encode assessment: %w", err)
	}
	return bytes.NewReader(append(data, '\n')).WriteTo(writer)
}

// Validate checks a saved assessment's identity and derived pass/fail results.
// A content hash detects changes; it does not authenticate a report's author.
func (r Report) Validate() error {
	if r.SuiteID == "" || r.ArchiveID == "" || r.StartedAt.IsZero() || r.Duration < 0 {
		return errors.New("assessment requires suite, archive, time, and duration")
	}
	id := r.ID
	r.ID = ""
	encoded, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("invalid assessment record: %w", err)
	}
	if id != bytesDigest(encoded) {
		return errors.New("assessment content hash does not match")
	}
	if err := validatePolicy(r.Policy); err != nil {
		return err
	}
	qualifications := make(map[string]Qualification, len(r.Policy.Qualifications))
	for _, qualification := range r.Policy.Qualifications {
		qualifications[qualification.ID] = qualification
	}
	passed := r.Error == "" && len(r.Scenarios) > 0
	seen := make(map[string]bool, len(r.Scenarios))
	for _, scenario := range r.Scenarios {
		if scenario.ID == "" || scenario.ObservationID == "" || seen[scenario.ID] {
			return errors.New("assessment has a missing or duplicate scenario identity")
		}
		seen[scenario.ID] = true
		if err := validateScenarioReport(scenario, qualifications); err != nil {
			return fmt.Errorf("scenario %q: %w", scenario.ID, err)
		}
		passed = passed && scenario.Passed
	}
	if r.Passed != passed {
		return errors.New("assessment pass status does not match its outcomes")
	}
	return nil
}

// UnmarshalJSON restores the closed decision form in a saved assessment. Unknown
// kinds or fields fail decoding instead of discarding evidence.
func (a *Assessment) UnmarshalJSON(data []byte) error {
	type value Assessment
	var decoded value
	record := struct {
		*value
		Decision json.RawMessage `json:"decision"`
	}{value: &decoded}
	if err := decodeReportValue(data, &record); err != nil {
		return err
	}
	if len(record.Decision) > 0 && string(record.Decision) != "null" {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(record.Decision, &kind); err != nil {
			return err
		}
		switch kind.Kind {
		case "calibrated":
			var decision struct {
				Kind string `json:"kind"`
				Calibrated
			}
			if err := decodeReportValue(record.Decision, &decision); err != nil {
				return err
			}
			decoded.Decision = decision.Calibrated
		case "reasoned":
			var decision struct {
				Kind string `json:"kind"`
				Reasoned
			}
			if err := decodeReportValue(record.Decision, &decision); err != nil {
				return err
			}
			decoded.Decision = decision.Reasoned
		case "unresolved":
			var decision struct {
				Kind string `json:"kind"`
				Unresolved
			}
			if err := decodeReportValue(record.Decision, &decision); err != nil {
				return err
			}
			decoded.Decision = decision.Unresolved
		default:
			return fmt.Errorf("unknown assessment decision kind %q", kind.Kind)
		}
	}
	*a = Assessment(decoded)
	return nil
}

func decodeReportValue(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("assessment value contains trailing data")
	}
	return nil
}

func validatePolicy(policy PolicyRecord) error {
	if !unitInterval(policy.AuditFraction) {
		return errors.New("assessment has an invalid audit fraction")
	}
	switch policy.Strategy {
	case strategyExact:
		if policy.Classifier != nil || policy.Reasoner != nil || len(policy.Qualifications) > 0 || policy.AuditFraction != 0 {
			return errors.New("exact assessment contains semantic model configuration")
		}
	case strategyReasoning, strategyDisagreement, strategySelective:
		if policy.Reasoner == nil {
			return errors.New("semantic assessment lacks reasoner configuration")
		}
		if err := validateEvaluator(*policy.Reasoner); err != nil {
			return err
		}
		if policy.Strategy == strategyReasoning {
			if policy.Classifier != nil || len(policy.Qualifications) > 0 || policy.AuditFraction != 0 {
				return errors.New("reasoning assessment contains classifier configuration")
			}
		} else {
			if policy.Classifier == nil {
				return errors.New("classification assessment lacks classifier configuration")
			}
			if err := validateEvaluator(*policy.Classifier); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown assessment strategy %q", policy.Strategy)
	}
	for _, qualification := range policy.Qualifications {
		if err := qualification.Validate(); err != nil {
			return err
		}
		if digest(qualification.Evaluator) != digest(policy.Classifier) {
			return errors.New("qualification does not match the assessment classifier")
		}
	}
	return nil
}

func validateScenarioReport(report ScenarioReport, qualifications map[string]Qualification) error {
	if report.Duration < 0 || report.CaptureDuration < 0 {
		return errors.New("negative assessment or capture duration")
	}
	if err := validateCalls(report.Calls); err != nil {
		return err
	}
	passed := report.Error == ""
	if passed && len(report.Checks)+len(report.Requirements) == 0 {
		return errors.New("completed assessment contains no declared assertions")
	}
	for _, check := range report.Checks {
		if check.Name == "" || check.Passed != (check.Diagnostic == "") {
			return errors.New("invalid exact-check result")
		}
		passed = passed && check.Passed
	}
	for _, requirement := range report.Requirements {
		if err := validateRequirement(requirement.Requirement); err != nil {
			return err
		}
		if report.Error == "" && requirement.Requirement.ForEach == "" && len(requirement.Instances) != 1 {
			return errors.New("non-quantified requirement must have one assessment")
		}
		for i, instance := range requirement.Instances {
			id := requirement.Requirement.ID
			if requirement.Requirement.ForEach != "" {
				id = fmt.Sprintf("%s[%d]", id, i)
			}
			if instance.ID != id {
				return errors.New("requirement instance identity does not match its declaration")
			}
			if err := validateAssessment(instance, requirement.Requirement, report.Error != ""); err != nil {
				return err
			}
			if decision, ok := instance.Decision.(Calibrated); ok {
				qualification, exists := qualifications[decision.QualificationID]
				if !exists || qualification.Evaluator.Model != decision.Model ||
					digest(qualification.Requirement) != digest(requirement.Requirement) ||
					!qualifiedPass(&qualification, decision.Prediction) ||
					instance.Audited || digest(instance.Prediction) != digest(decision.Prediction) {
					return errors.New("calibrated decision is not supported by the recorded qualification")
				}
			}
			passed = passed && instance.Passed()
		}
	}
	if report.Passed != passed {
		return errors.New("scenario pass status does not match its outcomes")
	}
	return nil
}

func validateAssessment(assessment Assessment, requirement Requirement, failed bool) error {
	claims := []Claim{{ID: assessment.ID, Text: requirement.Statement}}
	if requirement.Reasoning && (assessment.Prediction != nil || assessment.Audited || assessment.Disagreement) {
		return errors.New("a requirement marked Reasoning cannot retain native routing")
	}
	if assessment.Prediction != nil {
		if err := ValidateClassification(claims, Classification{Predictions: []Prediction{*assessment.Prediction}}); err != nil {
			return err
		}
	}
	if assessment.Initial != nil {
		if err := ValidateJudgments(claims, []Judgment{*assessment.Initial}); err != nil {
			return err
		}
	}
	switch decision := assessment.Decision.(type) {
	case Calibrated:
		if decision.Model == "" || decision.QualificationID == "" || requirement.Reasoning {
			return errors.New("calibrated decision requires a qualified pass and model identity")
		}
		return ValidateClassification(claims, Classification{Predictions: []Prediction{decision.Prediction}})
	case Reasoned:
		return ValidateJudgments(claims, []Judgment{decision.Judgment})
	case Unresolved:
		if decision.Reason == "" {
			return errors.New("unresolved assessment requires a reason")
		}
	case nil:
		if !failed {
			return errors.New("missing decision without an assessment error")
		}
	default:
		return errors.New("unsupported decision representation")
	}
	return nil
}

// validateCalls delegates token-count validation to the runtime that owns those
// counts. Provider usage is optional, but negative durations are never valid.
func validateCalls(calls []ModelCall) error {
	for _, call := range calls {
		if call.Stage == "" || call.Duration < 0 {
			return errors.New("model call requires a stage and nonnegative duration")
		}
		if call.Usage != nil {
			if _, err := model.AddTokenUsage(model.TokenUsage{}, *call.Usage); err != nil {
				return err
			}
		}
	}
	return nil
}
