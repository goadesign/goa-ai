package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// bedrockStreamer adapts a Bedrock ConverseStream event stream to the
// model.Streamer interface.
type bedrockStreamer struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream *bedrockruntime.ConverseStreamEventStream

	chunks chan model.Chunk

	errMu    sync.Mutex
	errSet   bool
	finalErr error

	metaMu      sync.RWMutex
	metadata    map[string]any
	toolNameMap map[string]string
}

func newBedrockStreamer(ctx context.Context, stream *bedrockruntime.ConverseStreamEventStream, nameMap map[string]string) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	bs := &bedrockStreamer{
		ctx:         cctx,
		cancel:      cancel,
		stream:      stream,
		chunks:      make(chan model.Chunk, 32),
		toolNameMap: nameMap,
	}
	go bs.run()
	return bs
}

func (s *bedrockStreamer) Recv() (model.Chunk, error) {
	select {
	case chunk, ok := <-s.chunks:
		if ok {
			return chunk, nil
		}
		if err := s.err(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return model.Chunk{}, err
			}
			s.setErr(err)
			return model.Chunk{}, err
		}
		return model.Chunk{}, io.EOF
	case <-s.ctx.Done():
		err := s.ctx.Err()
		if err == nil {
			err = context.Canceled
		}
		s.setErr(err)
		return model.Chunk{}, err
	}
}

func (s *bedrockStreamer) Close() error {
	s.cancel()
	return s.stream.Close()
}

func (s *bedrockStreamer) Metadata() map[string]any {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	if len(s.metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(s.metadata))
	for k, v := range s.metadata {
		out[k] = v
	}
	return out
}

func (s *bedrockStreamer) run() {
	defer close(s.chunks)
	defer func() {
		if err := s.stream.Close(); err != nil {
			s.setErr(err)
		}
	}()

	processor := newChunkProcessor(s.emitChunk, s.recordUsage, s.recordCitations, s.toolNameMap)
	events := s.stream.Events()

	for {
		select {
		case <-s.ctx.Done():
			s.setErr(s.ctx.Err())
			return
		case event, ok := <-events:
			if !ok {
				if err := s.stream.Err(); err != nil {
					s.setErr(wrapBedrockError("converse_stream.recv", err))
				} else if err := s.ctx.Err(); err != nil {
					s.setErr(err)
				} else {
					s.setErr(nil)
				}
				return
			}
			if err := processor.Handle(event); err != nil {
				s.setErr(err)
				return
			}
		}
	}
}

func (s *bedrockStreamer) emitChunk(chunk model.Chunk) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		return nil
	}
}

func (s *bedrockStreamer) recordUsage(usage model.TokenUsage) {
	s.metaMu.Lock()
	if s.metadata == nil {
		s.metadata = make(map[string]any)
	}
	s.metadata["usage"] = usage
	s.metaMu.Unlock()
}

func (s *bedrockStreamer) recordCitations(citations []model.Citation) {
	if len(citations) == 0 {
		return
	}
	s.metaMu.Lock()
	if s.metadata == nil {
		s.metadata = make(map[string]any)
	}
	if prev, ok := s.metadata["citations"].([]model.Citation); ok && len(prev) > 0 {
		citations = append(prev, citations...)
	}
	s.metadata["citations"] = citations
	s.metaMu.Unlock()
}

func (s *bedrockStreamer) setErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.errSet {
		return
	}
	s.errSet = true
	s.finalErr = err
}

func (s *bedrockStreamer) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.finalErr
}

// chunkProcessor converts Bedrock streaming events into model.Chunks.
type chunkProcessor struct {
	emit        func(model.Chunk) error
	recordUsage func(model.TokenUsage)
	recordCites func([]model.Citation)

	toolBlocks map[int]*toolBuffer
	// reasoningBlocks accumulates reasoning content per content index until stop.
	reasoningBlocks map[int]*reasoningBuffer

	toolNameMap map[string]string
}

func newChunkProcessor(
	emit func(model.Chunk) error,
	recordUsage func(model.TokenUsage),
	recordCites func([]model.Citation),
	nameMap map[string]string,
) *chunkProcessor {
	return &chunkProcessor{
		emit:            emit,
		recordUsage:     recordUsage,
		recordCites:     recordCites,
		toolBlocks:      make(map[int]*toolBuffer),
		reasoningBlocks: make(map[int]*reasoningBuffer),
		toolNameMap:     nameMap,
	}
}

func (p *chunkProcessor) Handle(event any) error {
	switch ev := event.(type) {
	case *brtypes.ConverseStreamOutputMemberMessageStart:
		p.toolBlocks = make(map[int]*toolBuffer)
		return nil
	case *brtypes.ConverseStreamOutputMemberContentBlockStart:
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		if start := ev.Value.Start; start != nil {
			if toolUse, ok := start.(*brtypes.ContentBlockStartMemberToolUse); ok {
				tb := &toolBuffer{}
				if toolUse.Value.ToolUseId == nil || *toolUse.Value.ToolUseId == "" {
					return fmt.Errorf("bedrock stream: tool use block missing tool_use_id")
				}
				tb.id = *toolUse.Value.ToolUseId
				if toolUse.Value.Name == nil || *toolUse.Value.Name == "" {
					return fmt.Errorf("bedrock stream: tool use block %q missing name", tb.id)
				}
				raw := *toolUse.Value.Name
				name := normalizeToolName(raw)
				canonical, ok := p.toolNameMap[name]
				if !ok {
					return fmt.Errorf(
						"bedrock stream: tool name %q not in reverse map (raw: %q); expected canonical tool ID",
						name, raw,
					)
				}
				tb.name = canonical
				p.toolBlocks[idx] = tb
				return nil
			}
		}
		return nil
	case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		switch delta := ev.Value.Delta.(type) {
		case *brtypes.ContentBlockDeltaMemberText:
			if delta.Value == "" {
				return nil
			}
			return p.emit(model.Chunk{
				Type: model.ChunkTypeText,
				Message: &model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: delta.Value}},
					Meta:  map[string]any{"content_index": idx},
				},
			})
		case *brtypes.ContentBlockDeltaMemberCitation:
			if p.recordCites == nil {
				return nil
			}
			citation := translateCitationDelta(delta.Value)
			if citation.Title == "" && citation.Source == "" && citation.Location == (model.CitationLocation{}) && len(citation.SourceContent) == 0 {
				return nil
			}
			p.recordCites([]model.Citation{citation})
			return nil
		case *brtypes.ContentBlockDeltaMemberReasoningContent:
			// Initialize/lookup buffer for this content index.
			rb := p.reasoningBlocks[idx]
			if rb == nil {
				rb = &reasoningBuffer{}
				p.reasoningBlocks[idx] = rb
			}
			// Capture reasoning deltas (text, redacted bytes, signature).
			switch v := delta.Value.(type) {
			case *brtypes.ReasoningContentBlockDeltaMemberText:
				if v.Value == "" {
					return nil
				}
				rb.text.WriteString(v.Value)
				// Stream incremental thinking text for UX; final part is emitted on stop.
				return p.emit(model.Chunk{
					Type:     model.ChunkTypeThinking,
					Thinking: v.Value,
					Message: &model.Message{
						Role: "assistant",
						Parts: []model.Part{model.ThinkingPart{
							Text:  v.Value,
							Index: idx,
							Final: false,
						}},
					},
				})
			case *brtypes.ReasoningContentBlockDeltaMemberRedactedContent:
				if len(v.Value) > 0 {
					rb.redacted = append(rb.redacted, v.Value...)
				}
				return nil
			case *brtypes.ReasoningContentBlockDeltaMemberSignature:
				if v.Value != "" {
					rb.signature = v.Value
				}
				return nil
			default:
				return nil
			}
		case *brtypes.ContentBlockDeltaMemberToolUse:
			if tb := p.toolBlocks[idx]; tb != nil && delta.Value.Input != nil {
				fragment := *delta.Value.Input
				tb.fragments = append(tb.fragments, fragment)
				if tb.id == "" {
					return fmt.Errorf("bedrock stream: tool JSON delta missing tool call id")
				}
				if tb.name == "" {
					return fmt.Errorf("bedrock stream: tool JSON delta missing tool name for id %q", tb.id)
				}
				return p.emit(model.Chunk{
					Type: model.ChunkTypeToolCallDelta,
					ToolCallDelta: &model.ToolCallDelta{
						Name:  tools.Ident(tb.name),
						ID:    tb.id,
						Delta: fragment,
					},
				})
			}
			return nil
		}
	case *brtypes.ConverseStreamOutputMemberContentBlockStop:
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		// Finalize any reasoning block accumulated for this index.
		if rb := p.reasoningBlocks[idx]; rb != nil {
			delete(p.reasoningBlocks, idx)
			if part := rb.finalize(); part != nil {
				part.Index = idx
				part.Final = true
				if part.Text != "" {
					// Emit final plaintext thinking with signature preserved.
					if err := p.emit(model.Chunk{
						Type:     model.ChunkTypeThinking,
						Thinking: part.Text,
						Message: &model.Message{
							Role:  "assistant",
							Parts: []model.Part{*part},
						},
					}); err != nil {
						return err
					}
				} else if len(part.Redacted) > 0 {
					// Emit final redacted thinking.
					if err := p.emit(model.Chunk{
						Type: model.ChunkTypeThinking,
						Message: &model.Message{
							Role:  "assistant",
							Parts: []model.Part{*part},
						},
					}); err != nil {
						return err
					}
				}
			}
		}
		if tb := p.toolBlocks[idx]; tb != nil {
			payload := decodeToolPayload(tb.finalInput())
			delete(p.toolBlocks, idx)
			return p.emit(model.Chunk{
				Type: model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{
					Name:    tools.Ident(tb.name),
					Payload: payload,
					ID:      tb.id,
				},
			})
		}
		return nil
	case *brtypes.ConverseStreamOutputMemberMessageStop:
		chunk := model.Chunk{Type: model.ChunkTypeStop}
		if ev.Value.StopReason != "" {
			chunk.StopReason = string(ev.Value.StopReason)
		}
		p.toolBlocks = make(map[int]*toolBuffer)
		p.reasoningBlocks = make(map[int]*reasoningBuffer)
		return p.emit(chunk)
	case *brtypes.ConverseStreamOutputMemberMetadata:
		if ev.Value.Usage == nil {
			return nil
		}
		// Compute ints efficiently with direct nil checks (avoid helper + double cast)
		var in, out, tot, cacheRead, cacheWrite int
		if t := ev.Value.Usage.InputTokens; t != nil {
			in = int(*t)
		}
		if t := ev.Value.Usage.OutputTokens; t != nil {
			out = int(*t)
		}
		if t := ev.Value.Usage.TotalTokens; t != nil {
			tot = int(*t)
		}
		if t := ev.Value.Usage.CacheReadInputTokens; t != nil {
			cacheRead = int(*t)
		}
		if t := ev.Value.Usage.CacheWriteInputTokens; t != nil {
			cacheWrite = int(*t)
		}
		usage := model.TokenUsage{
			InputTokens:      in,
			OutputTokens:     out,
			TotalTokens:      tot,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		}
		if p.recordUsage != nil {
			p.recordUsage(usage)
		}
		return p.emit(model.Chunk{Type: model.ChunkTypeUsage, UsageDelta: &usage})
	}
	return nil
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
	if joined == "" {
		return "{}"
	}
	return joined
}

func contentIndex(idx *int32) (int, error) {
	if idx == nil {
		return 0, fmt.Errorf("bedrock: content block index missing")
	}
	return int(*idx), nil
}

func decodeToolPayload(raw string) json.RawMessage {
	trimmed := raw
	if trimmed == "" {
		trimmed = "{}"
	}
	data := []byte(trimmed)
	if len(data) == 0 {
		return nil
	}
	return json.RawMessage(data)
}

func translateCitationDelta(delta brtypes.CitationsDelta) model.Citation {
	out := model.Citation{
		Location:      translateCitationLocationDelta(delta.Location),
		SourceContent: translateCitationSourceContentDelta(delta.SourceContent),
	}
	if delta.Title != nil {
		out.Title = *delta.Title
	}
	if delta.Source != nil {
		out.Source = *delta.Source
	}
	return out
}

func translateCitationLocationDelta(loc brtypes.CitationLocation) model.CitationLocation {
	switch v := loc.(type) {
	case *brtypes.CitationLocationMemberDocumentChar:
		return model.CitationLocation{
			DocumentChar: &model.DocumentCharLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}
	case *brtypes.CitationLocationMemberDocumentChunk:
		return model.CitationLocation{
			DocumentChunk: &model.DocumentChunkLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}
	case *brtypes.CitationLocationMemberDocumentPage:
		return model.CitationLocation{
			DocumentPage: &model.DocumentPageLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}
	default:
		return model.CitationLocation{}
	}
}

func translateCitationSourceContentDelta(contents []brtypes.CitationSourceContentDelta) []string {
	if len(contents) == 0 {
		return nil
	}
	out := make([]string, 0, len(contents))
	for _, content := range contents {
		if content.Text != nil && *content.Text != "" {
			out = append(out, *content.Text)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func int32Value(ptr *int32) int {
	if ptr == nil {
		return 0
	}
	return int(*ptr)
}

func normalizeToolName(name string) string {
	if strings.HasPrefix(name, "$FUNCTIONS.") {
		return strings.TrimPrefix(name, "$FUNCTIONS.")
	}
	return name
}

type reasoningBuffer struct {
	text      strings.Builder
	redacted  []byte
	signature string
}

func (rb *reasoningBuffer) finalize() *model.ThinkingPart {
	// Prefer redacted variant when present.
	if len(rb.redacted) > 0 {
		return &model.ThinkingPart{Redacted: append([]byte(nil), rb.redacted...)}
	}
	if s := rb.text.String(); s != "" && rb.signature != "" {
		return &model.ThinkingPart{
			Text:      s,
			Signature: rb.signature,
		}
	}
	return nil
}
