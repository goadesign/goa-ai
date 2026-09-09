// The actual Anthropic SDK request must carry one shared reference outside the
// judgment schema on both initial and correction requests. No network is used.
package judge_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/judge"
)

func TestJudgeAnthropicHTTPSharedReferenceOnce(t *testing.T) {
	for _, arguments := range []string{
		`{"first":{"label":"entailed","rationale":"First."},"second":{"label":"not_addressed","rationale":"Second."},"third":{"label":"contradicted","rationale":"Third."}}`,
		`{"first":"invalid"}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			transport := &judgmentHTTPTransport{t: t, arguments: arguments, stopReason: "tool_use", outputTokens: 200}
			client, _ := newJudgmentHTTPClient(t, transport)
			evaluator, err := judge.New(client, 32768)
			require.NoError(t, err)
			const candidate = "Candidate only.\n"
			reference := "unique-reference-start\n" + strings.Repeat("Exact observation: 12.5 °C <quoted>\n", 5000)
			claims := []aieval.Claim{{ID: "first", Text: "First claim."}, {ID: "second", Text: "Second claim."}, {ID: "third", Text: "Third claim."}}
			_, err = evaluator.Judge(t.Context(), candidate, claims, reference)
			if strings.Contains(arguments, "invalid") {
				require.ErrorContains(t, err, "recovery_cap")
				require.Len(t, transport.requests, 4)
			} else {
				require.NoError(t, err)
				require.Len(t, transport.requests, 1)
			}
			for _, request := range transport.requests {
				assert.Equal(t, 1, strings.Count(string(request.raw), "unique-reference-start"))
				assert.Equal(t, 32768, request.body.MaxTokens)
				assert.Empty(t, request.body.Thinking)
				require.Len(t, request.body.Tools, 1)
				assert.Equal(t, request.body.Tools[0].Name, request.body.ToolChoice.Name)
				assert.NotContains(t, string(request.body.Tools[0].InputSchema), "unique-reference-start")
				var schema struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						Description string `json:"description"`
					} `json:"properties"`
				}
				require.NoError(t, json.Unmarshal(request.body.Tools[0].InputSchema, &schema))
				assert.Equal(t, []string{"first", "second", "third"}, schema.Required)
				for _, claim := range claims {
					assert.Equal(t, claim.Text, schema.Properties[claim.ID].Description)
				}
				var userMessages []string
				for _, message := range request.body.Messages {
					if message.Role == "user" {
						for _, part := range message.Content {
							userMessages = append(userMessages, part.Text)
						}
					}
				}
				require.Len(t, userMessages, 1)
				var body map[string]string
				require.NoError(t, json.Unmarshal([]byte(userMessages[0]), &body))
				assert.Equal(t, map[string]string{"output": candidate, "reference": reference}, body)
			}
		})
	}
}
