package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// anthropicStreamer adapts an Anthropic Messages streaming stream to the
// model.Streamer interface.
type anthropicStreamer struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream *ssestream.Stream[sdk.MessageStreamEventUnion]

	chunks chan model.Chunk

	errMu    sync.Mutex
	errSet   bool
	finalErr error

	responseMu sync.RWMutex
	response   *model.Response

	toolNameMap map[string]string
}

func newAnthropicStreamer(ctx context.Context, stream *ssestream.Stream[sdk.MessageStreamEventUnion], nameMap map[string]string) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	as := &anthropicStreamer{
		ctx:         cctx,
		cancel:      cancel,
		stream:      stream,
		chunks:      make(chan model.Chunk, 32),
		toolNameMap: nameMap,
	}
	go as.run()
	return as
}

func (s *anthropicStreamer) Recv() (model.Chunk, error) {
	select {
	case chunk, ok := <-s.chunks:
		if ok {
			return chunk, nil
		}
		if err := s.err(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			s.setErr(err)
			return nil, err
		}
		return nil, io.EOF
	case <-s.ctx.Done():
		err := s.ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		s.setErr(err)
		return nil, err
	}
}

func (s *anthropicStreamer) Close() error {
	s.cancel()
	if s.stream == nil {
		return nil
	}
	return s.stream.Close()
}

func (s *anthropicStreamer) Response() *model.Response {
	s.responseMu.RLock()
	defer s.responseMu.RUnlock()
	return s.response
}

func (s *anthropicStreamer) run() {
	defer close(s.chunks)
	defer func() {
		if s.stream != nil {
			if err := s.stream.Close(); err != nil {
				s.setErr(err)
			}
		}
	}()

	processor := newAnthropicChunkProcessor(s.emitChunk, s.toolNameMap)
	var response sdk.Message

	for {
		select {
		case <-s.ctx.Done():
			s.setErr(s.ctx.Err())
			return
		default:
		}
		if !s.stream.Next() {
			if err := s.stream.Err(); err != nil {
				s.setErr(wrapAnthropicError("stream_recv", err))
			} else if err := s.ctx.Err(); err != nil {
				s.setErr(err)
			} else if !processor.complete {
				s.setErr(streamEndedEarlyError(processor.started))
			} else {
				translated, err := translateResponse(&response, s.toolNameMap)
				if err != nil {
					s.setErr(err)
					return
				}
				s.responseMu.Lock()
				s.response = translated
				s.responseMu.Unlock()
			}
			return
		}
		event := s.stream.Current()
		if err := response.Accumulate(event); err != nil {
			s.setErr(fmt.Errorf("anthropic: accumulate streamed response: %w", err))
			return
		}
		if err := processor.Handle(event); err != nil {
			s.setErr(err)
			return
		}
	}
}

func (s *anthropicStreamer) emitChunk(chunk model.Chunk) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		return nil
	}
}

func (s *anthropicStreamer) setErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.errSet {
		return
	}
	s.errSet = true
	s.finalErr = err
}

func (s *anthropicStreamer) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.finalErr
}

// anthropicChunkProcessor converts Anthropic streaming events into model.Chunks.
type anthropicChunkProcessor struct {
	emit func(model.Chunk) error

	toolBlocks     map[int]*toolBuffer
	thinkingBlocks map[int]*thinkingBuffer
	openBlocks     map[int]struct{}

	toolNameMap map[string]string

	stopReason string
	started    bool
	complete   bool
}

func newAnthropicChunkProcessor(emit func(model.Chunk) error, nameMap map[string]string) *anthropicChunkProcessor {
	return &anthropicChunkProcessor{
		emit:           emit,
		toolBlocks:     make(map[int]*toolBuffer),
		thinkingBlocks: make(map[int]*thinkingBuffer),
		openBlocks:     make(map[int]struct{}),
		toolNameMap:    nameMap,
	}
}

func (p *anthropicChunkProcessor) Handle(event sdk.MessageStreamEventUnion) error {
	switch ev := event.AsAny().(type) {
	case sdk.MessageStartEvent:
		if p.started {
			return errors.New("anthropic stream: duplicate message start")
		}
		p.toolBlocks = make(map[int]*toolBuffer)
		p.thinkingBlocks = make(map[int]*thinkingBuffer)
		p.openBlocks = make(map[int]struct{})
		p.stopReason = ""
		p.started = true
		p.complete = false
		return nil
	case sdk.ContentBlockStartEvent:
		if !p.started || p.complete {
			return errors.New("anthropic stream: content block started outside an active message")
		}
		idx := int(ev.Index)
		if _, ok := p.openBlocks[idx]; ok {
			return fmt.Errorf("anthropic stream: duplicate content block start %d", idx)
		}
		p.openBlocks[idx] = struct{}{}
		start := ev.ContentBlock.AsAny()
		if text, ok := start.(sdk.TextBlock); ok {
			if text.Text == "" {
				return nil
			}
			return p.emit(model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: text.Text}},
					Meta:  map[string]any{"content_index": idx},
				},
			})
		}
		if toolUse, ok := start.(sdk.ToolUseBlock); ok {
			tb := &toolBuffer{}
			if toolUse.ID == "" {
				return fmt.Errorf("anthropic stream: tool use block missing id")
			}
			if toolUse.Name == "" {
				return fmt.Errorf("anthropic stream: tool use block %q missing name", toolUse.ID)
			}
			raw := toolUse.Name
			// Anthropic echoes the provider-visible tool name in tool_use blocks.
			// When the model hallucinates a tool name that was not advertised in this
			// request, the reverse map will not contain it. Surface the tool call
			// as-is and let the runtime convert it into an "unknown tool" result so
			// the model can recover on the next resume turn.
			if canonical, ok := p.toolNameMap[raw]; ok {
				tb.name = canonical
			} else {
				tb.name = raw
			}
			tb.id = toolUse.ID
			p.toolBlocks[idx] = tb
			return nil
		}
		if thinking, ok := start.(sdk.ThinkingBlock); ok {
			tb := &thinkingBuffer{signature: thinking.Signature}
			tb.text.WriteString(thinking.Thinking)
			p.thinkingBlocks[idx] = tb
			return nil
		}
		if redacted, ok := start.(sdk.RedactedThinkingBlock); ok {
			if redacted.Data == "" {
				return errors.New("anthropic stream: redacted thinking block missing data")
			}
			p.thinkingBlocks[idx] = &thinkingBuffer{redacted: []byte(redacted.Data)}
			return nil
		}
		return fmt.Errorf("anthropic stream: unsupported content block %T", start)
	case sdk.ContentBlockDeltaEvent:
		if !p.started || p.complete {
			return errors.New("anthropic stream: content block delta received outside an active message")
		}
		idx := int(ev.Index)
		if _, ok := p.openBlocks[idx]; !ok {
			return fmt.Errorf("anthropic stream: content block delta %d has no matching start", idx)
		}
		switch delta := ev.Delta.AsAny().(type) {
		case sdk.TextDelta:
			if delta.Text == "" {
				return nil
			}
			return p.emit(model.TextChunk{
				Message: model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.TextPart{Text: delta.Text},
					},
					Meta: map[string]any{"content_index": idx},
				},
			})
		case sdk.InputJSONDelta:
			if delta.PartialJSON == "" {
				return nil
			}
			if tb := p.toolBlocks[idx]; tb != nil {
				tb.fragments = append(tb.fragments, delta.PartialJSON)
				if tb.id == "" {
					return fmt.Errorf("anthropic stream: tool JSON delta missing tool call id")
				}
				if tb.name == "" {
					return fmt.Errorf("anthropic stream: tool JSON delta missing tool name for id %q", tb.id)
				}
				return p.emit(model.ToolCallDeltaChunk{
					Delta: model.ToolCallDelta{
						Name:  tools.Ident(tb.name),
						ID:    tb.id,
						Delta: delta.PartialJSON,
					},
				})
			}
			return fmt.Errorf("anthropic stream: input JSON delta %d has no tool-use block", idx)
		case sdk.ThinkingDelta:
			if delta.Thinking == "" {
				return nil
			}
			tb := p.thinkingBlocks[idx]
			if tb == nil {
				tb = &thinkingBuffer{}
				p.thinkingBlocks[idx] = tb
			}
			tb.text.WriteString(delta.Thinking)
			return p.emit(model.ThinkingChunk{
				Message: model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.ThinkingPart{
							Text:  delta.Thinking,
							Index: idx,
							Final: false,
						},
					},
				},
			})
		case sdk.SignatureDelta:
			if delta.Signature == "" {
				return nil
			}
			tb := p.thinkingBlocks[idx]
			if tb == nil {
				tb = &thinkingBuffer{}
				p.thinkingBlocks[idx] = tb
			}
			tb.signature = delta.Signature
			return nil
		case sdk.CitationsDelta:
			// Citation deltas have no presentation chunk. The SDK accumulates
			// them into the terminal text block, which translateResponse maps
			// into the canonical CitationsPart returned by Response.
			return nil
		default:
			return fmt.Errorf("anthropic stream: unsupported content block delta %T", delta)
		}
	case sdk.ContentBlockStopEvent:
		if !p.started || p.complete {
			return errors.New("anthropic stream: content block stopped outside an active message")
		}
		idx := int(ev.Index)
		if _, ok := p.openBlocks[idx]; !ok {
			return fmt.Errorf("anthropic stream: content block stop %d has no matching start", idx)
		}
		delete(p.openBlocks, idx)
		if tb := p.thinkingBlocks[idx]; tb != nil {
			delete(p.thinkingBlocks, idx)
			part, err := tb.finalize(idx)
			if err != nil {
				return fmt.Errorf("anthropic stream: finalize thinking block %d: %w", idx, err)
			}
			if part != nil {
				if part.Text != "" {
					if err := p.emit(model.ThinkingChunk{
						Message: model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{*part},
						},
					}); err != nil {
						return err
					}
				} else if len(part.Redacted) > 0 {
					if err := p.emit(model.ThinkingChunk{
						Message: model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{*part},
						},
					}); err != nil {
						return err
					}
				}
			}
		}
		if tb := p.toolBlocks[idx]; tb != nil {
			payload, err := decodeToolPayload(tb.finalInput())
			if err != nil {
				return fmt.Errorf("anthropic stream: finalize tool payload %q: %w", tb.id, err)
			}
			delete(p.toolBlocks, idx)
			return p.emit(model.ToolCallChunk{
				ToolCall: model.ToolCall{
					Name:    tools.Ident(tb.name),
					Payload: payload,
					ID:      tb.id,
				},
			})
		}
		return nil
	case sdk.MessageDeltaEvent:
		if !p.started || p.complete {
			return errors.New("anthropic stream: message delta received outside an active message")
		}
		p.stopReason = string(ev.Delta.StopReason)
		usage := model.TokenUsage{
			InputTokens:      int(ev.Usage.InputTokens),
			OutputTokens:     int(ev.Usage.OutputTokens),
			TotalTokens:      int(ev.Usage.InputTokens + ev.Usage.OutputTokens),
			CacheReadTokens:  int(ev.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(ev.Usage.CacheCreationInputTokens),
		}
		return p.emit(model.UsageChunk{Usage: usage})
	case sdk.MessageStopEvent:
		if !p.started {
			// Anthropic models intermittently emit an empty completion whose
			// stream stops a message that never started. Classify as a
			// retryable empty stream instead of an opaque protocol error.
			return model.NewEmptyStreamError(
				anthropicProviderName,
				"stream_recv",
				"message stop received without an active message",
			)
		}
		if p.complete {
			return errors.New("anthropic stream: duplicate message stop")
		}
		if len(p.openBlocks) > 0 {
			return fmt.Errorf("anthropic stream: message stopped with %d open content blocks", len(p.openBlocks))
		}
		if p.stopReason == "" {
			return errors.New("anthropic stream: message stopped without a stop reason")
		}
		chunk := model.StopChunk{Reason: p.stopReason}
		p.complete = true
		return p.emit(chunk)
	default:
		return fmt.Errorf("anthropic stream: unsupported event %T", ev)
	}
}

type toolBuffer struct {
	name      string
	id        string
	fragments []string
}

func (tb *toolBuffer) finalInput() string {
	if len(tb.fragments) == 0 {
		return "{}"
	}
	joined := strings.Join(tb.fragments, "")
	if strings.TrimSpace(joined) == "" {
		return "{}"
	}
	return joined
}

type thinkingBuffer struct {
	text      strings.Builder
	signature string
	redacted  []byte
}

func (tb *thinkingBuffer) finalize(index int) (*model.ThinkingPart, error) {
	text := tb.text.String()
	if len(tb.redacted) > 0 {
		if text != "" || tb.signature != "" {
			return nil, errors.New("thinking block contains both redacted and plaintext content")
		}
		return &model.ThinkingPart{
			Redacted: append([]byte(nil), tb.redacted...),
			Index:    index,
			Final:    true,
		}, nil
	}
	if text == "" && tb.signature == "" {
		return nil, nil
	}
	if text == "" {
		return nil, errors.New("thinking signature is missing plaintext content")
	}
	if tb.signature == "" {
		return nil, errors.New("thinking plaintext is missing provider signature")
	}
	return &model.ThinkingPart{
		Text:      text,
		Signature: tb.signature,
		Index:     index,
		Final:     true,
	}, nil
}

func decodeToolPayload(raw string) (rawjson.Message, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	data := []byte(trimmed)
	if !json.Valid(data) {
		return nil, errors.New("tool payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

// streamEndedEarlyError classifies an Anthropic event stream that closed
// cleanly before message stop. When no message ever started, the provider
// produced an empty completion and callers may retry (model.ErrEmptyStream).
// When a message was underway, the stream was truncated mid-generation: a
// fresh request regenerates the full response, so the failure is a retryable
// provider fault but not an empty stream.
func streamEndedEarlyError(started bool) error {
	if !started {
		return model.NewEmptyStreamError(
			anthropicProviderName,
			"stream_recv",
			"stream ended before message start",
		)
	}
	return model.NewProviderError(
		anthropicProviderName,
		"stream_recv",
		0,
		model.ProviderErrorKindUnavailable,
		"truncated_stream",
		"stream ended before message stop",
		"",
		true,
		nil,
	)
}
