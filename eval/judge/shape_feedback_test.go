// These tests use the real Judge, model validation, and Anthropic SDK with
// synthetic HTTP responses. They verify that structural correction names the
// rejected field without changing judgments or repeating factual claim text.
package judge_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/eval/judge"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestJudgeAnthropicHTTPStructuralCorrectionAcceptsSemanticDecisions(t *testing.T) {
	for _, label := range []aieval.Label{aieval.Entailed, aieval.Contradicted, aieval.NotAddressed, aieval.Indeterminate} {
		t.Run(string(label), func(t *testing.T) {
			t.Parallel()
			const rationale = "  Preserve this semantic judgment exactly.\n"
			accepted := fmt.Sprintf(`{"complete":{"label":%q,"rationale":%q}}`, label, rationale)
			rejected := strings.TrimSuffix(accepted, "}") + `,"requests":[]}`
			transport := &judgmentHTTPTransport{
				t:                  t,
				argumentsByRequest: []string{rejected, accepted},
				stopReason:         "tool_use",
				outputTokens:       300,
			}
			client, recorder := newJudgmentHTTPClient(t, transport)
			evaluator, err := judge.New(client, 32768)
			require.NoError(t, err)
			const candidate = "The candidate is unchanged."
			const claim = "The answer addresses this claim."

			judgments, err := evaluator.Judge(t.Context(), candidate, []aieval.Claim{{ID: "complete", Text: claim}}, "")

			require.NoError(t, err)
			assert.Equal(t, []aieval.Judgment{{ClaimID: "complete", Label: label, Rationale: rationale}}, judgments)
			require.Len(t, transport.requests, 2)
			require.Len(t, recorder.responses, 2)
			for index, request := range transport.requests {
				assertJudgmentHTTPRequest(t, request, 32768, "invoke-with-response-stream", candidate, claim)
				require.NotNil(t, recorder.responses[index])
				require.Len(t, recorder.responses[index].ToolCalls(), 1)
				assert.Equal(t, transport.argumentsByRequest[index], string(recorder.responses[index].ToolCalls()[0].Payload))
			}
			correction := judgmentCorrection(t, transport.requests[1])
			assert.Contains(t, correction, `Field "$payload" contains an undeclared field.`)
			assert.Contains(t, correction, "Each property contains a judgment object with label and rationale")
			assert.NotContains(t, correction, rationale)
			assert.NotContains(t, correction, "Example illustrates structure")
		})
	}
}

func TestJudgeAnthropicHTTPStructuralCorrectionRetainsFailuresAndEvidence(t *testing.T) {
	const extra = `{"complete":{"label":"entailed","rationale":"Synthetic evidence."},"requests":[]}`
	const stringified = `{"complete":"{\"label\":\"entailed\",\"rationale\":\"Synthetic evidence.\"}","requests":[]}`
	transport := &judgmentHTTPTransport{
		t:                  t,
		argumentsByRequest: []string{extra, stringified, extra, extra},
		stopReason:         "tool_use",
		outputTokens:       300,
	}
	client, recorder := newJudgmentHTTPClient(t, transport)
	evaluator, err := judge.New(client, 32768)
	require.NoError(t, err)
	const candidate = "Original candidate text."
	claim := "Original claim text: " + strings.Repeat("Synthetic factual context. ", 4000)

	judgments, err := evaluator.Judge(t.Context(), candidate, []aieval.Claim{{ID: "complete", Text: claim}}, "")

	require.ErrorContains(t, err, "recovery_cap")
	require.ErrorContains(t, err, "requests")
	require.ErrorContains(t, err, "got string, want object")
	assert.Equal(t, 4, strings.Count(err.Error(), "observed chat high-reasoning:"))
	assert.Nil(t, judgments)
	require.Len(t, transport.requests, 4)
	require.Len(t, recorder.responses, 4)
	for index, request := range transport.requests {
		assertJudgmentHTTPRequest(t, request, 32768, "invoke-with-response-stream", candidate, claim)
		var schema struct {
			Description string          `json:"description"`
			Example     json.RawMessage `json:"example"`
		}
		require.NoError(t, json.Unmarshal(request.body.Tools[0].InputSchema, &schema))
		assert.Contains(t, schema.Description, "Return only the required claim properties")
		assert.Empty(t, schema.Example)
		require.NotNil(t, recorder.responses[index])
		require.Len(t, recorder.responses[index].ToolCalls(), 1)
		assert.Equal(t, transport.argumentsByRequest[index], string(recorder.responses[index].ToolCalls()[0].Payload))
		if index == 0 {
			continue
		}
		correction := judgmentCorrection(t, request)
		assert.LessOrEqual(t, len(correction), 4096)
		assert.NotContains(t, correction, "Synthetic factual context.")
		assert.NotContains(t, correction, "Synthetic evidence.")
		assert.Contains(t, correction, `Field "$payload" contains an undeclared field.`)
		if index == 2 {
			assert.Contains(t, correction, `Field "complete" must contain a JSON object.`)
		}
	}
}

func TestJudgeAnthropicHTTPMultipleClaimCorrectionsRetainEvidence(t *testing.T) {
	const missing = `{"label":"entailed","rationale":"Synthetic evidence."}`
	const stringsOnly = `{"coverage":"stringified judgment","timing":"stringified judgment","value":"stringified judgment","rationale":"Synthetic evidence."}`
	const mixed = `{"coverage":"stringified judgment","rationale":"Synthetic evidence."}`
	transport := &judgmentHTTPTransport{
		t:                  t,
		argumentsByRequest: []string{missing, stringsOnly, mixed, mixed},
		stopReason:         "tool_use",
		outputTokens:       300,
	}
	client, recorder := newJudgmentHTTPClient(t, transport)
	evaluator, err := judge.New(client, 32768)
	require.NoError(t, err)
	claims := []aieval.Claim{
		{ID: "coverage", Text: "The answer covers the requested subject."},
		{ID: "timing", Text: "The answer gives the requested time."},
		{ID: "value", Text: "The answer gives the requested value."},
	}
	judgments, err := evaluator.Judge(t.Context(), "Original candidate.", claims, "")
	require.ErrorContains(t, err, "recovery_cap")
	require.ErrorContains(t, err, "rationale")
	require.ErrorContains(t, err, "got string, want object")
	assert.Nil(t, judgments)
	assert.Equal(t, 4, strings.Count(err.Error(), "observed chat high-reasoning:"))
	require.Len(t, transport.requests, 4)
	require.Len(t, recorder.responses, 4)
	for index, request := range transport.requests {
		assert.Equal(t, "/model/"+judgmentTransportModel+"/invoke-with-response-stream", request.path)
		assert.Equal(t, 32768, request.body.MaxTokens)
		assert.Equal(t, "tool", request.body.ToolChoice.Type)
		require.Len(t, request.body.Tools, 1)
		assert.Equal(t, request.body.Tools[0].Name, request.body.ToolChoice.Name)
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(request.body.Tools[0].InputSchema, &schema))
		assert.Equal(t, []string{"coverage", "timing", "value"}, schema.Required)
		for _, claim := range claims {
			assert.Equal(t, claim.Text, schema.Properties[claim.ID].Description)
		}
		require.NotNil(t, recorder.responses[index])
		require.Len(t, recorder.responses[index].ToolCalls(), 1)
		assert.Equal(t, transport.argumentsByRequest[index], string(recorder.responses[index].ToolCalls()[0].Payload))
		if index == 0 {
			continue
		}
		feedback := judgmentCorrection(t, request)
		assert.Contains(t, feedback, `Field "$payload" contains an undeclared field.`)
		assert.NotContains(t, feedback, "Synthetic evidence.")
		assert.NotContains(t, feedback, "stringified judgment")
		assert.NotContains(t, feedback, "Example illustrates structure")
		assert.NotContains(t, feedback, "Other schema errors")
		assert.LessOrEqual(t, len(feedback), 4096)
		for _, claim := range claims {
			constraint := "is required."
			if index == 2 || (index == 3 && claim.ID == "coverage") {
				constraint = "must contain a JSON object."
			}
			assert.Contains(t, feedback, fmt.Sprintf("Field %q %s", claim.ID, constraint))
			assert.NotContains(t, feedback, claim.Text)
		}
	}
}

func TestJudgeAnthropicHTTPClaimNamesAreNotReserved(t *testing.T) {
	for _, id := range []string{"requests", "label", "rationale", "a.b", "a/b~c", "quote\"slash\\", "☃", " "} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			const rationale = "  Preserve the answer and whitespace.\n"
			arguments := fmt.Sprintf(`{%q:{"label":"contradicted","rationale":%q}}`, id, rationale)
			stringified := fmt.Sprintf(`{%q:%q}`, id, `{"label":"contradicted","rationale":"Synthetic."}`)
			transport := &judgmentHTTPTransport{
				t:                  t,
				argumentsByRequest: []string{stringified, arguments},
				stopReason:         "tool_use",
				outputTokens:       300,
			}
			client, _ := newJudgmentHTTPClient(t, transport)
			evaluator, err := judge.New(client, 32768)
			require.NoError(t, err)
			claims := []aieval.Claim{{ID: id, Text: "Exact claim for " + id}}

			judgments, err := evaluator.Judge(t.Context(), "Candidate.", claims, "")

			require.NoError(t, err)
			assert.Equal(t, []aieval.Judgment{{ClaimID: id, Label: aieval.Contradicted, Rationale: rationale}}, judgments)
			require.Len(t, transport.requests, 2)
			path := tools.FieldPathString([]tools.FieldPathSegment{tools.FixedField(id)})
			assert.Contains(t, judgmentCorrection(t, transport.requests[1]), fmt.Sprintf("Field %q must contain a JSON object.", path))
			var schema struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(transport.requests[0].body.Tools[0].InputSchema, &schema))
			assert.Equal(t, []string{id}, schema.Required)
			require.Len(t, schema.Properties, 1)
			assert.Contains(t, schema.Properties, id)
		})
	}
}

// judgmentCorrection reads the last system block from the actual SDK request.
// Earlier blocks keep the original grading prompt; the last carries runtime
// feedback from the preceding rejected arguments.
func judgmentCorrection(t *testing.T, request judgmentHTTPRequest) string {
	t.Helper()
	require.NotEmpty(t, request.body.System)
	last := request.body.System[len(request.body.System)-1]
	assert.Equal(t, "text", last.Type)
	assert.Contains(t, last.Text, "previous tool call was rejected")
	return last.Text
}
