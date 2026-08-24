package vertex

import (
	"context"
	"iter"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubGenerativeClient struct {
	lastModel       string
	lastContents    []*genai.Content
	lastConfig      *genai.GenerateContentConfig
	resp            *genai.GenerateContentResponse
	err             error
	streamChunks    []*genai.GenerateContentResponse
	streamErr       error
	countResp       *genai.CountTokensResponse
	lastCountConfig *genai.CountTokensConfig
}

func (s *stubGenerativeClient) GenerateContent(_ context.Context, m string, c []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	s.lastModel, s.lastContents, s.lastConfig = m, c, cfg
	return s.resp, s.err
}

func (s *stubGenerativeClient) GenerateContentStream(_ context.Context, m string, c []*genai.Content, cfg *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	s.lastModel, s.lastContents, s.lastConfig = m, c, cfg
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, ch := range s.streamChunks {
			if !yield(ch, nil) {
				return
			}
		}
		if s.streamErr != nil {
			yield(nil, s.streamErr)
		}
	}
}

func (s *stubGenerativeClient) CountTokens(_ context.Context, m string, c []*genai.Content, cfg *genai.CountTokensConfig) (*genai.CountTokensResponse, error) {
	s.lastModel, s.lastContents, s.lastCountConfig = m, c, cfg
	return s.countResp, s.err
}

func textResp(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}},
		}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 3,
			TotalTokenCount:      13,
		},
	}
}

func TestNewValidates(t *testing.T) {
	_, err := New(nil, Options{DefaultModel: "gemini-2.5-pro"})
	require.Error(t, err)
	_, err = New(&stubGenerativeClient{}, Options{})
	require.Error(t, err)
}

func TestCompleteTextOnly(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("hello")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro", MaxTokens: 256, Temperature: 0.2})
	require.NoError(t, err)
	resp, err := cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	assert.Equal(t, "gemini-2.5-pro", stub.lastModel)
	require.NotNil(t, stub.lastConfig)
	assert.NotNil(t, stub.lastConfig.SystemInstruction)
	assert.EqualValues(t, 256, stub.lastConfig.MaxOutputTokens)
	require.NotNil(t, stub.lastConfig.Temperature)
	assert.InDelta(t, 0.2, *stub.lastConfig.Temperature, 1e-6)
	assert.Equal(t, string(genai.FinishReasonStop), resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
}

func TestCompleteSystemOnlyTranscriptRejected(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("x")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
		},
	})
	require.ErrorContains(t, err, "no user or assistant messages")
}

func TestGemini3RejectsUnsupportedThinking(t *testing.T) {
	tests := []struct {
		name    string
		request *model.Request
		wantErr string
	}{
		{
			name: "numeric thinking budget",
			request: &model.Request{
				Thinking: &model.ThinkingOptions{Enable: true, BudgetTokens: 1024},
			},
			wantErr: "does not accept a token budget",
		},
		{
			name: "thinking disabled",
			request: &model.Request{
				Thinking: &model.ThinkingOptions{},
			},
			wantErr: "does not support disabling thinking",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubGenerativeClient{}
			client, err := New(stub, Options{
				DefaultModel: "projects/p/locations/us/publishers/google/models/gemini-3-pro-preview",
			})
			require.NoError(t, err)
			tt.request.Messages = []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			}}

			_, err = client.Complete(t.Context(), tt.request)

			require.ErrorContains(t, err, tt.wantErr)
			assert.Empty(t, stub.lastModel)
		})
	}
}

func TestGemini3ForwardsValidTemperature(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("ok")}
	client, err := New(stub, Options{
		DefaultModel: "projects/p/locations/us/publishers/google/models/gemini-3-pro-preview",
	})
	require.NoError(t, err)

	_, err = client.Complete(t.Context(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
		Temperature: 0.2,
	})

	require.NoError(t, err)
	require.NotNil(t, stub.lastConfig.Temperature)
	assert.InDelta(t, 0.2, *stub.lastConfig.Temperature, 1e-6)
}

func TestGemini3DoesNotInheritNumericThinkingBudgetDefault(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("ok")}
	client, err := New(stub, Options{
		DefaultModel:   "projects/p/locations/us/publishers/google/models/gemini-2.5-pro",
		HighModel:      "projects/p/locations/us/publishers/google/models/gemini-3-pro-preview",
		ThinkingBudget: 2048,
	})
	require.NoError(t, err)

	_, err = client.Complete(t.Context(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
		Thinking: &model.ThinkingOptions{Enable: true},
	})

	require.NoError(t, err)
	require.NotNil(t, stub.lastConfig.ThinkingConfig)
	assert.True(t, stub.lastConfig.ThinkingConfig.IncludeThoughts)
	assert.Zero(t, stub.lastConfig.ThinkingConfig.ThinkingBudget)
}

func TestCompleteStructuredOutputWithoutTools(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp(`{}`)}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	request := &model.Request{
		Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		StructuredOutput: &model.StructuredOutput{Name: "out", Schema: []byte(`{"type":"object"}`)},
	}
	require.NoError(t, model.SetCompletionValidator(
		request,
		func(*model.Response, *model.Completion) error { return nil },
	))
	_, err = cl.Complete(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, stub.lastConfig)
	assert.Equal(t, "application/json", stub.lastConfig.ResponseMIMEType)
	assert.NotNil(t, stub.lastConfig.ResponseJsonSchema)
}

func TestCompleteStructuredOutputWithToolsRejected(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("x")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	def := toolDef(t, "a", `{"type":"object"}`)
	request := &model.Request{
		Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		Tools:            []*model.ToolDefinition{def},
		StructuredOutput: &model.StructuredOutput{Name: "out", Schema: []byte(`{"type":"object"}`)},
	}
	require.NoError(t, model.SetCompletionValidator(
		request,
		func(*model.Response, *model.Completion) error { return nil },
	))
	_, err = cl.Complete(context.Background(), request)
	assert.ErrorContains(t, err, "structured output cannot include tools")
}

func TestCompleteThinkingConfig(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp("x")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro", ThinkingBudget: 2048})
	require.NoError(t, err)
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		Thinking: &model.ThinkingOptions{Enable: true},
	})
	require.NoError(t, err)
	require.NotNil(t, stub.lastConfig.ThinkingConfig)
}
