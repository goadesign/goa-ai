// Package judge implements strict semantic claim classification over Goa-AI's
// provider-neutral model client. The judge contract is a framework-owned typed
// completion: a canonical raw JSON schema plus a strict codec executed by the
// shared completion runtime. The judge never retries, repairs, or substitutes
// model output.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Judge classifies semantic assertions using provider-enforced structured output.
	Judge struct {
		client     model.Client
		modelClass model.ModelClass
	}

	// Option customizes a Judge.
	Option func(*Judge)

	// requestBody is the canonical user-message representation of an assertion batch.
	requestBody struct {
		Assertions []aieval.Assertion `json:"assertions"`
	}

	// responseBody is the only structured response shape accepted from the model.
	responseBody struct {
		Judgments []judgment `json:"judgments"`
	}

	// judgment mirrors the provider JSON contract before conversion to runtime types.
	judgment struct {
		ClaimID   string       `json:"claim_id"`
		Label     aieval.Label `json:"label"`
		Rationale string       `json:"rationale"`
	}
)

const (
	// maxTokensPerJudgment budgets output tokens for one judgment: a label,
	// a concise rationale, and its share of JSON envelope overhead. Provider
	// boundaries such as Anthropic and AURA's inference engine require an
	// explicit positive cap; truncation surfaces as a strict decode error.
	maxTokensPerJudgment = 256

	judgePrompt = `Classify each assertion independently using only its output and claim.
Return entailed when the output establishes the claim, contradicted when it establishes the claim is false, not_addressed when it does neither, and indeterminate only when ambiguity prevents classification.
Return exactly one judgment for every claim_id. Do not add, remove, merge, or rename claim IDs.`
	responseSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["judgments"],
  "properties": {
    "judgments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["claim_id", "label", "rationale"],
        "properties": {
          "claim_id": {"type": "string", "minLength": 1},
          "label": {
            "type": "string",
            "enum": ["entailed", "contradicted", "not_addressed", "indeterminate"]
          },
          "rationale": {"type": "string", "minLength": 1}
        }
      }
    }
  }
}`
)

// spec is the framework-owned typed completion contract for judge output. The
// schema is the canonical raw JSON contract and the codec owns strict boundary
// decoding, exactly like generated completion specs.
var spec = completion.Spec[responseBody]{
	Name:        "eval_judgments",
	Description: "One semantic label and rationale for every supplied claim ID.",
	Result: tools.TypeSpec{
		Name:   "responseBody",
		Schema: tools.RawJSON(responseSchema),
	},
	Codec: tools.JSONCodec[responseBody]{
		ToJSON:   func(value responseBody) ([]byte, error) { return json.Marshal(value) },
		FromJSON: decodeResponse,
	},
}

// New creates a strict semantic judge backed by client. By default the judge
// requests the high-reasoning model class; deployments whose high-reasoning
// provider cannot serve structured completions select a capable class with
// WithModelClass.
func New(client model.Client, opts ...Option) *Judge {
	judge := &Judge{client: client, modelClass: model.ModelClassHighReasoning}
	for _, opt := range opts {
		opt(judge)
	}
	return judge
}

// WithModelClass selects the model class judge requests use.
func WithModelClass(class model.ModelClass) Option {
	return func(j *Judge) { j.modelClass = class }
}

// Judge classifies all assertions in one structured completion using the
// configured model class.
func (j *Judge) Judge(ctx context.Context, assertions []aieval.Assertion) ([]aieval.Judgment, error) {
	if len(assertions) == 0 {
		return nil, errors.New("judge requires at least one assertion")
	}
	payload, err := json.Marshal(requestBody{Assertions: assertions})
	if err != nil {
		return nil, fmt.Errorf("encode judge assertions: %w", err)
	}
	response, err := completion.Complete(ctx, j.client, &model.Request{
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
		MaxTokens:   maxTokensPerJudgment * len(assertions),
	}, spec)
	if err != nil {
		return nil, fmt.Errorf("judge assertions: %w", err)
	}
	judgments := make([]aieval.Judgment, len(response.Value.Judgments))
	for i, item := range response.Value.Judgments {
		judgments[i] = aieval.Judgment{
			ClaimID:   item.ClaimID,
			Label:     item.Label,
			Rationale: item.Rationale,
		}
	}
	claims := make([]aieval.Claim, len(assertions))
	for i, assertion := range assertions {
		claims[i] = aieval.Claim{ID: assertion.ClaimID, Text: assertion.Claim}
	}
	if err := aieval.ValidateJudgments(claims, judgments); err != nil {
		return nil, fmt.Errorf("invalid judge response: %w", err)
	}
	return judgments, nil
}

// decodeResponse strictly decodes the provider JSON and rejects unknown fields
// or any trailing value instead of repairing model output.
func decodeResponse(data []byte) (responseBody, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response responseBody
	if err := decoder.Decode(&response); err != nil {
		return responseBody{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return responseBody{}, errors.New("trailing JSON value")
		}
		return responseBody{}, err
	}
	return response, nil
}
