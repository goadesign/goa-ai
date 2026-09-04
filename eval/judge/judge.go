// Package judge classifies evaluation claims with a forced typed tool. Invalid
// model arguments use the shared runtime correction flow before callers receive
// a result.
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
	"goa.design/goa-ai/runtime/agent/tooloutput"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Judge classifies semantic claims with one forced private tool.
	Judge struct {
		client     model.Client
		modelClass model.ModelClass
	}

	// Option customizes a Judge.
	Option func(*Judge)

	// requestBody is the compact input sent to the model. Claim identity stays
	// outside the model request because list position already correlates results.
	requestBody struct {
		Output string   `json:"output"`
		Claims []string `json:"claims"`
	}

	// responseBody is the private completion tool payload.
	responseBody struct {
		Judgments []modelJudgment `json:"judgments"`
	}

	// modelJudgment contains only the semantic decision the model must make.
	modelJudgment struct {
		Label     aieval.Label `json:"label"`
		Rationale string       `json:"rationale"`
	}
)

const (
	maxTokensPerJudgment = 256

	submitJudgmentsID completion.Ident = "eval.submit_judgments"

	judgePrompt = `Classify each claim independently using only the supplied output.
Return entailed when the output establishes the claim, contradicted when it establishes the claim is false, not_addressed when it does neither, and indeterminate only when ambiguity prevents classification.
Call submit_judgments exactly once. Return one judgment for each claim in the same order. Each judgment must contain a label and a concise rationale.`
)

// New creates a semantic judge backed by client. The judge uses the
// high-reasoning model class unless WithModelClass selects another class.
func New(client model.Client, opts ...Option) *Judge {
	judge := &Judge{client: client, modelClass: model.ModelClassHighReasoning}
	for _, opt := range opts {
		opt(judge)
	}
	return judge
}

// WithModelClass selects the model class used by the private judge agent.
func WithModelClass(class model.ModelClass) Option {
	return func(j *Judge) { j.modelClass = class }
}

// Judge classifies claims against one model-authored output. It restores each
// claim ID from list position after the private tool returns the exact number
// of requested judgments.
func (j *Judge) Judge(ctx context.Context, output string, claims []aieval.Claim) ([]aieval.Judgment, error) {
	if len(claims) == 0 {
		return nil, errors.New("judge requires at least one claim")
	}
	if err := aieval.ValidateClaims(claims); err != nil {
		return nil, fmt.Errorf("judge claims: %w", err)
	}
	claimTexts := make([]string, len(claims))
	for index, claim := range claims {
		claimTexts[index] = claim.Text
	}
	payload, err := json.Marshal(requestBody{Output: output, Claims: claimTexts})
	if err != nil {
		return nil, fmt.Errorf("encode judge request: %w", err)
	}
	response, err := j.run(ctx, payload, len(claims))
	if err != nil {
		return nil, fmt.Errorf("judge claims: %w", err)
	}
	judgments := make([]aieval.Judgment, len(response.Judgments))
	for index, judgment := range response.Judgments {
		judgments[index] = aieval.Judgment{
			// #nosec G602 -- decodeResponse requires exactly one judgment per claim.
			ClaimID:   claims[index].ID,
			Label:     judgment.Label,
			Rationale: judgment.Rationale,
		}
	}
	if err := aieval.ValidateJudgments(claims, judgments); err != nil {
		return nil, fmt.Errorf("invalid judge response: %w", err)
	}
	return judgments, nil
}

func (j *Judge) run(ctx context.Context, payload []byte, claimCount int) (responseBody, error) {
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
		MaxTokens:   maxTokensPerJudgment * claimCount,
	}, judgmentToolSpec(claimCount))
}

func judgmentToolSpec(claimCount int) completion.Spec[responseBody] {
	schema := rawjson.Message(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["judgments"],
  "properties": {
    "judgments": {
      "type": "array",
      "minItems": %d,
      "maxItems": %d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["label", "rationale"],
        "properties": {
          "label": {
            "type": "string",
            "enum": ["entailed", "contradicted", "not_addressed", "indeterminate"]
          },
          "rationale": {"type": "string", "minLength": 1}
        }
      }
    }
  }
}`, claimCount, claimCount))
	codec := tools.JSONCodec[responseBody]{
		ToJSON: func(value responseBody) ([]byte, error) {
			return json.Marshal(value)
		},
		FromJSON: func(data []byte) (responseBody, error) {
			return decodeResponse(data, claimCount)
		},
	}
	return completion.Spec[responseBody]{
		Name:        submitJudgmentsID,
		Description: "Submit one ordered semantic judgment for every supplied claim.",
		Schema:      schema,
		Codec:       codec,
	}
}

// decodeResponse strictly decodes one completion tool payload. The exact
// length check lets the runtime ask the model to correct missing or extra
// judgments before the tool executes.
func decodeResponse(data []byte, claimCount int) (responseBody, error) {
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
	if len(response.Judgments) != claimCount {
		return responseBody{}, fmt.Errorf("got %d judgments for %d claims", len(response.Judgments), claimCount)
	}
	for index, judgment := range response.Judgments {
		switch judgment.Label {
		case aieval.Entailed, aieval.Contradicted, aieval.NotAddressed, aieval.Indeterminate:
		default:
			return responseBody{}, fmt.Errorf("judgment %d has invalid label %q", index, judgment.Label)
		}
		if judgment.Rationale == "" {
			return responseBody{}, fmt.Errorf("judgment %d requires a rationale", index)
		}
	}
	return response, nil
}
