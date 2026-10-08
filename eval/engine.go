// Package eval routes fixed requirements through native classification and bounded
// reasoning. Only a held-out-qualified, unaudited pass can avoid reasoning.
// Exact predicates never enter this routing logic and cannot be overridden.
package eval

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Engine assesses bound requirements with one immutable routing policy.
	Engine struct {
		classifier     Classifier
		reasoner       Reasoner
		policy         PolicyRecord
		qualifications map[string]Qualification
	}

	// SelectivePolicy supplies the evidence and audit rate for automatic passes.
	SelectivePolicy struct {
		// Qualifications contains reviewed evidence for individual requirements.
		Qualifications []Qualification
		// AuditFraction selects a reproducible fraction of qualified passes for
		// independent reasoning. Zero disables auditing; one audits every pass.
		AuditFraction float64
	}

	assessmentTask struct {
		claim         Claim
		requirement   int
		instance      int
		qualification *Qualification
		native        bool
	}

	assessmentGroup struct {
		subject Subject
		tasks   []assessmentTask
	}
)

const (
	strategyExact        = "exact"
	strategyReasoning    = "reasoning"
	strategyDisagreement = "disagreement"
	strategySelective    = "selective"
)

// NewReasoningEngine assesses every subject with the reasoner. This
// operation provides the baseline for comparisons against selective assessment.
func NewReasoningEngine(reasoner Reasoner) (*Engine, error) {
	return newEngine(nil, reasoner, strategyReasoning, SelectivePolicy{})
}

// NewDisagreementEngine classifies and reasons about every eligible subject.
// Qualified pass/fail conflicts receive one additional adjudication. Classifier
// predictions never bypass the initial independent reasoning call.
func NewDisagreementEngine(classifier Classifier, reasoner Reasoner, qualifications []Qualification) (*Engine, error) {
	return newEngine(classifier, reasoner, strategyDisagreement, SelectivePolicy{Qualifications: qualifications})
}

// NewSelectiveEngine accepts qualified passes directly unless chosen for audit.
// Requirements without qualification, and requirements marked Reasoning, go
// directly to reasoning. Other predictions require reasoning; provider errors
// do not select a different assessor or silently become product decisions.
func NewSelectiveEngine(classifier Classifier, reasoner Reasoner, policy SelectivePolicy) (*Engine, error) {
	return newEngine(classifier, reasoner, strategySelective, policy)
}

// Policy returns an owned snapshot of the engine's configuration and evidence.
func (e *Engine) Policy() PolicyRecord {
	return cloneRecord(e.policy)
}

// CheckSemantics tests four synthetic cases. A reasoner distinguishes all four
// labels; a classifier distinguishes passing from failing. The caller must
// supply a deadline. Passing this check never creates a statistical
// qualification and never authorizes automatic application decisions.
func (e *Engine) CheckSemantics(ctx context.Context) ([]ModelCall, error) {
	if _, exists := ctx.Deadline(); !exists {
		return nil, errors.New("semantic sanity check requires a context deadline")
	}
	subject := "The pump is running. Two readings at the same time disagree about whether the fan is running."
	claims := []Claim{
		{ID: "sanity_entailed", Text: "The pump is running."},
		{ID: "sanity_contradicted", Text: "The pump is stopped."},
		{ID: "sanity_not_addressed", Text: "The compressor is running."},
		{ID: "sanity_indeterminate", Text: "The fan is running."},
	}
	expected := []Label{Entailed, Contradicted, NotAddressed, Indeterminate}
	var calls []ModelCall
	if e.classifier != nil {
		classified, err := e.classifier.Classify(ctx, subject, claims, "")
		calls = append(calls, classified.Calls...)
		if err != nil {
			return calls, err
		}
		if err := ValidateClassification(claims, classified); err != nil {
			return calls, err
		}
		byID := predictionMap(classified.Predictions)
		for i, claim := range claims {
			probability := byID[claim.ID].Probability
			if (probability > .5) != (expected[i] == Entailed) || probability == .5 {
				return calls, fmt.Errorf("classifier semantic sanity check failed for %s", claim.ID)
			}
		}
	}
	reasoned, err := e.reasoner.Reason(ctx, subject, claims, "")
	calls = append(calls, reasoned.Calls...)
	if err != nil {
		return calls, err
	}
	if err := validateReasoning(claims, reasoned); err != nil {
		return calls, err
	}
	byID := judgmentMap(reasoned.Judgments)
	for i, claim := range claims {
		if byID[claim.ID].Label != expected[i] {
			return calls, fmt.Errorf("reasoner semantic sanity check failed for %s", claim.ID)
		}
	}
	return calls, ctx.Err()
}

func newEngine(classifier Classifier, reasoner Reasoner, strategy string, policy SelectivePolicy) (*Engine, error) {
	if reasoner == nil {
		return nil, errors.New("assessment requires a reasoner")
	}
	configuration := reasoner.Config()
	if err := validateEvaluator(configuration); err != nil {
		return nil, err
	}
	engine := &Engine{
		classifier: classifier, reasoner: reasoner,
		policy:         PolicyRecord{Strategy: strategy, Reasoner: &configuration, AuditFraction: policy.AuditFraction},
		qualifications: make(map[string]Qualification, len(policy.Qualifications)),
	}
	if strategy != strategyReasoning {
		if classifier == nil {
			return nil, errors.New("this assessment strategy requires a native classifier")
		}
		configuration := classifier.Config()
		if err := validateEvaluator(configuration); err != nil {
			return nil, err
		}
		engine.policy.Classifier = &configuration
	}
	if !unitInterval(policy.AuditFraction) {
		return nil, errors.New("audit fraction must be finite and in [0,1]")
	}
	for _, qualification := range policy.Qualifications {
		if err := qualification.Validate(); err != nil {
			return nil, fmt.Errorf("invalid qualification: %w", err)
		}
		if qualification.Requirement.Reasoning {
			return nil, fmt.Errorf("requirement %q requires reasoning and cannot use a native qualification", qualification.Requirement.ID)
		}
		if digest(qualification.Evaluator) != digest(engine.policy.Classifier) {
			return nil, fmt.Errorf("qualification %q uses a different classifier configuration", qualification.ID)
		}
		if _, exists := engine.qualifications[qualification.Requirement.ID]; exists {
			return nil, fmt.Errorf("duplicate qualification for %q", qualification.Requirement.ID)
		}
		engine.qualifications[qualification.Requirement.ID] = cloneRecord(qualification)
		engine.policy.Qualifications = append(engine.policy.Qualifications, qualification)
	}
	engine.policy = cloneRecord(engine.policy)
	return engine, nil
}

// assess fixes identities and groups only subjects with byte-identical content
// and reference. Static requirements remain in declaration order in the report.
func (e *Engine) assess(ctx context.Context, observationID string, requirements []Requirement, subjects map[string][]Subject) ([]RequirementReport, []ModelCall, error) {
	if digest(e.reasoner.Config()) != digest(e.policy.Reasoner) || (e.classifier != nil && digest(e.classifier.Config()) != digest(e.policy.Classifier)) {
		return nil, nil, errors.New("evaluator configuration changed after engine construction")
	}
	reports := make([]RequirementReport, len(requirements))
	groups := make([]assessmentGroup, 0)
	groupIndex := make(map[Subject]int)
	for ri, requirement := range requirements {
		values := subjects[requirement.ID]
		reports[ri] = RequirementReport{Requirement: requirement, Instances: make([]Assessment, len(values))}
		var qualification *Qualification
		if saved, exists := e.qualifications[requirement.ID]; exists {
			if digest(saved.Requirement) != digest(requirement) {
				return nil, nil, fmt.Errorf("qualification is stale for requirement %q", requirement.ID)
			}
			qualification = &saved
		}
		for ii, subject := range values {
			id := requirement.ID
			if requirement.ForEach != "" {
				id = fmt.Sprintf("%s[%d]", id, ii)
			}
			reports[ri].Instances[ii] = Assessment{ID: id}
			index, exists := groupIndex[subject]
			if !exists {
				index = len(groups)
				groupIndex[subject] = index
				groups = append(groups, assessmentGroup{subject: subject})
			}
			groups[index].tasks = append(groups[index].tasks, assessmentTask{
				claim: Claim{ID: id, Text: requirement.Statement}, requirement: ri, instance: ii, qualification: qualification,
				native: !requirement.Reasoning && (e.policy.Strategy == strategyDisagreement ||
					qualification != nil && (qualification.Pass != nil && qualification.Pass.Qualified ||
						qualification.Fail != nil && qualification.Fail.Qualified)),
			})
		}
	}
	var calls []ModelCall
	for _, group := range groups {
		completed, err := e.assessGroup(ctx, observationID, group, reports)
		calls = append(calls, completed...)
		if err != nil {
			return reports, calls, err
		}
	}
	return reports, calls, ctx.Err()
}

// assessGroup first obtains native probabilities, then independently reasons
// about uncertain, unqualified, failing, and audited instances in one batch.
// Only qualified pass/fail conflicts enter the final, single adjudication call.
func (e *Engine) assessGroup(ctx context.Context, observationID string, group assessmentGroup, reports []RequirementReport) ([]ModelCall, error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.assess.requirements")
	defer span.End()
	span.SetAttributes(attribute.Int("eval.requirement.count", len(group.tasks)))
	if err := ctx.Err(); err != nil {
		recordError(span, err)
		return nil, err
	}
	var nativeTasks []assessmentTask
	for _, task := range group.tasks {
		if task.native {
			nativeTasks = append(nativeTasks, task)
		}
	}
	var calls []ModelCall
	predictions := make(map[string]Prediction)
	if e.classifier != nil && len(nativeTasks) > 0 {
		claims := taskClaims(nativeTasks)
		classified, err := e.classifier.Classify(ctx, group.subject.Content, claims, group.subject.Reference)
		calls = append(calls, classified.Calls...)
		if err == nil {
			err = ValidateClassification(claims, classified)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			recordError(span, err)
			return calls, fmt.Errorf("native classification: %w", err)
		}
		predictions = predictionMap(classified.Predictions)
	}
	pending := make([]assessmentTask, 0, len(group.tasks))
	for _, task := range group.tasks {
		assessment := &reports[task.requirement].Instances[task.instance]
		prediction, exists := predictions[task.claim.ID]
		if exists {
			assessment.Prediction = &prediction
		}
		if exists && e.policy.Strategy == strategySelective && task.qualification != nil && qualifiedPass(task.qualification, prediction) {
			assessment.Audited = auditSelected(observationID, task.claim.ID, task.qualification.ID, e.policy.AuditFraction)
			if !assessment.Audited {
				assessment.Decision = Calibrated{
					Prediction: prediction, Model: e.policy.Classifier.Model, QualificationID: task.qualification.ID,
				}
				continue
			}
		}
		pending = append(pending, task)
	}
	if len(pending) == 0 {
		return calls, nil
	}
	// This request contains only the original evidence. Preliminary decisions
	// are deliberately unavailable to the first reasoning call.
	reasoned, err := e.reasoner.Reason(ctx, group.subject.Content, taskClaims(pending), group.subject.Reference)
	calls = append(calls, reasoned.Calls...)
	if err == nil {
		err = validateReasoning(taskClaims(pending), reasoned)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		recordError(span, err)
		return calls, fmt.Errorf("reason about requirements: %w", err)
	}
	decisions := reasoningDecisions(reasoned)
	var conflicts []Disagreement
	var disputed []assessmentTask
	for _, task := range pending {
		assessment := &reports[task.requirement].Instances[task.instance]
		assessment.Decision = decisions[task.claim.ID]
		judgment, substantive := assessment.Decision.(Reasoned)
		if !substantive || task.qualification == nil {
			continue
		}
		prediction, exists := predictions[task.claim.ID]
		if !exists {
			continue
		}
		passConflict := qualifiedPass(task.qualification, prediction) && judgment.Judgment.Label != Entailed
		failConflict := qualifiedFail(task.qualification, prediction) && judgment.Judgment.Label == Entailed
		if !passConflict && !failConflict {
			continue
		}
		assessment.Initial = &judgment.Judgment
		assessment.Disagreement = true
		// An unresolved conflict cannot retain the first model's decision if
		// the adjudication request fails.
		assessment.Decision = nil
		conflicts = append(conflicts, Disagreement{Prediction: prediction, Judgment: judgment.Judgment, QualificationID: task.qualification.ID})
		disputed = append(disputed, task)
	}
	if len(disputed) == 0 {
		return calls, nil
	}
	span.AddEvent("qualified assessments disagree", trace.WithAttributes(attribute.Int("eval.disagreement.count", len(disputed))))
	adjudicated, err := e.reasoner.Adjudicate(ctx, group.subject.Content, taskClaims(disputed), group.subject.Reference, conflicts)
	calls = append(calls, adjudicated.Calls...)
	if err == nil {
		err = validateReasoning(taskClaims(disputed), adjudicated)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		recordError(span, err)
		return calls, fmt.Errorf("adjudicate disagreement: %w", err)
	}
	decisions = reasoningDecisions(adjudicated)
	for _, task := range disputed {
		reports[task.requirement].Instances[task.instance].Decision = decisions[task.claim.ID]
	}
	return calls, nil
}

func taskClaims(tasks []assessmentTask) []Claim {
	claims := make([]Claim, len(tasks))
	for i, task := range tasks {
		claims[i] = task.claim
	}
	return claims
}

func predictionMap(predictions []Prediction) map[string]Prediction {
	result := make(map[string]Prediction, len(predictions))
	for _, prediction := range predictions {
		result[prediction.ClaimID] = prediction
	}
	return result
}

func judgmentMap(judgments []Judgment) map[string]Judgment {
	result := make(map[string]Judgment, len(judgments))
	for _, judgment := range judgments {
		result[judgment.ClaimID] = judgment
	}
	return result
}

func reasoningDecisions(result Reasoning) map[string]Decision {
	decisions := make(map[string]Decision, len(result.Judgments)+len(result.Abstentions))
	for _, judgment := range result.Judgments {
		decisions[judgment.ClaimID] = Reasoned{Judgment: judgment}
	}
	for _, abstention := range result.Abstentions {
		decisions[abstention.ClaimID] = Unresolved{Reason: abstention.Reason}
	}
	return decisions
}

func qualifiedPass(qualification *Qualification, prediction Prediction) bool {
	return qualification.Pass != nil && qualification.Pass.Qualified && inBand(prediction, qualification.Pass.Threshold, true)
}

func qualifiedFail(qualification *Qualification, prediction Prediction) bool {
	return qualification.Fail != nil && qualification.Fail.Qualified && inBand(prediction, qualification.Fail.Threshold, false)
}

// auditSelected gives the same observation, requirement, and qualification a
// stable uniform draw. Raising the configured fraction can only add audits.
func auditSelected(observationID, claimID, qualificationID string, fraction float64) bool {
	hash := sha256.Sum256([]byte(digest([]string{observationID, claimID, qualificationID})))
	draw := float64(binary.BigEndian.Uint64(hash[:8])>>11) / (1 << 53)
	return draw < fraction
}

// cloneRecord owns plain persisted framework values so caller mutations cannot
// change an engine's configuration after its qualifications have been checked.
func cloneRecord[T any](value T) T {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("invalid constructed evaluation record: %v", err))
	}
	var copied T
	if err := json.Unmarshal(encoded, &copied); err != nil {
		panic(fmt.Sprintf("invalid constructed evaluation record: %v", err))
	}
	return copied
}
