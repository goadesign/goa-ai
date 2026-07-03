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
		// Thought text split across two parts; the signature only arrives
		// on the last one. The final ThinkingPart must carry the FULL
		// accumulated text ("thinking hard about it"), not just the text
		// from the part that happened to carry the signature.
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "thinking hard "},
			{Thought: true, Text: "about it", ThoughtSignature: sig},
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
	require.Len(t, chunks, 4)

	// First draft thinking chunk carries the first part's text and is not final.
	assert.Equal(t, model.ChunkTypeThinking, chunks[0].Type)
	assert.Equal(t, "thinking hard ", chunks[0].Thinking)
	require.NotNil(t, chunks[0].Message)
	require.Len(t, chunks[0].Message.Parts, 1)
	draft1, ok := chunks[0].Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.False(t, draft1.Final)
	assert.Equal(t, "thinking hard ", draft1.Text)

	// Second draft thinking chunk carries the second part's incremental
	// text (not the accumulated text).
	assert.Equal(t, model.ChunkTypeThinking, chunks[1].Type)
	assert.Equal(t, "about it", chunks[1].Thinking)
	draft2, ok := chunks[1].Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.False(t, draft2.Final)
	assert.Equal(t, "about it", draft2.Text)

	// The thought signature yields a third, final thinking chunk carrying
	// the FULL accumulated text and the base64-encoded signature.
	assert.Equal(t, model.ChunkTypeThinking, chunks[2].Type)
	require.NotNil(t, chunks[2].Message)
	require.Len(t, chunks[2].Message.Parts, 1)
	final, ok := chunks[2].Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.True(t, final.Final)
	assert.Equal(t, "thinking hard about it", final.Text)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), final.Signature)

	assert.Equal(t, model.ChunkTypeStop, chunks[3].Type)
}

// TestStreamThinkingSignatureLedgerSeamRoundTrip verifies the seam between
// the streamer and two downstream consumers of the final ThinkingPart:
// (1) the transcript ledger only replays thinking parts with BOTH Text and
// Signature set (see runtime/agent/transcript/ledger.go BuildMessages), and
// (2) encodeContents (features/model/vertex/messages.go) must be able to
// round-trip that exact part back into a genai Part with the original
// signature bytes when the assistant message is replayed on a later turn.
func TestStreamThinkingSignatureLedgerSeamRoundTrip(t *testing.T) {
	sig := []byte("sig-bytes-for-round-trip")
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "part one "},
			{Thought: true, Text: "part two", ThoughtSignature: sig},
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
	var final *model.ThinkingPart
	for _, ch := range chunks {
		if ch.Type != model.ChunkTypeThinking || ch.Message == nil {
			continue
		}
		for _, p := range ch.Message.Parts {
			if tp, ok := p.(model.ThinkingPart); ok && tp.Final {
				tp := tp
				final = &tp
			}
		}
	}
	require.NotNil(t, final, "expected a final ThinkingPart in the stream")
	require.NotEmpty(t, final.Text)
	require.NotEmpty(t, final.Signature)

	// The ledger only replays this part if both Text and Signature survive
	// (the bug this fix closes was a final part with Signature but no
	// Text, which the ledger's `v.Text != "" && v.Signature != ""` filter
	// silently drops).
	require.True(t, final.Text != "" && final.Signature != "")

	// Place the final ThinkingPart in an assistant message and run it
	// through the same translation the runtime uses to build the next
	// request's contents.
	msg := &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			*final,
			model.TextPart{Text: "answer"},
		},
	}
	_, contents, err := encodeContents([]*model.Message{msg}, nil)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 2)

	gp := contents[0].Parts[0]
	assert.True(t, gp.Thought)
	assert.Equal(t, final.Text, gp.Text)
	assert.Equal(t, sig, gp.ThoughtSignature, "signature must round-trip byte-for-byte")
}

func TestStreamUsageEmittedOnceWithLatestValues(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{
			Candidates:    []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "part one "}}}}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 1, TotalTokenCount: 4},
		},
		{
			Candidates:    []*genai.Candidate{{FinishReason: genai.FinishReasonStop}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 5, TotalTokenCount: 12},
		},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	chunks := drain(t, s)
	var usageChunks []model.Chunk
	for _, ch := range chunks {
		if ch.Type == model.ChunkTypeUsage {
			usageChunks = append(usageChunks, ch)
		}
	}
	require.Len(t, usageChunks, 1, "exactly one usage chunk must be emitted even though two responses carried UsageMetadata")
	assert.Equal(t, 7, usageChunks[0].UsageDelta.InputTokens)
	assert.Equal(t, 12, usageChunks[0].UsageDelta.TotalTokens)

	usage, ok := s.Metadata()["usage"].(model.TokenUsage)
	require.True(t, ok)
	assert.Equal(t, 7, usage.InputTokens)
	assert.Equal(t, 12, usage.TotalTokens)
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
