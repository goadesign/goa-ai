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

	metaMu   sync.RWMutex
	metadata map[string]any
}

func newBedrockStreamer(ctx context.Context, stream *bedrockruntime.ConverseStreamEventStream) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	bs := &bedrockStreamer{
		ctx:    cctx,
		cancel: cancel,
		stream: stream,
		chunks: make(chan model.Chunk, 32),
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
			if err != nil {
				return model.Chunk{}, err
			}
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
	defer func() { _ = s.stream.Close() }()

	processor := newChunkProcessor(s.emitChunk, s.recordUsage)
	events := s.stream.Events()

	for {
		select {
		case <-s.ctx.Done():
			s.setErr(s.ctx.Err())
			return
		case event, ok := <-events:
			if !ok {
				if err := s.stream.Err(); err != nil {
					s.setErr(err)
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

	toolBlocks map[int]*toolBuffer
}

func newChunkProcessor(emit func(model.Chunk) error, recordUsage func(model.TokenUsage)) *chunkProcessor {
	return &chunkProcessor{
		emit:        emit,
		recordUsage: recordUsage,
		toolBlocks:  make(map[int]*toolBuffer),
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
				if toolUse.Value.Name != nil {
					tb.name = normalizeToolName(*toolUse.Value.Name)
				}
				if toolUse.Value.ToolUseId != nil {
					tb.id = *toolUse.Value.ToolUseId
				}
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
			if strings.TrimSpace(delta.Value) == "" {
				return nil
			}
			return p.emit(model.Chunk{
				Type:    model.ChunkTypeText,
				Message: &model.Message{Role: "assistant", Content: delta.Value, Meta: map[string]any{"content_index": idx}},
			})
		case *brtypes.ContentBlockDeltaMemberReasoningContent:
			if textDelta, ok := delta.Value.(*brtypes.ReasoningContentBlockDeltaMemberText); ok {
				if strings.TrimSpace(textDelta.Value) == "" {
					return nil
				}
				return p.emit(model.Chunk{
					Type:     model.ChunkTypeThinking,
					Thinking: textDelta.Value,
					Message:  &model.Message{Role: "assistant", Content: textDelta.Value, Meta: map[string]any{"content_index": idx}},
				})
			}
		case *brtypes.ContentBlockDeltaMemberToolUse:
			if tb := p.toolBlocks[idx]; tb != nil && delta.Value.Input != nil {
				tb.fragments = append(tb.fragments, *delta.Value.Input)
			}
			return nil
		}
	case *brtypes.ConverseStreamOutputMemberContentBlockStop:
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		if tb := p.toolBlocks[idx]; tb != nil {
			payload := decodeToolPayload(tb.finalInput())
			delete(p.toolBlocks, idx)
			return p.emit(model.Chunk{
				Type:     model.ChunkTypeToolCall,
				ToolCall: &model.ToolCall{Name: tb.name, Payload: payload},
			})
		}
		return nil
	case *brtypes.ConverseStreamOutputMemberMessageStop:
		chunk := model.Chunk{Type: model.ChunkTypeStop}
		if ev.Value.StopReason != "" {
			chunk.StopReason = string(ev.Value.StopReason)
		}
		p.toolBlocks = make(map[int]*toolBuffer)
		return p.emit(chunk)
	case *brtypes.ConverseStreamOutputMemberMetadata:
		if ev.Value.Usage == nil {
			return nil
		}
		// Compute ints efficiently with direct nil checks (avoid helper + double cast)
		var in, out, tot int
		if t := ev.Value.Usage.InputTokens; t != nil {
			in = int(*t)
		}
		if t := ev.Value.Usage.OutputTokens; t != nil {
			out = int(*t)
		}
		if t := ev.Value.Usage.TotalTokens; t != nil {
			tot = int(*t)
		}
		usage := model.TokenUsage{InputTokens: in, OutputTokens: out, TotalTokens: tot}
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
	if strings.TrimSpace(joined) == "" {
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

func decodeToolPayload(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return map[string]any{"raw": raw}
	}
	return payload
}

func normalizeToolName(name string) string {
	if strings.HasPrefix(name, "$FUNCTIONS.") {
		return strings.TrimPrefix(name, "$FUNCTIONS.")
	}
	return name
}
