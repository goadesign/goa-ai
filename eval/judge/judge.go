// Package judge implements strict semantic claim classification over Goa-AI's
// provider-neutral model client. It makes one structured, tool-free request per
// batch and never retries, repairs, or substitutes model output.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// Judge classifies semantic assertions using provider-enforced structured output.
	Judge struct {
		client model.Client
	}

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

// New creates a strict semantic judge backed by client.
func New(client model.Client) *Judge {
	return &Judge{client: client}
}

// Judge classifies all assertions in one high-reasoning structured completion.
func (j *Judge) Judge(ctx context.Context, assertions []aieval.Assertion) ([]aieval.Judgment, error) {
	if len(assertions) == 0 {
		return nil, errors.New("judge requires at least one assertion")
	}
	payload, err := json.Marshal(requestBody{Assertions: assertions})
	if err != nil {
		return nil, fmt.Errorf("encode judge assertions: %w", err)
	}
	response, err := j.client.Complete(ctx, &model.Request{
		ModelClass: model.ModelClassHighReasoning,
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
		StructuredOutput: &model.StructuredOutput{
			Name:        "eval_judgments",
			Description: "One semantic label and rationale for every supplied claim ID.",
			Schema:      []byte(responseSchema),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("judge assertions: %w", err)
	}
	body, err := responseJSON(response)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeResponse(body)
	if err != nil {
		return nil, err
	}
	judgments := make([]aieval.Judgment, len(decoded.Judgments))
	for i, item := range decoded.Judgments {
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

// responseJSON accepts the one canonical assistant JSON content part and
// rejects tool calls, multiple messages, or mixed content at the model boundary.
func responseJSON(response *model.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("judge response is nil")
	}
	if err := model.ValidateResponse(response); err != nil {
		return nil, fmt.Errorf("invalid judge model response: %w", err)
	}
	if len(response.Content) != 1 {
		return nil, fmt.Errorf("judge expected exactly one assistant message, got %d", len(response.Content))
	}
	var body string
	for _, part := range response.Content[0].Parts {
		switch actual := part.(type) {
		case model.TextPart:
			if body != "" {
				return nil, errors.New("judge response contains multiple content parts")
			}
			body = actual.Text
		case model.ThinkingPart, model.CacheCheckpointPart:
			continue
		default:
			return nil, fmt.Errorf("judge response contains unsupported part %T", part)
		}
	}
	if body == "" {
		return nil, errors.New("judge response contains no JSON")
	}
	return []byte(body), nil
}

// decodeResponse strictly decodes the provider JSON and rejects unknown fields
// or any trailing value instead of repairing model output.
func decodeResponse(data []byte) (responseBody, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response responseBody
	if err := decoder.Decode(&response); err != nil {
		return responseBody{}, fmt.Errorf("decode judge response: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return responseBody{}, errors.New("decode judge response: trailing JSON value")
		}
		return responseBody{}, fmt.Errorf("decode judge response: %w", err)
	}
	return response, nil
}
