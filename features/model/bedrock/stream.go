package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// bedrockStreamer adapts a Bedrock ConverseStream event stream to the
// model.Streamer interface. It stamps model attribution (modelID, modelClass)
// onto usage chunks so downstream consumers can attribute token costs.
type bedrockStreamer struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream *bedrockruntime.ConverseStreamEventStream

	chunks chan model.Chunk
	done   chan struct{}

	errMu    sync.Mutex
	errSet   bool
	finalErr error

	closeOnce sync.Once
	closeErr  error

	responseMu       sync.RWMutex
	response         *model.Response
	toolNameMap      map[string]string
	modelID          string
	modelClass       model.ModelClass
	output           *model.StructuredOutput
	toolFallbackName string
	contract         *model.RequestContract
}

// newBedrockStreamer adapts a Bedrock ConverseStream to model.Streamer.
// toolFallbackName is non-empty only when structuredOutputUsesToolFallback
// chose to express output (structured output) as a forced tool call; the
// streamer then treats that one tool_use block as the completion channel
// instead of a canonical ToolCallChunk.
func newBedrockStreamer(
	ctx context.Context,
	stream *bedrockruntime.ConverseStreamEventStream,
	nameMap map[string]string,
	modelID string,
	modelClass model.ModelClass,
	output *model.StructuredOutput,
	toolFallbackName string,
	contract *model.RequestContract,
) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	bs := &bedrockStreamer{
		ctx:              cctx,
		cancel:           cancel,
		stream:           stream,
		chunks:           make(chan model.Chunk, 32),
		done:             make(chan struct{}),
		toolNameMap:      nameMap,
		modelID:          modelID,
		modelClass:       modelClass,
		output:           output,
		toolFallbackName: toolFallbackName,
		contract:         contract,
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

func (s *bedrockStreamer) Close() error {
	s.cancel()
	closeErr := s.closeProviderStream()
	<-s.done
	return closeErr
}

func (s *bedrockStreamer) Response() *model.Response {
	s.responseMu.RLock()
	defer s.responseMu.RUnlock()
	return s.response
}

func (s *bedrockStreamer) run() {
	defer close(s.chunks)
	defer close(s.done)
	defer func() {
		if err := s.closeProviderStream(); err != nil {
			s.setErr(err)
		}
	}()

	processor := newChunkProcessor(
		s.emitChunk,
		s.toolNameMap,
		s.modelID,
		s.modelClass,
		s.output,
		s.toolFallbackName,
	)
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
				} else if !processor.complete {
					s.setErr(model.NewStreamEndedEarlyError(
						bedrockProviderName,
						"converse_stream",
						processor.started,
					))
				} else if err := processor.finishStream(); err != nil {
					s.setErr(s.outputError(processor.rejectedUsage(), err))
				} else {
					response := processor.response()
					s.responseMu.Lock()
					s.response = response
					s.responseMu.Unlock()
				}
				return
			}
			if err := processor.Handle(event); err != nil {
				s.setErr(s.outputError(processor.rejectedUsage(), err))
				return
			}
		}
	}
}

// closeProviderStream closes the AWS event stream once and returns the same
// cleanup result to the pump and every caller of Close.
func (s *bedrockStreamer) closeProviderStream() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.stream.Close()
	})
	return s.closeErr
}

// outputError preserves provider and caller flow-control errors while marking
// malformed Bedrock stream data as output validation.
func (s *bedrockStreamer) outputError(usage model.TokenUsage, err error) error {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, ok := model.AsProviderError(err); ok {
		return err
	}
	var validationErr *model.OutputValidationError
	if errors.As(err, &validationErr) {
		return err
	}
	return s.contract.RejectProviderOutput(&usage, err)
}

func (s *bedrockStreamer) emitChunk(chunk model.Chunk) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		return nil
	}
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

// chunkProcessor converts Bedrock streaming events into model.Chunks. It
// stamps model attribution onto usage chunks using the resolved model ID and
// class provided at construction.
type chunkProcessor struct {
	emit func(model.Chunk) error

	toolBlocks map[int]*toolBuffer
	completion *completionBuffer
	// reasoningBlocks accumulates reasoning content per content index until stop.
	reasoningBlocks  map[int]*reasoningBuffer
	reasoningIndexes map[int]int
	nextReasoning    int
	textBlocks       map[int]*strings.Builder
	citationBlocks   map[int][]model.Citation
	canonicalParts   map[int]model.Part
	openBlocks       map[int]struct{}

	toolNameMap      map[string]string
	modelID          string
	modelClass       model.ModelClass
	output           *model.StructuredOutput
	toolFallbackName string

	canonical       model.Response
	started         bool
	complete        bool
	terminalEmitted bool
	retainedBytes   int
	retainedValues  int
}

// newChunkProcessor builds the stream event decoder. toolFallbackName is
// non-empty only when the request encoded structured output as a forced tool
// call (see structuredOutputUsesToolFallback); the processor then routes that
// one tool_use content block through the completion buffer instead of
// emitting a ToolCallChunk.
func newChunkProcessor(
	emit func(model.Chunk) error,
	nameMap map[string]string,
	modelID string,
	modelClass model.ModelClass,
	output *model.StructuredOutput,
	toolFallbackName string,
) *chunkProcessor {
	return &chunkProcessor{
		emit:             emit,
		toolBlocks:       make(map[int]*toolBuffer),
		reasoningBlocks:  make(map[int]*reasoningBuffer),
		reasoningIndexes: make(map[int]int),
		textBlocks:       make(map[int]*strings.Builder),
		citationBlocks:   make(map[int][]model.Citation),
		canonicalParts:   make(map[int]model.Part),
		openBlocks:       make(map[int]struct{}),
		toolNameMap:      nameMap,
		modelID:          modelID,
		modelClass:       modelClass,
		output:           output,
		toolFallbackName: toolFallbackName,
	}
}

func (p *chunkProcessor) Handle(event any) error {
	switch ev := event.(type) {
	case *brtypes.ConverseStreamOutputMemberMessageStart:
		if p.started {
			return errors.New("bedrock stream: duplicate message start")
		}
		p.toolBlocks = make(map[int]*toolBuffer)
		p.reasoningBlocks = make(map[int]*reasoningBuffer)
		p.reasoningIndexes = make(map[int]int)
		p.nextReasoning = 0
		p.textBlocks = make(map[int]*strings.Builder)
		p.citationBlocks = make(map[int][]model.Citation)
		p.canonicalParts = make(map[int]model.Part)
		p.openBlocks = make(map[int]struct{})
		p.completion = nil
		p.canonical = model.Response{}
		p.started = true
		p.complete = false
		p.terminalEmitted = false
		return nil
	case *brtypes.ConverseStreamOutputMemberContentBlockStart:
		if !p.started || p.complete {
			return errors.New("bedrock stream: content block started outside an active message")
		}
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		if _, ok := p.openBlocks[idx]; ok {
			return fmt.Errorf("bedrock stream: duplicate content block start %d", idx)
		}
		p.openBlocks[idx] = struct{}{}
		if start := ev.Value.Start; start != nil {
			toolUse, ok := start.(*brtypes.ContentBlockStartMemberToolUse)
			if !ok {
				return fmt.Errorf("bedrock stream: unsupported content block start %T", start)
			}
			if toolUse.Value.ToolUseId == nil || *toolUse.Value.ToolUseId == "" {
				return fmt.Errorf("bedrock stream: tool use block missing tool_use_id")
			}
			id := *toolUse.Value.ToolUseId
			if toolUse.Value.Name == nil || *toolUse.Value.Name == "" {
				return fmt.Errorf("bedrock stream: tool use block %q missing name", id)
			}
			raw := *toolUse.Value.Name
			providerName := normalizeToolName(raw)
			name, ok := p.toolNameMap[providerName]
			if !ok {
				return fmt.Errorf(
					"bedrock stream: tool use block %q returned unadvertised name %q",
					id,
					raw,
				)
			}
			if p.output != nil {
				if p.toolFallbackName == "" || name != p.toolFallbackName {
					return fmt.Errorf(
						"bedrock stream: structured output %q emitted tool_use start",
						p.output.Name,
					)
				}
				if p.completion != nil {
					return fmt.Errorf(
						"bedrock stream: structured output %q spanned multiple content blocks (%d, %d)",
						p.output.Name,
						p.completion.index,
						idx,
					)
				}
				p.completion = &completionBuffer{name: p.output.Name, index: idx}
				return nil
			}
			p.toolBlocks[idx] = &toolBuffer{id: id, name: name}
			return nil
		}
		return nil
	case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
		if !p.started || p.complete {
			return errors.New("bedrock stream: content block delta received outside an active message")
		}
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		delta := ev.Value.Delta
		if _, ok := p.openBlocks[idx]; !ok {
			switch delta.(type) {
			case *brtypes.ContentBlockDeltaMemberText,
				*brtypes.ContentBlockDeltaMemberCitation,
				*brtypes.ContentBlockDeltaMemberReasoningContent:
				// Bedrock emits ContentBlockStart only for tool-use blocks.
				// Text, citation, and reasoning blocks begin with their first
				// delta and are still closed by ContentBlockStop.
				p.openBlocks[idx] = struct{}{}
			case *brtypes.ContentBlockDeltaMemberToolUse:
				return fmt.Errorf("bedrock stream: tool-use delta %d has no matching start", idx)
			}
		}
		switch delta := delta.(type) {
		case *brtypes.ContentBlockDeltaMemberText:
			if delta.Value == "" {
				return nil
			}
			if p.output != nil {
				return p.handleCompletionDelta(idx, delta.Value)
			}
			if err := p.retainString(delta.Value); err != nil {
				return err
			}
			buffer := p.textBlocks[idx]
			if buffer == nil {
				buffer = &strings.Builder{}
				p.textBlocks[idx] = buffer
			}
			buffer.WriteString(delta.Value)
			return p.emit(model.TextChunk{
				Message: model.Message{
					Role:  "assistant",
					Parts: []model.Part{model.TextPart{Text: delta.Value}},
					Meta:  map[string]any{"content_index": idx},
				},
			})
		case *brtypes.ContentBlockDeltaMemberCitation:
			citation, err := translateCitationDelta(delta.Value)
			if err != nil {
				return err
			}
			if err := p.retainCitation(citation); err != nil {
				return err
			}
			p.citationBlocks[idx] = append(p.citationBlocks[idx], citation)
			return nil
		case *brtypes.ContentBlockDeltaMemberReasoningContent:
			// Initialize/lookup buffer for this content index.
			rb := p.reasoningBlocks[idx]
			if rb == nil {
				rb = &reasoningBuffer{}
				p.reasoningBlocks[idx] = rb
				p.reasoningIndexes[idx] = p.nextReasoning
				p.nextReasoning++
			}
			reasoningIndex := p.reasoningIndexes[idx]
			// Capture reasoning deltas (text, redacted bytes, signature).
			switch v := delta.Value.(type) {
			case *brtypes.ReasoningContentBlockDeltaMemberText:
				if v.Value == "" {
					return nil
				}
				if err := p.retainString(v.Value); err != nil {
					return err
				}
				rb.text.WriteString(v.Value)
				// Stream incremental thinking text for UX; final part is emitted on stop.
				return p.emit(model.ThinkingChunk{
					Message: model.Message{
						Role: "assistant",
						Parts: []model.Part{model.ThinkingPart{
							Text:  v.Value,
							Index: reasoningIndex,
							Final: false,
						}},
					},
				})
			case *brtypes.ReasoningContentBlockDeltaMemberRedactedContent:
				if len(v.Value) > 0 {
					if err := p.retainBytes(len(v.Value)); err != nil {
						return err
					}
					rb.redacted = append(rb.redacted, v.Value...)
				}
				return nil
			case *brtypes.ReasoningContentBlockDeltaMemberSignature:
				if v.Value != "" {
					if err := p.retainString(v.Value); err != nil {
						return err
					}
					rb.signature = v.Value
				}
				return nil
			default:
				return fmt.Errorf("bedrock stream: unsupported reasoning content delta %T", delta.Value)
			}
		case *brtypes.ContentBlockDeltaMemberToolUse:
			if p.output != nil {
				if p.completion == nil || p.completion.index != idx {
					return fmt.Errorf(
						"bedrock stream: structured output %q emitted tool_use delta",
						p.output.Name,
					)
				}
				if delta.Value.Input == nil || *delta.Value.Input == "" {
					return nil
				}
				return p.handleCompletionDelta(idx, *delta.Value.Input)
			}
			if tb := p.toolBlocks[idx]; tb != nil && delta.Value.Input != nil {
				fragment := *delta.Value.Input
				if fragment == "" {
					return nil
				}
				if err := p.retainString(fragment); err != nil {
					return err
				}
				tb.fragments.WriteString(fragment)
				if tb.id == "" {
					return fmt.Errorf("bedrock stream: tool JSON delta missing tool call id")
				}
				if tb.name == "" {
					return fmt.Errorf("bedrock stream: tool JSON delta missing tool name for id %q", tb.id)
				}
				return p.emit(model.ToolCallDeltaChunk{
					Delta: model.ToolCallDelta{
						Name:  tools.Ident(tb.name),
						ID:    tb.id,
						Delta: fragment,
					},
				})
			}
			return fmt.Errorf("bedrock stream: tool-use delta %d has no matching tool-use start", idx)
		default:
			return fmt.Errorf("bedrock stream: unsupported content block delta %T", delta)
		}
	case *brtypes.ConverseStreamOutputMemberContentBlockStop:
		if !p.started || p.complete {
			return errors.New("bedrock stream: content block stopped outside an active message")
		}
		idx, err := contentIndex(ev.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		if _, ok := p.openBlocks[idx]; !ok {
			return fmt.Errorf("bedrock stream: content block stop %d has no matching start", idx)
		}
		delete(p.openBlocks, idx)
		if err := p.finalizeCompletion(idx); err != nil {
			return err
		}
		// Finalize any reasoning block accumulated for this index.
		if rb := p.reasoningBlocks[idx]; rb != nil {
			delete(p.reasoningBlocks, idx)
			part, err := rb.finalize()
			if err != nil {
				return fmt.Errorf("bedrock stream: finalize reasoning block %d: %w", idx, err)
			}
			if part != nil {
				part.Index = p.reasoningIndexes[idx]
				part.Final = true
				p.canonicalParts[idx] = *part
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
		if tb := p.toolBlocks[idx]; tb != nil {
			payload, err := decodeToolPayload(tb.finalInput())
			if err != nil {
				return fmt.Errorf("bedrock stream: finalize tool payload %q: %w", tb.id, err)
			}
			delete(p.toolBlocks, idx)
			call := model.ToolCall{
				Name:    tools.Ident(tb.name),
				Payload: payload,
				ID:      tb.id,
			}
			p.canonicalParts[idx] = model.ToolUsePart{
				Name:  string(call.Name),
				Input: call.Payload,
				ID:    call.ID,
			}
			return p.emit(model.ToolCallChunk{
				ToolCall: call,
			})
		}
		if text := p.textBlocks[idx]; text != nil && p.output == nil {
			if citations := p.citationBlocks[idx]; len(citations) > 0 {
				p.canonicalParts[idx] = model.CitationsPart{
					Text:      text.String(),
					Citations: append([]model.Citation(nil), citations...),
				}
			} else {
				p.canonicalParts[idx] = model.TextPart{Text: text.String()}
			}
		}
		return nil
	case *brtypes.ConverseStreamOutputMemberMessageStop:
		if !p.started {
			// Bedrock intermittently stops a message it never started when the
			// model produces an empty completion (observed on Haiku). Classify
			// as a retryable empty stream instead of an opaque protocol error.
			return model.NewEmptyStreamError(
				bedrockProviderName,
				"converse_stream",
				"message stop received without an active message",
			)
		}
		if p.complete {
			return errors.New("bedrock stream: duplicate message stop")
		}
		if len(p.openBlocks) > 0 {
			return fmt.Errorf("bedrock stream: message stopped with %d open content blocks", len(p.openBlocks))
		}
		if ev.Value.StopReason == "" {
			return errors.New("bedrock stream: message stopped without a stop reason")
		}
		p.canonical.StopReason = string(ev.Value.StopReason)
		p.complete = true
		return nil
	case *brtypes.ConverseStreamOutputMemberMetadata:
		if !p.started || !p.complete || p.terminalEmitted {
			return errors.New("bedrock stream: metadata received outside a completed message")
		}
		if ev.Value.Usage == nil {
			return p.finishStream()
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
			Model:            p.modelID,
			ModelClass:       p.modelClass,
			InputTokens:      in,
			OutputTokens:     out,
			TotalTokens:      tot,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		}
		p.canonical.Usage = usage
		if err := p.emit(model.UsageChunk{Usage: usage}); err != nil {
			return err
		}
		return p.finishStream()
	default:
		return fmt.Errorf("bedrock stream: unsupported event %T", event)
	}
}

// rejectedUsage returns scalar usage with resolved identity even when content
// translation fails before the provider emits metadata.
func (p *chunkProcessor) rejectedUsage() model.TokenUsage {
	usage := p.canonical.Usage
	if usage.Model == "" {
		usage.Model = p.modelID
	}
	if usage.ModelClass == "" {
		usage.ModelClass = p.modelClass
	}
	return usage
}

// response returns the canonical provider response assembled from the completed
// stream. The provider event loop calls it only after message stop.
func (p *chunkProcessor) response() *model.Response {
	response := p.canonical
	if len(p.canonicalParts) > 0 {
		indices := make([]int, 0, len(p.canonicalParts))
		for index := range p.canonicalParts {
			indices = append(indices, index)
		}
		slices.Sort(indices)
		parts := make([]model.Part, 0, len(indices))
		for _, index := range indices {
			parts = append(parts, p.canonicalParts[index])
		}
		response.Content = []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: parts,
		}}
	}
	return &response
}

// finishStream emits the terminal stop after all provider metadata so no
// presentation or accounting chunk can follow the stop boundary.
func (p *chunkProcessor) finishStream() error {
	if p.terminalEmitted {
		return nil
	}
	if !p.complete || p.canonical.StopReason == "" {
		return errors.New("bedrock stream: cannot finish before message stop")
	}
	p.terminalEmitted = true
	return p.emit(model.StopChunk{Reason: p.canonical.StopReason})
}

type toolBuffer struct {
	name      string
	id        string
	fragments strings.Builder
}

// completionBuffer accumulates one structured-output content block until the
// provider closes it and the adapter can emit the canonical completion chunk.
type completionBuffer struct {
	name      string
	index     int
	fragments strings.Builder
}

func (tb *toolBuffer) finalInput() string {
	return tb.fragments.String()
}

// finalPayload returns the canonical JSON payload for the structured completion
// block. Unlike tool payloads, typed completions do not use fallbacks: invalid
// or empty JSON is a hard provider contract violation.
func (cb *completionBuffer) finalPayload() (rawjson.Message, error) {
	data := []byte(cb.fragments.String())
	if !json.Valid(data) {
		return nil, errors.New("structured completion payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

// handleCompletionDelta records and emits one structured-output preview
// fragment for the currently open Bedrock content block.
func (p *chunkProcessor) handleCompletionDelta(idx int, delta string) error {
	if p.output == nil {
		return errors.New("bedrock stream: completion delta requested without structured output")
	}
	if p.completion == nil {
		p.completion = &completionBuffer{
			name:  p.output.Name,
			index: idx,
		}
	}
	if p.completion.index != idx {
		return fmt.Errorf(
			"bedrock stream: structured output %q spanned multiple content blocks (%d, %d)",
			p.output.Name,
			p.completion.index,
			idx,
		)
	}
	if err := p.retainString(delta); err != nil {
		return err
	}
	p.completion.fragments.WriteString(delta)
	if p.toolFallbackName != "" {
		// Synthetic-tool fragments contain Bedrock's private object wrapper.
		// Completion previews are optional, so do not expose that provider
		// representation through the provider-neutral stream.
		return nil
	}
	return p.emit(model.CompletionDeltaChunk{
		Delta: model.CompletionDelta{
			Name:  p.completion.name,
			Delta: delta,
		},
	})
}

// finalizeCompletion emits the canonical structured completion payload for the
// given content block index when one is buffered there.
func (p *chunkProcessor) finalizeCompletion(idx int) error {
	if p.completion == nil || p.completion.index != idx {
		return nil
	}
	payload, err := p.completion.finalPayload()
	if err != nil {
		return fmt.Errorf("bedrock stream: structured output %q: %w", p.output.Name, err)
	}
	if p.toolFallbackName != "" {
		payload, err = unwrapStructuredOutputValue(payload)
		if err != nil {
			return fmt.Errorf(
				"bedrock stream: structured output tool fallback %q: %w",
				p.toolFallbackName,
				err,
			)
		}
	}
	completion := p.completion
	p.completion = nil
	p.canonicalParts[idx] = model.TextPart{Text: string(payload)}
	return p.emit(model.CompletionChunk{
		Completion: model.Completion{
			Name:    completion.name,
			Payload: payload,
		},
	})
}

func contentIndex(idx *int32) (int, error) {
	if idx == nil {
		return 0, fmt.Errorf("bedrock: content block index missing")
	}
	return int(*idx), nil
}

func decodeToolPayload(raw string) (rawjson.Message, error) {
	data := []byte(raw)
	if !json.Valid(data) {
		return nil, errors.New("tool payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

// retainString charges one provider string before private stream state grows.
func (p *chunkProcessor) retainString(value string) error {
	return p.retainBytes(len(value))
}

// retainValue charges one provider-controlled retained element.
func (p *chunkProcessor) retainValue() error {
	if p.retainedValues >= 100_000 {
		return errors.New("bedrock stream: retained output exceeds 100000 values")
	}
	p.retainedValues++
	return nil
}

// retainBytes charges one provider-controlled value and its bytes.
func (p *chunkProcessor) retainBytes(size int) error {
	if err := p.retainValue(); err != nil {
		return err
	}
	if size > 16<<20-p.retainedBytes {
		return errors.New("bedrock stream: retained output exceeds 16777216 bytes")
	}
	p.retainedBytes += size
	return nil
}

// retainCitation charges every provider-controlled value copied into one
// canonical citation before the citation slice grows.
func (p *chunkProcessor) retainCitation(citation model.Citation) error {
	if err := p.retainValue(); err != nil {
		return err
	}
	if err := p.retainString(citation.Title); err != nil {
		return err
	}
	if err := p.retainString(citation.Source); err != nil {
		return err
	}
	for _, content := range citation.SourceContent {
		if err := p.retainString(content); err != nil {
			return err
		}
	}
	if citation.Location.DocumentChar != nil {
		if err := p.retainValue(); err != nil {
			return err
		}
	}
	if citation.Location.DocumentChunk != nil {
		if err := p.retainValue(); err != nil {
			return err
		}
	}
	if citation.Location.DocumentPage != nil {
		if err := p.retainValue(); err != nil {
			return err
		}
	}
	return nil
}

func translateCitationDelta(delta brtypes.CitationsDelta) (model.Citation, error) {
	location, err := translateCitationLocationDelta(delta.Location)
	if err != nil {
		return model.Citation{}, err
	}
	out := model.Citation{
		Location:      location,
		SourceContent: translateCitationSourceContentDelta(delta.SourceContent),
	}
	if delta.Title != nil {
		out.Title = *delta.Title
	}
	if delta.Source != nil {
		out.Source = *delta.Source
	}
	return out, nil
}

func translateCitationLocationDelta(loc brtypes.CitationLocation) (model.CitationLocation, error) {
	switch v := loc.(type) {
	case *brtypes.CitationLocationMemberDocumentChar:
		return model.CitationLocation{
			DocumentChar: &model.DocumentCharLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}, nil
	case *brtypes.CitationLocationMemberDocumentChunk:
		return model.CitationLocation{
			DocumentChunk: &model.DocumentChunkLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}, nil
	case *brtypes.CitationLocationMemberDocumentPage:
		return model.CitationLocation{
			DocumentPage: &model.DocumentPageLocation{
				DocumentIndex: int32Value(v.Value.DocumentIndex),
				Start:         int32Value(v.Value.Start),
				End:           int32Value(v.Value.End),
			},
		}, nil
	default:
		return model.CitationLocation{}, fmt.Errorf("bedrock stream: unsupported citation location %T", loc)
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

func (rb *reasoningBuffer) finalize() (*model.ThinkingPart, error) {
	text := rb.text.String()
	if len(rb.redacted) > 0 {
		if text != "" || rb.signature != "" {
			return nil, errors.New("reasoning block contains both redacted and plaintext content")
		}
		return &model.ThinkingPart{Redacted: append([]byte(nil), rb.redacted...)}, nil
	}
	if text == "" && rb.signature == "" {
		return nil, nil
	}
	if rb.signature == "" {
		return nil, errors.New("reasoning plaintext is missing provider signature")
	}
	// A signature with empty text is canonical provider output, not an
	// anomaly: Opus 4.8-class models with thinking display "omitted" (the
	// default) emit signed thinking blocks whose plaintext is withheld. The
	// signed empty-text part must be preserved verbatim so transcript replay
	// can echo it back to the provider unchanged.
	return &model.ThinkingPart{
		Text:      text,
		Signature: rb.signature,
	}, nil
}
