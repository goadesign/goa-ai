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
			{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{"title": "x"}}},
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
		types = append(types, ch.Kind())
	}
	assert.Equal(t, []string{
		model.ChunkTypeText,
		model.ChunkTypeToolCall,
		model.ChunkTypeUsage,
		model.ChunkTypeStop,
	}, types)
	assert.Equal(t, "feed/find_duplicates", string(chunks[1].(model.ToolCallChunk).ToolCall.Name))
	assert.Equal(t, 7, chunks[2].(model.UsageChunk).Usage.InputTokens)
	assert.Equal(t, string(genai.FinishReasonStop), chunks[3].(model.StopChunk).Reason)
	response := s.Response()
	require.NotNil(t, response)
	assert.Equal(t, 7, response.Usage.InputTokens)
}

func TestStreamRejectsProviderEndBeforeFinishReason(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{{
		Candidates: []*genai.Candidate{{Content: &genai.Content{
			Parts: []*genai.Part{{Text: "partial"}},
		}}},
	}}}
	client, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	stream, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "go"}},
		}},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, stream.Close()) }()

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.TextChunk{}, chunk)
	_, err = stream.Recv()

	require.EqualError(t, err, "vertex: stream ended before candidate finish reason")
}

func TestStreamToolCallThoughtSignature(t *testing.T) {
	sig := []byte("gemini-3-tool-call-signature")
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{
				FunctionCall:     &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{"title": "x"}},
				ThoughtSignature: sig,
			},
		}}}}},
		{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}},
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
	require.Len(t, chunks, 2)
	call := chunks[0].(model.ToolCallChunk).ToolCall
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), call.ThoughtSignature)
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
	first := chunks[0].(model.ThinkingChunk)
	require.Len(t, first.Message.Parts, 1)
	draft1, ok := first.Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.False(t, draft1.Final)
	assert.Equal(t, "thinking hard ", draft1.Text)

	// Second draft thinking chunk carries the second part's incremental
	// text (not the accumulated text).
	second := chunks[1].(model.ThinkingChunk)
	draft2, ok := second.Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.False(t, draft2.Final)
	assert.Equal(t, "about it", draft2.Text)

	// The thought signature yields a third, final thinking chunk carrying
	// the FULL accumulated text and the base64-encoded signature.
	third := chunks[2].(model.ThinkingChunk)
	require.Len(t, third.Message.Parts, 1)
	final, ok := third.Message.Parts[0].(model.ThinkingPart)
	require.True(t, ok)
	assert.True(t, final.Final)
	assert.Equal(t, "thinking hard about it", final.Text)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sig), final.Signature)

	require.IsType(t, model.StopChunk{}, chunks[3])
	require.NotNil(t, s.Response())
}

// TestStreamThinkingSignatureCanonicalReplay verifies that the stream's
// canonical response preserves both reasoning and tool-call signatures and
// that encodeContents returns their original provider bytes on a later turn.
func TestStreamThinkingSignatureCanonicalReplay(t *testing.T) {
	thinkSig := []byte("sig-bytes-for-round-trip")
	toolSig := []byte("tool-call-sig-bytes-for-round-trip")
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "part one "},
			{Thought: true, Text: "part two", ThoughtSignature: thinkSig},
		}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{
				FunctionCall:     &genai.FunctionCall{ID: "call-1", Name: "feed_find_duplicates", Args: map[string]any{"title": "x"}},
				ThoughtSignature: toolSig,
			},
		}}}}},
		{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}},
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
	var finalThinking *model.ThinkingPart
	var toolCall *model.ToolCall
	for _, ch := range chunks {
		switch actual := ch.(type) {
		case model.ThinkingChunk:
			for _, p := range actual.Message.Parts {
				if tp, ok := p.(model.ThinkingPart); ok && tp.Final {
					tp := tp
					finalThinking = &tp
				}
			}
		case model.ToolCallChunk:
			call := actual.ToolCall
			toolCall = &call
		}
	}
	require.NotNil(t, finalThinking, "expected a final ThinkingPart in the stream")
	require.NotEmpty(t, finalThinking.Text)
	require.NotEmpty(t, finalThinking.Signature)
	require.NotNil(t, toolCall, "expected a tool call in the stream")
	require.Equal(t, "feed/find_duplicates", string(toolCall.Name))
	require.NotEmpty(t, toolCall.ThoughtSignature)

	response := s.Response()
	require.NotNil(t, response)
	require.Len(t, response.Content, 1)
	rebuilt := []*model.Message{&response.Content[0]}
	require.Len(t, rebuilt, 1)
	require.Len(t, rebuilt[0].Parts, 2)

	canonToProv, _, err := buildToolNameMaps([]*model.ToolDefinition{def})
	require.NoError(t, err)
	_, contents, err := encodeContents(rebuilt, canonToProv)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 2)

	thoughtPart := contents[0].Parts[0]
	assert.True(t, thoughtPart.Thought)
	assert.Equal(t, finalThinking.Text, thoughtPart.Text)
	assert.Equal(t, thinkSig, thoughtPart.ThoughtSignature, "thinking signature must round-trip byte-for-byte")

	toolPart := contents[0].Parts[1]
	require.NotNil(t, toolPart.FunctionCall)
	assert.Equal(t, "feed_find_duplicates", toolPart.FunctionCall.Name)
	assert.Equal(t, toolSig, toolPart.ThoughtSignature, "tool-call thought signature must round-trip byte-for-byte")
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
		if _, ok := ch.(model.UsageChunk); ok {
			usageChunks = append(usageChunks, ch)
		}
	}
	require.Len(t, usageChunks, 1, "exactly one usage chunk must be emitted even though two responses carried UsageMetadata")
	usageChunk := usageChunks[0].(model.UsageChunk)
	assert.Equal(t, 7, usageChunk.Usage.InputTokens)
	assert.Equal(t, 12, usageChunk.Usage.TotalTokens)

	response := s.Response()
	require.NotNil(t, response)
	assert.Equal(t, 7, response.Usage.InputTokens)
	assert.Equal(t, 12, response.Usage.TotalTokens)
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

func TestStreamStructuredOutputEmitsCompletionDeltaAndFinalCompletion(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Text: `{"assistant_text":`},
		}}}}},
		{
			Candidates: []*genai.Candidate{{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: `"created a draft"}`}}},
				FinishReason: genai.FinishReasonStop,
			}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 5, TotalTokenCount: 8},
		},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	chunks := drain(t, s)
	types := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		types = append(types, ch.Kind())
	}
	assert.Equal(t, []string{
		model.ChunkTypeCompletionDelta,
		model.ChunkTypeCompletionDelta,
		model.ChunkTypeCompletion,
		model.ChunkTypeUsage,
		model.ChunkTypeStop,
	}, types)
	response := s.Response()
	require.NotNil(t, response)
	require.Len(t, response.Content, 1)
	require.Equal(t, []model.Part{
		model.TextPart{Text: `{"assistant_text":"created a draft"}`},
	}, response.Content[0].Parts)

	first := chunks[0].(model.CompletionDeltaChunk).Delta
	assert.Equal(t, "draft_from_transcript", first.Name)
	assert.Equal(t, `{"assistant_text":`, first.Delta)

	second := chunks[1].(model.CompletionDeltaChunk).Delta
	assert.Equal(t, `"created a draft"}`, second.Delta)

	completion := chunks[2].(model.CompletionChunk).Completion
	assert.Equal(t, "draft_from_transcript", completion.Name)
	assert.JSONEq(t, `{"assistant_text":"created a draft"}`, string(completion.Payload))
}

func TestStreamStructuredOutputRejectsInvalidFinalJSON(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Text: `{"assistant_text":`},
		}}}}},
		{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	err = drainToError(t, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestStreamStructuredOutputRejectsEmptyAccumulation(t *testing.T) {
	stub := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}},
	}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	s, err := cl.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "go"}}}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, s.Close()) }()

	err = drainToError(t, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "structured completion payload is empty")
}

// drainToError drains a streamer until it returns a non-EOF error, failing
// the test if the stream instead reaches a clean end.
func drainToError(t *testing.T, s model.Streamer) error {
	t.Helper()
	for {
		_, err := s.Recv()
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			t.Fatal("expected stream error, got clean EOF")
		}
		return err
	}
}
