// Shared references belong to one grading request, never to the Judge instance
// or to the output being graded. These tests use the validated model client.
package judge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestJudgePreservesReferenceAcrossCorrection(t *testing.T) {
	const output = "  Candidate only: no measurements.\n"
	reference := strings.Repeat("Shared observation ☃ <quoted> \"12.5\"\n", 1000)
	provider := &recordingClient{responses: []*model.Response{
		toolResponse(`{"claim":"wrong shape"}`),
		toolResponse(`{"claim":{"label":"not_addressed","rationale":"The candidate omits the observation."}}`),
	}}
	claims := []aieval.Claim{{ID: "claim", Text: "The candidate reports the measurement."}}
	judgments, err := newTestJudge(t, provider).Judge(t.Context(), output, claims, reference)
	require.NoError(t, err)
	require.Len(t, judgments, 1)
	assert.Equal(t, aieval.NotAddressed, judgments[0].Label)
	require.Len(t, provider.requests, 2)
	for _, request := range provider.requests {
		body := sharedReferenceRequestBody(t, request)
		assert.Equal(t, output, body.Output)
		assert.Equal(t, reference, body.Reference)
		assert.NotContains(t, string(request.Tools[0].Input.Contract().Schema), "Shared observation")
		assert.Contains(t, systemText(request), "Do not credit the output with information that appears only in the reference")
		assert.Equal(t, 1024, request.MaxTokens)
		assert.Equal(t, model.ModelClassHighReasoning, request.ModelClass)
	}
}

// This test proves request preservation, not semantic accuracy. The scripted
// conditional judgment is deliberately wrong; the judge must return it unchanged.
func TestJudgePreservesConditionalClaimsAcrossCorrection(t *testing.T) {
	const output = "Delivery is tomorrow."
	const reference = "The price is $12."
	claims := []aieval.Claim{
		{ID: "required_price", Text: "The answer reports the price."},
		{ID: "accurate_prices", Text: "Any price quoted agrees with the reference. Quoting no prices satisfies this constraint."},
	}
	provider := &recordingClient{responses: []*model.Response{
		toolResponse(`{"required_price":"wrong shape"}`),
		toolResponse(`{"required_price":{"label":"not_addressed","rationale":"The answer omits the price."},"accurate_prices":{"label":"not_addressed","rationale":"Scripted decision preserved unchanged."}}`),
	}}

	judgments, err := newTestJudge(t, provider).Judge(t.Context(), output, claims, reference)

	require.NoError(t, err)
	assert.Equal(t, []aieval.Judgment{
		{ClaimID: "required_price", Label: aieval.NotAddressed, Rationale: "The answer omits the price."},
		{ClaimID: "accurate_prices", Label: aieval.NotAddressed, Rationale: "Scripted decision preserved unchanged."},
	}, judgments)
	require.Len(t, provider.requests, 2)
	for _, request := range provider.requests {
		body := sharedReferenceRequestBody(t, request)
		assert.Equal(t, output, body.Output)
		assert.Equal(t, reference, body.Reference)
		prompt := systemText(request)
		assert.Contains(t, prompt, "For a constraint on content the output may omit, absence of that content satisfies the constraint; use entailed if the rest of the claim is satisfied, not not_addressed.")
		assert.Contains(t, prompt, "This does not satisfy a requirement to include content, supply missing evidence for content actually included, or resolve an unknown condition about the world.")
		assert.Contains(t, prompt, "Do not credit the output with information that appears only in the reference.")
		require.Len(t, request.Tools, 1)
		var schema judgmentSchema
		require.NoError(t, json.Unmarshal(request.Tools[0].Input.Contract().Schema, &schema))
		assert.Equal(t, []string{"required_price", "accurate_prices"}, schema.Required)
		require.Len(t, schema.Properties, len(claims))
		for _, claim := range claims {
			var property judgmentSchema
			require.NoError(t, json.Unmarshal(schema.Properties[claim.ID], &property))
			assert.Equal(t, claim.Text, property.Description)
		}
	}
	assert.Equal(t, provider.requests[0].Tools[0].Input.Contract().Schema, provider.requests[1].Tools[0].Input.Contract().Schema)
}

func TestJudgeKeepsConcurrentReferencesSeparate(t *testing.T) {
	const count = 12
	provider := &recordingClient{}
	for range count {
		provider.responses = append(provider.responses, toolResponse(`{"claim":{"label":"entailed","rationale":"Synthetic decision."}}`))
	}
	judge := newTestJudge(t, provider)
	t.Run("requests", func(t *testing.T) {
		for index := range count {
			t.Run(fmt.Sprint(index), func(t *testing.T) {
				t.Parallel()
				_, err := judge.Judge(t.Context(), fmt.Sprintf("candidate-%d", index), []aieval.Claim{{ID: "claim", Text: "The candidate is supported."}}, fmt.Sprintf("reference-%d", index))
				require.NoError(t, err)
			})
		}
	})
	require.Len(t, provider.requests, count)
	seen := make(map[string]bool, count)
	for _, request := range provider.requests {
		body := sharedReferenceRequestBody(t, request)
		assert.Equal(t, strings.Replace(body.Output, "candidate-", "reference-", 1), body.Reference)
		assert.False(t, seen[body.Output])
		seen[body.Output] = true
	}
}

// sharedReferenceRequestBody locates the unchanged user input even when the
// runtime inserts a system correction reminder before it.
func sharedReferenceRequestBody(t *testing.T, request *model.Request) requestBody {
	t.Helper()
	var users []string
	for _, message := range request.Messages {
		if message.Role == model.ConversationRoleUser {
			require.Len(t, message.Parts, 1)
			users = append(users, message.Parts[0].(model.TextPart).Text)
		}
	}
	require.Len(t, users, 1)
	var body requestBody
	require.NoError(t, json.Unmarshal([]byte(users[0]), &body))
	return body
}
