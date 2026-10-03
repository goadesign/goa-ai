// Package judge adapts the existing typed-tool judge to the assessment engine.
// Initial reasoning sees only captured content and facts. Adjudication adds the
// two conflicting assessments and allows an explicit, terminal abstention.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

const (
	abstentionPrompt   = "\nIf the supplied evidence does not permit you to make a reliable assessment, use unresolved and explain why in the rationale. Evaluator uncertainty is not indeterminate: indeterminate describes ambiguity in the product content."
	adjudicationPrompt = "\nIndependently resolve the supplied pass/fail disagreements using the original subject and reference. Both earlier assessments may be wrong. Do not treat their confidence or persuasive wording as evidence about the product. If the disagreement cannot be resolved from the supplied evidence, use unresolved and explain what is missing. There will be no further adjudication."
)

// Config records the exact semantic instructions, model class, and response
// allowance. Actual provider model identities are retained on individual calls.
func (j *Judge) Config() eval.EvaluatorConfig {
	return eval.EvaluatorConfig{
		ID: "goa-ai/typed-judge/1", Model: string(j.modelClass),
		Instructions: judgePrompt + abstentionPrompt + adjudicationPrompt,
		Settings: map[string]string{
			"max_output_tokens": strconv.Itoa(j.maxOutputTokens),
			"tool_choice":       string(j.toolChoice),
			"temperature":       "0",
		},
	}
}

// Reason makes a blind assessment and retains every provider invocation used
// for bounded structural corrections. It can explicitly abstain for a claim.
func (j *Judge) Reason(ctx context.Context, subject string, claims []eval.Claim, reference string) (eval.Reasoning, error) {
	payload, err := json.Marshal(requestBody{Output: subject, Reference: reference})
	if err != nil {
		return eval.Reasoning{}, err
	}
	return j.assess(ctx, claims, payload, judgePrompt+abstentionPrompt, "reasoning")
}

// Adjudicate receives original evidence and one conflict per claim. It makes one
// substantive decision per claim; only invalid output shape permits the existing
// bounded format corrections. An unresolved result is returned without retrying.
func (j *Judge) Adjudicate(ctx context.Context, subject string, claims []eval.Claim, reference string, conflicts []eval.Disagreement) (eval.Reasoning, error) {
	if len(conflicts) != len(claims) {
		return eval.Reasoning{}, errors.New("adjudication requires one disagreement per claim")
	}
	byID := make(map[string]eval.Disagreement, len(conflicts))
	for _, conflict := range conflicts {
		id := conflict.Judgment.ClaimID
		if _, exists := byID[id]; exists || conflict.Prediction.ClaimID != id || conflict.QualificationID == "" {
			return eval.Reasoning{}, errors.New("invalid adjudication disagreement identity")
		}
		byID[id] = conflict
	}
	for _, claim := range claims {
		conflict, exists := byID[claim.ID]
		if !exists {
			return eval.Reasoning{}, fmt.Errorf("missing disagreement for %q", claim.ID)
		}
		if err := eval.ValidateJudgments([]eval.Claim{claim}, []eval.Judgment{conflict.Judgment}); err != nil {
			return eval.Reasoning{}, err
		}
		if err := eval.ValidateClassification([]eval.Claim{claim}, eval.Classification{Predictions: []eval.Prediction{conflict.Prediction}}); err != nil {
			return eval.Reasoning{}, err
		}
	}
	payload, err := json.Marshal(struct {
		requestBody
		Disagreements map[string]eval.Disagreement `json:"disagreements"`
	}{requestBody: requestBody{Output: subject, Reference: reference}, Disagreements: byID})
	if err != nil {
		return eval.Reasoning{}, err
	}
	return j.assess(ctx, claims, payload, judgePrompt+abstentionPrompt+adjudicationPrompt, "adjudication")
}

// assess shares the judge's typed tool, output allowance, and correction flow.
// A call-local observer records validated usage without changing other calls.
func (j *Judge) assess(ctx context.Context, claims []eval.Claim, payload []byte, prompt, stage string) (eval.Reasoning, error) {
	if len(claims) == 0 {
		return eval.Reasoning{}, errors.New("reasoning requires at least one claim")
	}
	if err := eval.ValidateClaims(claims); err != nil {
		return eval.Reasoning{}, err
	}
	spec, err := decisionToolSpec(claims, true)
	if err != nil {
		return eval.Reasoning{}, err
	}
	observer := &callRecorder{stage: stage}
	client, err := model.WrapClient(j.client, func(provider model.Provider) model.Provider {
		return &observedProvider{Provider: provider, recorder: observer}
	})
	if err != nil {
		return eval.Reasoning{}, err
	}
	tracked := *j
	tracked.client = client
	response, err := tracked.run(ctx, payload, spec, prompt)
	result := eval.Reasoning{Calls: observer.snapshot()}
	if err != nil {
		return result, err
	}
	for _, claim := range claims {
		decision := response[claim.ID]
		if decision.Label == "unresolved" {
			result.Abstentions = append(result.Abstentions, eval.Abstention{ClaimID: claim.ID, Reason: decision.Rationale})
		} else {
			result.Judgments = append(result.Judgments, eval.Judgment{ClaimID: claim.ID, Label: decision.Label, Rationale: decision.Rationale})
		}
	}
	return result, nil
}
