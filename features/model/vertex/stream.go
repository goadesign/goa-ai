package vertex

// Streaming adapter. Invariants: chunks flow through a buffered channel
// (32) drained by Recv; Recv returns io.EOF after a clean end; Close is
// idempotent; Metadata returns a copy including final "usage".

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"sync"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// Stream implements model.Client.
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	seq := c.models.GenerateContentStream(ctx, prep.modelID, prep.contents, prep.config)
	s := &geminiStreamer{
		ctx:    ctx,
		cancel: cancel,
		chunks: make(chan model.Chunk, 32),
		meta:   make(map[string]any),
	}
	go s.run(seq, prep)
	return s, nil
}

type geminiStreamer struct {
	ctx    context.Context
	cancel context.CancelFunc
	chunks chan model.Chunk
	meta   map[string]any

	mu  sync.Mutex
	err error
}

func (s *geminiStreamer) run(seq func(func(*genai.GenerateContentResponse, error) bool), prep *preparedRequest) {
	defer close(s.chunks)
	callIndex := 0
	var stopReason string
	var thoughtText strings.Builder
	var usageSeen bool
	var latestUsage model.TokenUsage
	for resp, err := range seq {
		if err != nil {
			s.setErr(wrapGeminiError("generate_content_stream", err))
			return
		}
		if resp == nil || len(resp.Candidates) == 0 {
			continue
		}
		cand := resp.Candidates[0]
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					callIndex++
					payload, merr := marshalArgs(part.FunctionCall.Args)
					if merr != nil {
						s.setErr(merr)
						return
					}
					s.emit(model.Chunk{Type: model.ChunkTypeToolCall, ToolCall: &model.ToolCall{
						Name:             toolIdent(part.FunctionCall.Name, prep.provToCanon),
						Payload:          payload,
						ID:               toolCallID(part.FunctionCall, callIndex),
						ThoughtSignature: encodeThoughtSignature(part.ThoughtSignature),
					}})
				case part.Thought:
					// Accumulate thought text across Thought parts (mirrors
					// the anthropic streamer's thinkingBuffer). Draft chunks
					// are display-only and only emitted for text-bearing
					// parts, so a signature-only part produces no empty-draft
					// noise. When a signature arrives, the final ThinkingPart
					// carries the FULL accumulated text plus the signature —
					// the transcript ledger (BuildMessages) only replays
					// thinking parts that have both Text and Signature set,
					// so a signature emitted alone would be silently dropped.
					if part.Text != "" {
						thoughtText.WriteString(part.Text)
						draft := model.ThinkingPart{Text: part.Text}
						s.emit(model.Chunk{Type: model.ChunkTypeThinking, Thinking: part.Text,
							Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{draft}}})
					}
					if len(part.ThoughtSignature) > 0 {
						final := model.ThinkingPart{
							Text:      thoughtText.String(),
							Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
							Final:     true,
						}
						s.emit(model.Chunk{Type: model.ChunkTypeThinking,
							Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{final}}})
						thoughtText.Reset()
					}
				case part.Text != "":
					s.emit(model.Chunk{Type: model.ChunkTypeText,
						Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: part.Text}}}})
				}
			}
		}
		if cand.FinishReason != "" {
			stopReason = string(cand.FinishReason)
		}
		if resp.UsageMetadata != nil {
			// Gemini streaming UsageMetadata is cumulative (not a delta), and
			// consumers sum usage chunks across the stream, so only the
			// latest value is emitted, and only once, below.
			latestUsage = translateUsage(resp.UsageMetadata, prep.modelID, prep.modelClass)
			usageSeen = true
			s.mu.Lock()
			s.meta["usage"] = latestUsage
			s.mu.Unlock()
		}
	}
	if usageSeen {
		s.emit(model.Chunk{Type: model.ChunkTypeUsage, UsageDelta: &latestUsage})
	}
	if stopReason != "" {
		s.emit(model.Chunk{Type: model.ChunkTypeStop, StopReason: stopReason})
	}
}

func (s *geminiStreamer) emit(ch model.Chunk) {
	select {
	case s.chunks <- ch:
	case <-s.ctx.Done():
	}
}

func (s *geminiStreamer) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Recv implements model.Streamer.
func (s *geminiStreamer) Recv() (model.Chunk, error) {
	select {
	case ch, ok := <-s.chunks:
		if !ok {
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return model.Chunk{}, err
			}
			return model.Chunk{}, io.EOF
		}
		return ch, nil
	case <-s.ctx.Done():
		return model.Chunk{}, s.ctx.Err()
	}
}

// Close implements model.Streamer. It cancels the pump goroutine's context
// so run stops emitting even when the caller abandons the stream without
// draining it. Context cancellation is idempotent, so is Close.
func (s *geminiStreamer) Close() error {
	s.cancel()
	return nil
}

// Metadata implements model.Streamer.
func (s *geminiStreamer) Metadata() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.meta))
	for k, v := range s.meta {
		out[k] = v
	}
	return out
}
