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

func TestCompleteStructuredOutputWithoutTools(t *testing.T) {
	stub := &stubGenerativeClient{resp: textResp(`{}`)}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		StructuredOutput: &model.StructuredOutput{Name: "out", Schema: []byte(`{"type":"object"}`)},
	})
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
	_, err = cl.Complete(context.Background(), &model.Request{
		Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
		Tools:            []*model.ToolDefinition{def},
		StructuredOutput: &model.StructuredOutput{Name: "out", Schema: []byte(`{"type":"object"}`)},
	})
	assert.ErrorIs(t, err, model.ErrStructuredOutputUnsupported)
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
