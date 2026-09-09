// Package judge classifies evaluation claims with a forced typed tool. Invalid
// model arguments use the shared runtime correction flow before callers receive
// a result.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tooloutput"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Judge classifies semantic claims with one forced private tool.
	Judge struct {
		client          model.Client
		modelClass      model.ModelClass
		maxOutputTokens int
	}

	// Option customizes a Judge.
	Option func(*Judge)

	// requestBody separates the answer from shared factual context; the tool
	// schema describes each claim without duplicating that context.
	requestBody struct {
		Output    string `json:"output"`
		Reference string `json:"reference,omitempty"`
	}

	// responseBody associates each decision with its required schema property.
	responseBody map[string]modelJudgment

	// modelJudgment contains only the semantic decision the model must make.
	modelJudgment struct {
		Label     aieval.Label `json:"label"`
		Rationale string       `json:"rationale"`
	}

	// judgmentSchema describes one named claim without a second claim-text list.
	judgmentSchema struct {
		Type                 string                     `json:"type"`
		Description          string                     `json:"description,omitempty"`
		AdditionalProperties bool                       `json:"additionalProperties"` //nolint:tagliatelle // JSON Schema owns this spelling.
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
)

const (
	submitJudgmentsID completion.Ident = "eval.submit_judgments"

	judgePrompt = `Classify each claim independently against the supplied output.
Use the reference, when supplied, as factual context only. Do not credit the output with information that appears only in the reference.
Return entailed when the output establishes the claim, contradicted when it establishes the claim is false, not_addressed when it does neither, and indeterminate only when ambiguity prevents classification.
Call submit_judgments exactly once. Each required property describes one claim; supply its label and a concise rationale in that property.`
)

// New creates a semantic judge backed by client. maxOutputTokens must be positive
// and limits one complete model response, including all judgments. Every permitted
// correction uses the same limit; the limit does not guarantee a complete response.
// The judge uses the high-reasoning class unless WithModelClass selects another.
func New(client model.Client, maxOutputTokens int, opts ...Option) (*Judge, error) {
	if maxOutputTokens <= 0 {
		return nil, errors.New("judge max output tokens must be positive")
	}
	judge := &Judge{
		client:          client,
		modelClass:      model.ModelClassHighReasoning,
		maxOutputTokens: maxOutputTokens,
	}
	for _, opt := range opts {
		opt(judge)
	}
	return judge, nil
}

// WithModelClass selects the model class used by the private judge agent.
func WithModelClass(class model.ModelClass) Option {
	return func(j *Judge) { j.modelClass = class }
}

// Judge classifies claims against one model-authored output. Each claim ID names
// a required tool property; results are returned in the caller's claim order.
// Reference supplies shared factual context, not content missing from output.
func (j *Judge) Judge(ctx context.Context, output string, claims []aieval.Claim, reference string) ([]aieval.Judgment, error) {
	if len(claims) == 0 {
		return nil, errors.New("judge requires at least one claim")
	}
	if err := aieval.ValidateClaims(claims); err != nil {
		return nil, fmt.Errorf("judge claims: %w", err)
	}
	for _, claim := range claims {
		if !utf8.ValidString(claim.ID) {
			return nil, fmt.Errorf("judge claim ID %q is not valid UTF-8", claim.ID)
		}
	}
	payload, err := json.Marshal(requestBody{Output: output, Reference: reference})
	if err != nil {
		return nil, fmt.Errorf("encode judge request: %w", err)
	}
	spec, err := judgmentToolSpec(claims)
	if err != nil {
		return nil, fmt.Errorf("encode judge schema: %w", err)
	}
	response, err := j.run(ctx, payload, spec)
	if err != nil {
		return nil, fmt.Errorf("judge claims: %w", err)
	}
	judgments := make([]aieval.Judgment, len(claims))
	for index, claim := range claims {
		judgment := response[claim.ID]
		judgments[index] = aieval.Judgment{
			ClaimID:   claim.ID,
			Label:     judgment.Label,
			Rationale: judgment.Rationale,
		}
	}
	if err := aieval.ValidateJudgments(claims, judgments); err != nil {
		return nil, fmt.Errorf("invalid judge response: %w", err)
	}
	return judgments, nil
}

func (j *Judge) run(ctx context.Context, payload []byte, spec completion.Spec[responseBody]) (responseBody, error) {
	return tooloutput.Run[responseBody](ctx, j.client, &model.Request{
		ModelClass: j.modelClass,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleSystem,
				Parts: []model.Part{model.TextPart{Text: judgePrompt}},
			},
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: string(payload)}},
			},
		},
		Temperature: 0,
		MaxTokens:   j.maxOutputTokens,
	}, spec)
}

// judgmentToolSpec makes names and coverage part of the advertised schema. The
// shared model validator owns required/unknown fields, labels, and rationales.
func judgmentToolSpec(claims []aieval.Claim) (completion.Spec[responseBody], error) {
	schema := judgmentSchema{
		Type:       "object",
		Required:   make([]string, len(claims)),
		Properties: make(map[string]json.RawMessage, len(claims)),
	}
	for index, claim := range claims {
		property, err := json.Marshal(judgmentSchema{
			Type:        "object",
			Description: claim.Text,
			Required:    []string{"label", "rationale"},
			Properties: map[string]json.RawMessage{
				"label":     json.RawMessage(`{"type":"string","enum":["entailed","contradicted","not_addressed","indeterminate"]}`),
				"rationale": json.RawMessage(`{"type":"string","minLength":1}`),
			},
		})
		if err != nil {
			return completion.Spec[responseBody]{}, err
		}
		schema.Required[index] = claim.ID
		schema.Properties[claim.ID] = property
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return completion.Spec[responseBody]{}, err
	}
	codec := tools.JSONCodec[responseBody]{
		ToJSON: func(value responseBody) ([]byte, error) {
			return json.Marshal(value)
		},
		FromJSON: decodeResponse,
	}
	return completion.Spec[responseBody]{
		Name:        submitJudgmentsID,
		Description: "Submit a semantic judgment for each claim described by a required property.",
		Schema:      rawjson.Message(encoded),
		Codec:       codec,
	}, nil
}

// decodeResponse checks member uniqueness before JSON decoding can overwrite a
// decision. All expressible shape rules belong to the advertised schema.
func decodeResponse(data []byte) (responseBody, error) {
	if err := validateMemberNames(data); err != nil {
		return nil, err
	}
	var response responseBody
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response, nil
}
