// These tests send synthetic claims through the real judge and validated model
// client. Provider fixtures account for output work explicitly; their success
// proves request handling, not how many tokens a real model needs to judge text.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aieval "goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

type budgetProvider func(context.Context, *model.Request) (*model.Response, error)

func TestNewRequiresPositiveOutputBudget(t *testing.T) {
	for _, budget := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			provider := &recordingClient{}
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			judge, err := New(client, budget)
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

func TestJudgeConcurrentBatchesKeepOneResponseBudget(t *testing.T) {
	const budget = 2048
	provider := budgetProvider(func(_ context.Context, request *model.Request) (*model.Response, error) {
		assert.Equal(t, budget, request.MaxTokens)
		var body requestBody
		if err := json.Unmarshal([]byte(request.Messages[1].Parts[0].(model.TextPart).Text), &body); err != nil {
			return nil, err
		}
		assert.Equal(t, "Full candidate\n<quoted> ☃", body.Output)
		var schema judgmentSchema
		if err := json.Unmarshal(request.Tools[0].Input.Contract().Schema, &schema); err != nil {
			return nil, err
		}
		response := make(responseBody, len(schema.Properties))
		for name, data := range schema.Properties {
			var property judgmentSchema
			if err := json.Unmarshal(data, &property); err != nil {
				return nil, err
			}
			response[name] = modelJudgment{Label: aieval.Entailed, Rationale: property.Description}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return toolResponse(string(encoded)), nil
	})
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	judge, err := New(client, budget)
	require.NoError(t, err)
	var calls sync.WaitGroup
	for _, count := range []int{1, 4, 100} {
		calls.Go(func() {
			claims := make([]aieval.Claim, count)
			for index := range claims {
				claims[index] = aieval.Claim{ID: fmt.Sprintf("batch-%d-claim-%d", count, index), Text: fmt.Sprintf("Full evidence %d/%d\n☃", count, index)}
			}
			judgments, err := judge.Judge(t.Context(), "Full candidate\n<quoted> ☃", claims, "")
			if err != nil {
				t.Errorf("judge batch of %d claims: %v", count, err)
				return
			}
			if !assert.Len(t, judgments, count) {
				return
			}
			for index, judgment := range judgments {
				assert.Equal(t, claims[index].ID, judgment.ClaimID)
				assert.Equal(t, claims[index].Text, judgment.Rationale)
			}
		})
	}
	calls.Wait()
}

func TestJudgePreservesProviderCeiling(t *testing.T) {
	const ceiling = 512
	limitError := errors.New("provider maximum output tokens is 512")
	for _, budget := range []int{ceiling - 1, ceiling, ceiling + 1} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			calls := 0
			provider := budgetProvider(func(_ context.Context, request *model.Request) (*model.Response, error) {
				calls++
				assert.Equal(t, budget, request.MaxTokens)
				if request.MaxTokens > ceiling {
					return nil, limitError
				}
				return toolResponse(`{"claim":{"label":"entailed","rationale":"Supported."}}`), nil
			})
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			judge, err := New(client, budget)
			require.NoError(t, err)
			judgments, err := judge.Judge(t.Context(), "Supported.", []aieval.Claim{{ID: "claim", Text: "Supported."}}, "")
			if budget > ceiling {
				require.ErrorIs(t, err, limitError)
				assert.Nil(t, judgments)
			} else {
				require.NoError(t, err)
				assert.Len(t, judgments, 1)
			}
			assert.Equal(t, 1, calls)
		})
	}
}

func TestJudgeAcceptsShortAndLargeAccountedResponses(t *testing.T) {
	for _, outputTokens := range []int{32, 300} {
		t.Run(fmt.Sprint(outputTokens), func(t *testing.T) {
			budget := outputTokens + 1
			provider := budgetProvider(func(_ context.Context, request *model.Request) (*model.Response, error) {
				assert.Equal(t, budget, request.MaxTokens)
				response := toolResponse(fmt.Sprintf(`{"evidence":{"label":"entailed","rationale":%q}}`, strings.Repeat("Evidence. ", outputTokens)))
				response.Usage = model.TokenUsage{OutputTokens: outputTokens, TotalTokens: outputTokens}
				return response, nil
			})
			client, err := model.NewClient(provider)
			require.NoError(t, err)
			judge, err := New(client, budget)
			require.NoError(t, err)
			judgments, err := judge.Judge(t.Context(), "Evidence.", []aieval.Claim{{ID: "evidence", Text: "Evidence."}}, "")
			require.NoError(t, err)
			require.Len(t, judgments, 1)
			assert.Equal(t, strings.Repeat("Evidence. ", outputTokens), judgments[0].Rationale)
		})
	}
}

func TestJudgeLimitedCorrectionsKeepBudgetAndDiagnostics(t *testing.T) {
	const budget = 513
	provider := &recordingClient{}
	for range 4 {
		response := toolResponse(`{}`)
		response.StopReason = "max_tokens"
		response.OutputLimited = true
		response.Usage = model.TokenUsage{InputTokens: 17, OutputTokens: budget, TotalTokens: 17 + budget}
		provider.responses = append(provider.responses, response)
	}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	judge, err := New(client, budget)
	require.NoError(t, err)
	judgments, err := judge.Judge(t.Context(), "Candidate.", []aieval.Claim{{ID: "claim", Text: "Evidence."}}, "")
	require.Error(t, err)
	assert.Nil(t, judgments)
	assert.Contains(t, err.Error(), "recovery_cap")
	assert.Contains(t, err.Error(), "claim")
	assert.Contains(t, err.Error(), "max_tokens")
	assert.Contains(t, err.Error(), "513")
	require.Len(t, provider.requests, 4)
	for _, request := range provider.requests {
		assert.Equal(t, budget, request.MaxTokens)
		var texts []string
		for _, message := range request.Messages {
			for _, part := range message.Parts {
				if text, ok := part.(model.TextPart); ok {
					texts = append(texts, text.Text)
				}
			}
		}
		assert.Contains(t, texts, `{"output":"Candidate."}`)
		assert.Contains(t, string(request.Tools[0].Input.Contract().Schema), `"description":"Evidence."`)
	}
}

func TestJudgeCancellationPreservesCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	provider := budgetProvider(func(ctx context.Context, request *model.Request) (*model.Response, error) {
		calls++
		assert.Equal(t, 1024, request.MaxTokens)
		cancel()
		return nil, ctx.Err()
	})
	judgments, err := newTestJudge(t, provider).Judge(ctx, "Candidate.", []aieval.Claim{{ID: "claim", Text: "Evidence."}}, "")
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, judgments)
	assert.Equal(t, 1, calls)
}

func (p budgetProvider) Complete(ctx context.Context, request *model.Request) (*model.Response, error) {
	return p(ctx, request)
}

func (budgetProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}
