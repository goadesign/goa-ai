package vertex

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"iter"
	"testing"
	"time"

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

func TestStreamThinkingWithSignature(t *testing.T) {
	sig := []byte("sig-bytes")
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "thinking hard", ThoughtSignature: sig},
		}}}}},
		{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	chunks := drain(t, s)
	require.Len(t, chunks, 3)

	// Draft thinking chunk carries the reasoning text and is not final.
	assert.Equal(t, model.ChunkTypeThinking, chunks[0].Type)
	assert.Equal(t, "thinking hard", chunks[0].Thinking)
	require.NotNil(t, chunks[0].Message)
	require.Len(t, chunks[0].Message.Parts, 1)
	draft, ok := chunks[0].Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.False(t, draft.Final)
	assert.Equal(t, "thinking hard", draft.Text)

	// The thought signature yields a second, final thinking chunk carrying
	// the base64-encoded signature.
	assert.Equal(t, model.ChunkTypeThinking, chunks[1].Type)
	require.NotNil(t, chunks[1].Message)
	require.Len(t, chunks[1].Message.Parts, 1)
	final, ok := chunks[1].Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.True(t, final.Final)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), final.Signature)

	assert.Equal(t, model.ChunkTypeStop, chunks[2].Type)
}

// signalingStreamClient wraps stubGenerativeClient to close pumpDone once
// the streaming iterator returns, i.e. once the streamer's pump goroutine
// has finished consuming it. With a no-op Close and an abandoned stream the
// pump blocks in emit forever and pumpDone never closes.
type signalingStreamClient struct {
	*stubGenerativeClient
	pumpDone chan struct{}
}

func (s *signalingStreamClient) GenerateContentStream(ctx context.Context, m string, c []*genai.Content, cfg *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	inner := s.stubGenerativeClient.GenerateContentStream(ctx, m, c, cfg)
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		defer close(s.pumpDone)
		inner(yield)
	}
}

func TestStreamCloseStopsPump(t *testing.T) {
	// More responses than the 32-chunk buffer so the pump goroutine blocks
	// in emit once the buffer fills and the caller stops draining.
	const n = 40
	resps := make([]*genai.GenerateContentResponse, n)
	for i := range resps {
		resps[i] = &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "x"}}},
		}}}
	}
	stub := &signalingStreamClient{
		stubGenerativeClient: &stubGenerativeClient{streamChunks: resps},
		pumpDone:             make(chan struct{}),
	}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
	})
	require.NoError(t, err)

	_, err = s.Recv()
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close()) // Close is idempotent

	// Close must unblock the pump without the caller draining the stream.
	select {
	case <-stub.pumpDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pump goroutine still blocked after Close")
	}

	// Subsequent Recvs terminate: buffered chunks may still surface, then
	// context.Canceled (Done arm) or io.EOF (closed channel) — no hang.
	for {
		_, rerr := s.Recv()
		if rerr == nil {
			continue
		}
		if !errors.Is(rerr, context.Canceled) {
			assert.ErrorIs(t, rerr, io.EOF)
		}
		break
	}
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
