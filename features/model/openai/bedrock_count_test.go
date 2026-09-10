// Bedrock counting estimates the prepared SDK request without inference. These
// tests cover model routing, opaque replay content, rejection, and caller data
// preservation; they do not establish native token accuracy.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestBedrockTokenEstimateUsesPreparedRequest(t *testing.T) {
	var calls atomic.Int32
	raw, err := NewBedrockProvider(t.Context(), "us-west-2",
		credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"),
		Options{DefaultModel: bedrockTestModel, HighModel: "high-model", SmallModel: "small-model", ThinkingEffort: "medium"},
		option.WithHTTPClient(&http.Client{Transport: bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("counting must not call HTTP")
		})}))
	require.NoError(t, err)
	counter, ok := raw.(*bedrockProvider)
	require.True(t, ok)
	client, err := model.NewClient(raw)
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		model     string
		class     model.ModelClass
		wantModel string
		wantClass model.ModelClass
	}{
		{"implicit default", "", "", bedrockTestModel, model.ModelClassDefault},
		{"default", "", model.ModelClassDefault, bedrockTestModel, model.ModelClassDefault},
		{"high", "", model.ModelClassHighReasoning, "high-model", model.ModelClassHighReasoning},
		{"small", "", model.ModelClassSmall, "small-model", model.ModelClassSmall},
		{"explicit", "explicit-model", model.ModelClassHighReasoning, "explicit-model", model.ModelClassHighReasoning},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := bedrockTestRequest()
			request.Model, request.ModelClass = test.model, test.class
			before, err := json.Marshal(request)
			require.NoError(t, err)
			prepared, err := counter.prepareRequest(request)
			require.NoError(t, err)
			body, err := json.Marshal(prepared.request)
			require.NoError(t, err)
			count, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.Equal(t, test.wantModel, count.Model)
			assert.Equal(t, test.wantClass, count.ModelClass)
			assert.Equal(t, (len(body)+2)/3+500, count.InputTokens)
			assert.False(t, count.Exact)
			after, err := json.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestBedrockTokenEstimateIncludesOpaqueReasoningAndTools(t *testing.T) {
	var calls atomic.Int32
	client := newBedrockTestClient(t, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("counting must not call HTTP")
	}))
	for _, summary := range []string{"", "A short reasoning summary"} {
		t.Run("summary="+summary, func(t *testing.T) {
			request := bedrockTestRequest()
			base, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			encrypted := strings.Repeat("opaque+/=", 1000)
			reasoning, err := json.Marshal(map[string]any{
				"type": "reasoning", "id": "rs_count", "status": "completed",
				"summary":           []map[string]string{{"type": "summary_text", "text": summary}},
				"encrypted_content": encrypted,
			})
			require.NoError(t, err)
			part := model.ThinkingPart{Text: summary, Final: true}
			if summary == "" {
				part.Redacted = []byte(encrypted)
			}
			request.Messages = append(request.Messages,
				&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{part}, Meta: map[string]any{openAIReasoningItemsMetaKey: []string{string(reasoning)}}},
				&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Continue."}}})
			before, err := json.Marshal(request)
			require.NoError(t, err)
			withReasoning, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, withReasoning.InputTokens-base.InputTokens, len(encrypted)/3)
			assert.False(t, withReasoning.Exact)
			after, err := json.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			request.Tools, request.ToolChoice = nil, nil
			withoutTools, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.Less(t, withoutTools.InputTokens, withReasoning.InputTokens)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestBedrockTokenEstimateRejectsInvalidAndCanceledRequests(t *testing.T) {
	raw, err := newProvider(Options{DefaultModel: bedrockTestModel, transport: &mockTransport{}}, true)
	require.NoError(t, err)
	counter := &bedrockProvider{provider: raw}
	_, err = counter.CountTokens(t.Context(), nil)
	require.Error(t, err)
	_, err = counter.CountTokens(t.Context(), &model.Request{MaxTokens: -1})
	require.Error(t, err)
	_, err = counter.CountTokens(t.Context(), &model.Request{})
	require.ErrorContains(t, err, "messages are required")
	request := bedrockTestRequest()
	request.ModelClass = model.ModelClassHighReasoning
	_, err = counter.CountTokens(t.Context(), request)
	require.ErrorContains(t, err, "HighModel is not configured")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = counter.CountTokens(ctx, bedrockTestRequest())
	require.ErrorIs(t, err, context.Canceled)
	direct, err := NewProvider(Options{DefaultModel: bedrockTestModel, transport: &mockTransport{}})
	require.NoError(t, err)
	_, supportsCount := direct.(model.TokenCounter)
	assert.False(t, supportsCount)
	client, err := model.NewClient(direct)
	require.NoError(t, err)
	_, err = client.CountTokens(t.Context(), bedrockTestRequest())
	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
}
