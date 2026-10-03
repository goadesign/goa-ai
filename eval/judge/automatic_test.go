// These tests compare the two judge constructors using synthetic provider
// responses. Automatic selection changes the request choice, while claims,
// evidence, corrections and returned semantic decisions keep the same contract.
package judge

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestJudgeAutomaticPreservesRequestAndCorrectedDecisions(t *testing.T) {
	claims := []aieval.Claim{
		{ID: "quoted\"☃", Text: "The candidate reports the price."},
		{ID: "other", Text: "Any quoted price agrees with the reference."},
	}
	const output = "  Delivery is tomorrow.\n"
	const reference = "The price is $12; this is reference evidence only."
	const accepted = `{"other":{"label":"indeterminate","rationale":"  Preserve this scripted semantic decision.\n"},"quoted\"☃":{"label":"contradicted","rationale":"Scripted contradiction."}}`
	var firstRequest *model.Request
	for _, constructor := range []struct {
		name string
		new  func(model.Client, int, ...Option) (*Judge, error)
		mode model.ToolChoiceMode
	}{
		{"forced", New, model.ToolChoiceModeTool},
		{"automatic", NewAutomatic, model.ToolChoiceModeAuto},
	} {
		t.Run(constructor.name, func(t *testing.T) {
			provider := &recordingClient{responses: []*model.Response{
				toolResponse(`{"other":{"label":"entailed","label":"contradicted","rationale":"Duplicate decision."},"quoted\"☃":{"label":"not_addressed","rationale":"Missing."}}`),
				toolResponse(accepted),
			}}
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			judge, err := constructor.new(client, 1024, WithModelClass(model.ModelClassSmall))
			require.NoError(t, err)

			judgments, err := judge.Judge(t.Context(), output, claims, reference)

			require.NoError(t, err)
			assert.Equal(t, []aieval.Judgment{
				{ClaimID: claims[0].ID, Label: aieval.Contradicted, Rationale: "Scripted contradiction."},
				{ClaimID: claims[1].ID, Label: aieval.Indeterminate, Rationale: "  Preserve this scripted semantic decision.\n"},
			}, judgments)
			require.Len(t, provider.requests, 2, "a schema-valid semantic decision must not trigger another call")
			for _, request := range provider.requests {
				require.NotNil(t, request.ToolChoice)
				assert.Equal(t, constructor.mode, request.ToolChoice.Mode)
				if constructor.mode == model.ToolChoiceModeAuto {
					assert.Empty(t, request.ToolChoice.Name)
				}
				assert.Nil(t, request.StructuredOutput)
				assert.Equal(t, model.ModelClassSmall, request.ModelClass)
				assert.Equal(t, 1024, request.MaxTokens)
				assert.Equal(t, requestBody{Output: output, Reference: reference}, sharedReferenceRequestBody(t, request))
				assert.Equal(t, provider.requests[0].Tools[0].Input.Contract().Schema, request.Tools[0].Input.Contract().Schema)
			}
			assert.Contains(t, systemText(provider.requests[1]), "previous tool call was rejected")
			if firstRequest == nil {
				firstRequest = provider.requests[0]
			} else {
				assert.Equal(t, firstRequest.Messages, provider.requests[0].Messages)
				assert.Equal(t, firstRequest.Tools[0].Input.Contract().Schema, provider.requests[0].Tools[0].Input.Contract().Schema)
				assert.Equal(t, firstRequest.Tools[0].Description, provider.requests[0].Tools[0].Description)
			}
		})
	}
}

func TestNewAutomaticRequiresPositiveOutputBudget(t *testing.T) {
	for _, budget := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			provider := &recordingClient{}
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			judge, err := NewAutomatic(client, budget)
			if budget <= 0 {
				require.ErrorContains(t, err, "max output tokens must be positive")
				assert.Nil(t, judge)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, judge)
			}
			assert.Empty(t, provider.requests)
		})
	}
}

func TestJudgeAutomaticKeepsConcurrentReferencesSeparate(t *testing.T) {
	provider := &recordingClient{}
	for range 4 {
		provider.responses = append(provider.responses, toolResponse(`{"claim":{"label":"not_addressed","rationale":"Scripted decision."}}`))
	}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	judge, err := NewAutomatic(client, 1024)
	require.NoError(t, err)
	t.Run("requests", func(t *testing.T) {
		for index := range 4 {
			t.Run(fmt.Sprint(index), func(t *testing.T) {
				t.Parallel()
				_, err := judge.Judge(t.Context(), fmt.Sprintf("candidate-%d", index),
					[]aieval.Claim{{ID: "claim", Text: "The candidate reports the price."}},
					fmt.Sprintf("reference-%d", index))
				require.NoError(t, err)
			})
		}
	})
	require.Len(t, provider.requests, 4)
	seen := make(map[requestBody]bool)
	for _, request := range provider.requests {
		body := sharedReferenceRequestBody(t, request)
		assert.False(t, seen[body])
		seen[body] = true
	}
	for index := range 4 {
		assert.True(t, seen[requestBody{Output: fmt.Sprintf("candidate-%d", index), Reference: fmt.Sprintf("reference-%d", index)}])
	}
}
