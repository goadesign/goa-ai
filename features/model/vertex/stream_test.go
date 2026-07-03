package vertex

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func drain(t *testing.T, s model.Streamer) []model.Chunk {
	t.Helper()
	var chunks []model.Chunk
	for {
		ch, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return chunks
		}
		require.NoError(t, err)
		chunks = append(chunks, ch)
	}
}

func TestStreamTextToolCallUsageStop(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "part one "}}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "feed_find_duplicates", Args: map[string]any{"title": "x"}}},
		}}}}},
		{
			Candidates:    []*genai.Candidate{{FinishReason: genai.FinishReasonStop}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 2, TotalTokenCount: 9},
		},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	def := toolDef(t, "feed/find_duplicates", `{"type":"object"}`)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
		Tools:    []*model.ToolDefinition{def},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	chunks := drain(t, s)
	types := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		types = append(types, ch.Type)
	}
	assert.Equal(t, []string{
		model.ChunkTypeText, model.ChunkTypeToolCall, model.ChunkTypeUsage, model.ChunkTypeStop,
	}, types)
	assert.Equal(t, "feed/find_duplicates", string(chunks[1].ToolCall.Name))
	assert.Equal(t, 7, chunks[2].UsageDelta.InputTokens)
	assert.Equal(t, string(genai.FinishReasonStop), chunks[3].StopReason)
	assert.NotNil(t, s.Metadata()["usage"])
}

func TestStreamSurfacesIteratorError(t *testing.T) {
	stub := &stubGenerativeClient{streamErr: errors.New("boom")}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
	})
	require.NoError(t, err)
	_, recvErr := s.Recv()
	require.Error(t, recvErr)
	_, ok := model.AsProviderError(recvErr)
	assert.True(t, ok)
}
