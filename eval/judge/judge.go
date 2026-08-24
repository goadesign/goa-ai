// Package judge asks a model to classify evaluation claims. It sends a typed
// JSON request and accepts one matching JSON response. Invalid output ends the
// request without asking the model again.
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
	"goa.design/goa-ai/runtime/agent/rawjson"
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

	// requestBody is the JSON sent to the model for one group of assertions.
	requestBody struct {
		Assertions []aieval.Assertion `json:"assertions"`
	}

	// responseBody is the only JSON shape accepted from the model.
	responseBody struct {
		Judgments []aieval.Judgment `json:"judgments"`
	}
)

const (
	// maxTokensPerJudgment allows enough output for one label, one short reason,
	// and its JSON fields. A cut-off response fails JSON decoding.
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

// baseSpec defines the JSON response accepted from the model. Each request adds
// its exact claim IDs before calling the model.
var baseSpec = completion.Spec[responseBody]{
	Name:        "eval_judgments",
	Description: "One semantic label and rationale for every supplied claim ID.",
	Schema:      rawjson.Message(responseSchema),
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
	claims := make([]aieval.Claim, len(assertions))
	for i, assertion := range assertions {
		claims[i] = aieval.Claim{ID: assertion.ClaimID, Text: assertion.Claim}
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
	}, specForClaims(claims))
	if err != nil {
		return nil, fmt.Errorf("judge assertions: %w", err)
	}
	return response.Value.Judgments, nil
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

// specForClaims requires one judgment for each claim ID in this request.
func specForClaims(claims []aieval.Claim) completion.Spec[responseBody] {
	spec := baseSpec
	spec.Codec.FromJSON = decodeResponseForClaims(claims)
	return spec
}

// decodeResponseForClaims checks both the JSON shape and the exact claim IDs.
func decodeResponseForClaims(claims []aieval.Claim) func([]byte) (responseBody, error) {
	return func(data []byte) (responseBody, error) {
		response, err := decodeResponse(data)
		if err != nil {
			return responseBody{}, err
		}
		if err := aieval.ValidateJudgments(claims, response.Judgments); err != nil {
			return responseBody{}, fmt.Errorf("invalid judge response: %w", err)
		}
		return response, nil
	}
}
