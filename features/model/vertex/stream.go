// Streaming adapter. Invariants: chunks flow through a buffered channel
// (32) drained by Recv; Recv returns io.EOF after a clean end; Close is
// idempotent; Metadata returns a copy including final "usage".

package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// geminiStreamer adapts a Gemini GenerateContentStream sequence to the
// model.Streamer interface. A single pump goroutine (run) translates
// provider responses into chunks; Recv drains them.
type geminiStreamer struct {
	// ctx is the pump context. Close cancels it so run stops emitting even
	// when the caller abandons the stream without draining it.
	ctx context.Context

	// cancel cancels ctx; Close calls it (context cancellation is
	// idempotent, so is Close).
	cancel context.CancelFunc

	// chunks carries translated chunks from the pump goroutine to Recv. It
	// is buffered (32) and closed by run when the provider stream ends,
	// which is Recv's signal to surface the terminal error or io.EOF.
	chunks chan model.Chunk

	// meta holds stream metadata (the final cumulative "usage"). Guarded by
	// mu; Metadata returns a copy.
	meta map[string]any

	// mu guards meta and err, the only fields that cross the pump/consumer
	// boundary outside the chunks channel.
	mu sync.Mutex

	// err is the terminal pump error surfaced by Recv after chunks closes.
	err error

	// callIndex numbers function-call parts so toolCallID can synthesize a
	// stable identifier when the provider omits one. Pump-owned: only the
	// run goroutine touches it, so it needs no locking.
	callIndex int

	// thoughtText accumulates thought text across Thought parts until a
	// signature finalizes the block. Pump-owned, no locking.
	thoughtText strings.Builder

	// completionText accumulates structured-output text for the canonical
	// Completion chunk emitted at stream end. Pump-owned, no locking.
	completionText strings.Builder
}

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

// run is the pump goroutine: it drains the provider sequence, dispatches
// candidate parts to the named part handlers, and finishes with the
// canonical completion, usage, and stop chunks before closing chunks.
func (s *geminiStreamer) run(seq func(func(*genai.GenerateContentResponse, error) bool), prep *preparedRequest) {
	defer close(s.chunks)
	var stopReason string
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
					if err := s.handleFunctionCallPart(part, prep); err != nil {
						s.setErr(err)
						return
					}
				case part.Thought:
					s.handleThoughtPart(part)
				case part.Text != "":
					s.handleTextPart(part, prep)
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
	if prep.structuredOutput != nil {
		payload, perr := finalStructuredCompletionPayload(s.completionText.String())
		if perr != nil {
			s.setErr(fmt.Errorf("vertex: structured output %q: %w", prep.structuredOutput.Name, perr))
			return
		}
		s.emit(model.Chunk{Type: model.ChunkTypeCompletion, Completion: &model.Completion{
			Name:    prep.structuredOutput.Name,
			Payload: payload,
		}})
	}
	if usageSeen {
		s.emit(model.Chunk{Type: model.ChunkTypeUsage, UsageDelta: &latestUsage})
	}
	if stopReason != "" {
		s.emit(model.Chunk{Type: model.ChunkTypeStop, StopReason: stopReason})
	}
}

// handleFunctionCallPart emits the finalized tool call for a functionCall
// part, carrying the provider-issued tool-call thought signature when
// present. A payload marshal failure is terminal for the stream.
func (s *geminiStreamer) handleFunctionCallPart(part *genai.Part, prep *preparedRequest) error {
	s.callIndex++
	payload, err := marshalArgs(part.FunctionCall.Args)
	if err != nil {
		return err
	}
	s.emit(model.Chunk{Type: model.ChunkTypeToolCall, ToolCall: &model.ToolCall{
		Name:             toolIdent(part.FunctionCall.Name, prep.provToCanon),
		Payload:          payload,
		ID:               toolCallID(part.FunctionCall, s.callIndex),
		ThoughtSignature: encodeThoughtSignature(part.ThoughtSignature),
	}})
	return nil
}

// handleThoughtPart accumulates thought text across Thought parts (mirrors
// the anthropic streamer's thinkingBuffer). Draft chunks are display-only
// and only emitted for text-bearing parts, so a signature-only part
// produces no empty-draft noise. When a signature arrives, the final
// ThinkingPart carries the FULL accumulated text plus the signature — the
// transcript ledger (BuildMessages) only replays thinking parts that have
// both Text and Signature set, so a signature emitted alone would be
// silently dropped.
func (s *geminiStreamer) handleThoughtPart(part *genai.Part) {
	if part.Text != "" {
		s.thoughtText.WriteString(part.Text)
		draft := model.ThinkingPart{Text: part.Text}
		s.emit(model.Chunk{Type: model.ChunkTypeThinking, Thinking: part.Text,
			Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{draft}}})
	}
	if len(part.ThoughtSignature) > 0 {
		final := model.ThinkingPart{
			Text:      s.thoughtText.String(),
			Signature: base64.StdEncoding.EncodeToString(part.ThoughtSignature),
			Final:     true,
		}
		s.emit(model.Chunk{Type: model.ChunkTypeThinking,
			Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{final}}})
		s.thoughtText.Reset()
	}
}

// handleTextPart emits assistant text, or a CompletionDelta preview when
// the request declared structured output: structured-output requests
// replace free-form assistant text with the typed completion contract (see
// runtime/agent/completion) — text deltas become CompletionDelta previews
// and the accumulated text is validated and emitted as one canonical
// Completion chunk once the stream ends, mirroring the bedrock adapter.
func (s *geminiStreamer) handleTextPart(part *genai.Part, prep *preparedRequest) {
	if prep.structuredOutput != nil {
		s.completionText.WriteString(part.Text)
		s.emit(model.Chunk{Type: model.ChunkTypeCompletionDelta, CompletionDelta: &model.CompletionDelta{
			Name:  prep.structuredOutput.Name,
			Delta: part.Text,
		}})
		return
	}
	s.emit(model.Chunk{Type: model.ChunkTypeText,
		Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: part.Text}}}})
}

// emit delivers a chunk to Recv, dropping it when the pump context is
// canceled so an abandoned stream never blocks the goroutine.
func (s *geminiStreamer) emit(ch model.Chunk) {
	select {
	case s.chunks <- ch:
	case <-s.ctx.Done():
	}
}

// setErr records the terminal pump error surfaced by Recv after chunks
// closes.
func (s *geminiStreamer) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// finalStructuredCompletionPayload validates the fully-accumulated
// structured-output text as canonical JSON. Unlike tool-call payload
// fragments, typed completions use no fallbacks: empty or invalid JSON is a
// hard provider contract violation surfaced to the caller instead of a
// best-effort coercion.
func finalStructuredCompletionPayload(accumulated string) (rawjson.Message, error) {
	trimmed := strings.TrimSpace(accumulated)
	if trimmed == "" {
		return nil, errors.New("structured completion payload is empty")
	}
	data := []byte(trimmed)
	if !json.Valid(data) {
		return nil, fmt.Errorf("structured completion payload is not valid JSON: %q", trimmed)
	}
	return rawjson.Message(data), nil
}
