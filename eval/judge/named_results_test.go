// Named-result tests exercise the real validated model client and correction
// flow. Shape validation cannot decide whether a rationale is semantically true;
// it must preserve that decision while enforcing exact claim association.
package judge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestJudgeRejectsNamedContractViolations(t *testing.T) {
	for _, test := range []struct {
		name, payload, cause string
	}{
		{"same count wrong name", `{"other":{"label":"entailed","rationale":"Evidence."}}`, "claim"},
		{"missing name", `{}`, "claim"},
		{"extra name", `{"claim":{"label":"entailed","rationale":"Evidence."},"other":{"label":"entailed","rationale":"Extra."}}`, "other"},
		{"old array", `{"judgments":[{"label":"entailed","rationale":"Evidence."}]}`, "judgments"},
		{"root array", `[{"label":"entailed","rationale":"Evidence."}]`, "object"},
		{"missing label", `{"claim":{"rationale":"Evidence."}}`, "label"},
		{"invalid label", `{"claim":{"label":"supported","rationale":"Evidence."}}`, "value must be one of"},
		{"empty rationale", `{"claim":{"label":"entailed","rationale":""}}`, "minLength"},
		{"duplicate claim", `{"claim":{"label":"contradicted","rationale":"First."},"claim":{"label":"entailed","rationale":"Last."}}`, "duplicate JSON member"},
		{"escaped duplicate claim", `{"claim":{"label":"contradicted","rationale":"First."},"cl\u0061im":{"label":"entailed","rationale":"Last."}}`, "duplicate JSON member"},
		{"duplicate label", `{"claim":{"label":"contradicted","label":"entailed","rationale":"Evidence."}}`, "duplicate JSON member"},
		{"escaped duplicate label", `{"claim":{"label":"contradicted","\u006cabel":"entailed","rationale":"Evidence."}}`, "duplicate JSON member"},
		{"duplicate rationale", `{"claim":{"label":"entailed","rationale":"First.","rationale":"Last."}}`, "duplicate JSON member"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingClient{}
			for range 4 {
				provider.responses = append(provider.responses, toolResponse(test.payload))
			}
			judgments, err := newTestJudge(t, provider).Judge(t.Context(), "Evidence.", []aieval.Claim{{ID: "claim", Text: "Evidence."}}, "")
			require.ErrorContains(t, err, "recovery_cap")
			require.ErrorContains(t, err, test.cause)
			assert.Nil(t, judgments)
			var rejected *model.OutputValidationError
			require.ErrorAs(t, err, &rejected)
			retained, retainErr := rejected.RejectedResponse()
			require.NoError(t, retainErr)
			require.NotNil(t, retained)
			require.Len(t, retained.ToolCalls(), 1)
			assert.Equal(t, test.payload, string(retained.ToolCalls()[0].Payload), "returned error retains exact rejected bytes")
			require.Len(t, provider.requests, 4)
			for _, response := range provider.responses {
				assert.Equal(t, test.payload, string(response.ToolCalls()[0].Payload), "never rewrite rejected model evidence")
			}
		})
	}
}

func TestJudgeCorrectsDuplicateMemberWithoutExtraBudget(t *testing.T) {
	provider := &recordingClient{responses: []*model.Response{
		toolResponse(`{"claim":{"label":"entailed","label":"contradicted","rationale":"Conflicting decisions."}}`),
		toolResponse(`{"claim":{"label":"contradicted","rationale":"The output says the opposite."}}`),
	}}
	judgments, err := newTestJudge(t, provider).Judge(t.Context(), "Stopped.", []aieval.Claim{{ID: "claim", Text: "Running."}}, "")
	require.NoError(t, err)
	assert.Equal(t, []aieval.Judgment{{ClaimID: "claim", Label: aieval.Contradicted, Rationale: "The output says the opposite."}}, judgments)
	require.Len(t, provider.requests, 2)
	for _, request := range provider.requests {
		assert.Equal(t, 1024, request.MaxTokens)
	}
	assert.Contains(t, systemText(provider.requests[1]), "previous tool call was rejected")
}

func TestJudgePreservesJSONPropertyNamesAndSemanticDecisions(t *testing.T) {
	ids := []string{"severity", "subject", "$ref", "properties", " ", "a/b~c", "quote\"slash\\", "☃", "\x00", "line\nname"}
	claims := make([]aieval.Claim, len(ids))
	response := make(responseBody, len(ids))
	for index, id := range ids {
		claims[index] = aieval.Claim{ID: id, Text: "Exact claim " + id}
		response[id] = modelJudgment{Label: aieval.Entailed, Rationale: "  Wrong semantic rationale for " + id + "\n"}
	}
	data, err := json.Marshal(response)
	require.NoError(t, err)
	provider := &recordingClient{responses: []*model.Response{toolResponse(string(data))}}
	judgments, err := newTestJudge(t, provider).Judge(t.Context(), "Unrelated evidence.", claims, "")
	require.NoError(t, err)
	require.Len(t, judgments, len(claims))
	for index, judgment := range judgments {
		assert.Equal(t, claims[index].ID, judgment.ClaimID)
		assert.Equal(t, response[judgment.ClaimID].Label, judgment.Label)
		assert.Equal(t, response[judgment.ClaimID].Rationale, judgment.Rationale, "do not trim or repair a semantic error")
	}
	assert.Len(t, provider.requests, 1)
}

func TestJudgeRejectsInvalidUTF8ClaimNamesBeforeInference(t *testing.T) {
	provider := &recordingClient{}
	_, err := newTestJudge(t, provider).Judge(t.Context(), "Evidence.", []aieval.Claim{{ID: "bad\xffname", Text: "Evidence."}}, "")
	require.ErrorContains(t, err, "not valid UTF-8")
	assert.Empty(t, provider.requests)
}

func TestJudgeKeepsExistingSchemaSizeLimit(t *testing.T) {
	const limit = 1 << 20
	claims := []aieval.Claim{{ID: "claim", Text: "x"}}
	spec, err := judgmentToolSpec(claims)
	require.NoError(t, err)
	overhead := len(spec.Schema) - 1
	for _, size := range []int{limit - 1, limit, limit + 1} {
		claims[0].Text = strings.Repeat("x", size-overhead)
		provider := &recordingClient{responses: []*model.Response{toolResponse(`{"claim":{"label":"entailed","rationale":"Evidence."}}`)}}
		_, err := newTestJudge(t, provider).Judge(t.Context(), "Evidence.", claims, "")
		if size > limit {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "schema")
			assert.Empty(t, provider.requests, "oversized schema must fail before inference, not truncate or split")
		} else {
			require.NoError(t, err)
			require.Len(t, provider.requests, 1)
			assert.Len(t, provider.requests[0].Tools[0].Input.Contract().Schema, size)
		}
	}
}

func TestRawMemberUniquenessAtEveryObjectPath(t *testing.T) {
	for _, payload := range []string{
		`{"a":{"b":{"x":1,"x":2}}}`,
		`{"a":[{"x":1,"\u0078":2}]}`,
		`[{"a":[{"b":{"x":1,"x":2}}]}]`,
		`{"a":{"\u0000":1,"\u0000":2}}`,
	} {
		err := validateMemberNames([]byte(payload))
		var validation *tools.ValidationError
		require.ErrorAs(t, err, &validation)
		require.ErrorContains(t, err, "duplicate JSON member")
		require.Len(t, validation.Issues(), 1)
		assert.Equal(t, "invalid_format", validation.Issues()[0].Constraint)
		assert.Equal(t, "unique JSON object member names", validation.Issues()[0].Format)
	}
	for _, payload := range []string{
		`{"a":{"label":"one"},"b":{"label":"two"}}`,
		`[{"label":1},{"label":2}]`,
		`{"x":1,"X":2," x":3,"x ":4}`,
	} {
		require.NoError(t, validateMemberNames([]byte(payload)))
	}
}
