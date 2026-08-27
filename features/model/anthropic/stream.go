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
	done   chan struct{}

	errMu    sync.Mutex
	errSet   bool
	finalErr error

	closeOnce sync.Once
	closeErr  error

	responseMu sync.RWMutex
	response   *model.Response

	toolNameMap     map[string]string
	noArgumentTools map[string]struct{}
	modelID         string
	modelClass      model.ModelClass
	output          *model.StructuredOutput
	contract        *model.RequestContract
}

func newAnthropicStreamer(
	ctx context.Context,
	stream *ssestream.Stream[sdk.MessageStreamEventUnion],
	nameMap map[string]string,
	noArgumentTools map[string]struct{},
	modelID string,
	modelClass model.ModelClass,
	output *model.StructuredOutput,
	contract *model.RequestContract,
) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	as := &anthropicStreamer{
		ctx:             cctx,
		cancel:          cancel,
		stream:          stream,
		chunks:          make(chan model.Chunk, 32),
		done:            make(chan struct{}),
		toolNameMap:     nameMap,
		noArgumentTools: noArgumentTools,
		modelID:         modelID,
		modelClass:      modelClass,
		output:          output,
		contract:        contract,
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
	closeErr := s.closeProviderStream()
	<-s.done
	return closeErr
}

func (s *anthropicStreamer) Response() *model.Response {
	s.responseMu.RLock()
	defer s.responseMu.RUnlock()
	return s.response
}

func (s *anthropicStreamer) run() {
	defer close(s.chunks)
	defer close(s.done)
	defer func() {
		if err := s.closeProviderStream(); err != nil {
			s.setErr(err)
		}
	}()

	processor := newAnthropicChunkProcessor(
		s.emitAttributedChunk,
		s.toolNameMap,
		s.noArgumentTools,
		s.output,
	)
	var response sdk.Message

	for {
		select {
		case <-s.ctx.Done():
			s.setErr(s.ctx.Err())
			return
		default:
		}
		if !s.stream.Next() {
			if err := s.stream.Err(); err != nil && !errors.Is(err, io.EOF) {
				s.setErr(wrapAnthropicError("stream_recv", err))
			} else if err := s.ctx.Err(); err != nil {
				s.setErr(err)
			} else if !processor.complete {
				s.setErr(model.NewStreamEndedEarlyError(
					anthropicProviderName,
					"stream_recv",
					processor.started,
				))
			} else {
				translated, err := translateResponse(&response, s.toolNameMap)
				if err != nil {
					s.setErr(s.outputError(processor, err))
					return
				}
				translated.Usage = processor.attributedUsage(s.modelID, s.modelClass)
				s.responseMu.Lock()
				s.response = translated
				s.responseMu.Unlock()
			}
			return
		}
		event := s.stream.Current()
		if err := processor.retainSDKSnapshot(event); err != nil {
			s.setErr(s.outputError(processor, err))
			return
		}
		if err := response.Accumulate(event); err != nil {
			s.setErr(s.outputError(processor, fmt.Errorf("anthropic: accumulate streamed response: %w", err)))
			return
		}
		if err := processor.Handle(event); err != nil {
			s.setErr(s.outputError(processor, err))
			return
		}
	}
}

// emitAttributedChunk attaches the invocation's resolved model identity to
// token deltas before callers compare them with the complete response.
func (s *anthropicStreamer) emitAttributedChunk(chunk model.Chunk) error {
	if usage, ok := chunk.(model.UsageChunk); ok {
		usage.Usage.Model = s.modelID
		usage.Usage.ModelClass = s.modelClass
		chunk = usage
	}
	return s.emitChunk(chunk)
}

// closeProviderStream closes the Anthropic SSE stream once and returns the
// cached cleanup result to the pump and every caller of Close.
func (s *anthropicStreamer) closeProviderStream() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.stream.Close()
	})
	return s.closeErr
}

// outputError preserves provider and caller flow-control errors while marking
// malformed Anthropic stream data as output validation.
func (s *anthropicStreamer) outputError(processor *anthropicChunkProcessor, err error) error {
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
	usage := processor.attributedUsage(s.modelID, s.modelClass)
	return s.contract.RejectProviderOutput(&usage, err)
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

	toolBlocks      map[int]*toolBuffer
	thinkingBlocks  map[int]*thinkingBuffer
	thinkingIndexes map[int]int
	nextThinking    int
	completion      *completionBuffer
	completionSeen  bool
	openBlocks      map[int]struct{}

	toolNameMap     map[string]string
	noArgumentTools map[string]struct{}
	output          *model.StructuredOutput

	stopReason     string
	usage          model.TokenUsage
	started        bool
	complete       bool
	retainedBytes  int
	retainedValues int
}

func newAnthropicChunkProcessor(
	emit func(model.Chunk) error,
	nameMap map[string]string,
	noArgumentTools map[string]struct{},
	output *model.StructuredOutput,
) *anthropicChunkProcessor {
	return &anthropicChunkProcessor{
		emit:            emit,
		toolBlocks:      make(map[int]*toolBuffer),
		thinkingBlocks:  make(map[int]*thinkingBuffer),
		thinkingIndexes: make(map[int]int),
		openBlocks:      make(map[int]struct{}),
		toolNameMap:     nameMap,
		noArgumentTools: noArgumentTools,
		output:          output,
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
		p.thinkingIndexes = make(map[int]int)
		p.nextThinking = 0
		p.completion = nil
		p.completionSeen = false
		p.openBlocks = make(map[int]struct{})
		p.stopReason = ""
		p.usage = anthropicUsage(ev.Message.Usage)
		p.started = true
		p.complete = false
		if p.usage == (model.TokenUsage{}) {
			return nil
		}
		return p.emit(model.UsageChunk{Usage: p.usage})
	case sdk.ContentBlockStartEvent:
		if !p.started || p.complete {
			return errors.New("anthropic stream: content block started outside an active message")
		}
		idx := int(ev.Index)
		if _, ok := p.openBlocks[idx]; ok {
			return fmt.Errorf("anthropic stream: duplicate content block start %d", idx)
		}
		if err := p.retain(""); err != nil {
			return err
		}
		p.openBlocks[idx] = struct{}{}
		start := ev.ContentBlock.AsAny()
		if text, ok := start.(sdk.TextBlock); ok {
			if p.output != nil {
				if p.completion != nil || p.completionSeen {
					return errors.New("anthropic stream: structured output contains multiple text blocks")
				}
				p.completion = &completionBuffer{
					name:  p.output.Name,
					index: idx,
				}
				return p.emitCompletionDelta(text.Text)
			}
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
			if err := p.retain(toolUse.ID); err != nil {
				return err
			}
			if err := p.retain(toolUse.Name); err != nil {
				return err
			}
			raw := toolUse.Name
			canonical, ok := p.toolNameMap[raw]
			if !ok {
				return fmt.Errorf(
					"anthropic stream: tool use block %q returned unadvertised name %q",
					toolUse.ID,
					raw,
				)
			}
			tb.name = canonical
			tb.id = toolUse.ID
			_, tb.noArguments = p.noArgumentTools[canonical]
			initialInput := string(toolUse.Input)
			if err := p.retain(initialInput); err != nil {
				return err
			}
			tb.initialEmptyObject = initialInput == "{}"
			tb.fragments.WriteString(initialInput)
			p.toolBlocks[idx] = tb
			return nil
		}
		if thinking, ok := start.(sdk.ThinkingBlock); ok {
			tb := &thinkingBuffer{signature: thinking.Signature}
			if err := p.retain(thinking.Thinking); err != nil {
				return err
			}
			if err := p.retain(thinking.Signature); err != nil {
				return err
			}
			tb.text.WriteString(thinking.Thinking)
			if err := p.retain(""); err != nil {
				return err
			}
			p.thinkingIndexes[idx] = p.nextThinking
			p.nextThinking++
			p.thinkingBlocks[idx] = tb
			return nil
		}
		if redacted, ok := start.(sdk.RedactedThinkingBlock); ok {
			if redacted.Data == "" {
				return errors.New("anthropic stream: redacted thinking block missing data")
			}
			if err := p.retain(redacted.Data); err != nil {
				return err
			}
			if err := p.retain(""); err != nil {
				return err
			}
			p.thinkingIndexes[idx] = p.nextThinking
			p.nextThinking++
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
			if p.output != nil {
				if p.completion == nil || p.completion.index != idx {
					return fmt.Errorf(
						"anthropic stream: structured output text delta %d has no matching text block",
						idx,
					)
				}
				return p.emitCompletionDelta(delta.Text)
			}
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
				if err := p.retain(delta.PartialJSON); err != nil {
					return err
				}
				if tb.noArguments {
					return nil
				}
				tb.appendFragment(delta.PartialJSON)
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
				if err := p.retain(""); err != nil {
					return err
				}
				tb = &thinkingBuffer{}
				p.thinkingBlocks[idx] = tb
				p.thinkingIndexes[idx] = p.nextThinking
				p.nextThinking++
			}
			if err := p.retain(delta.Thinking); err != nil {
				return err
			}
			tb.text.WriteString(delta.Thinking)
			return p.emit(model.ThinkingChunk{
				Message: model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.ThinkingPart{
							Text:  delta.Thinking,
							Index: p.thinkingIndexes[idx],
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
				if err := p.retain(""); err != nil {
					return err
				}
				tb = &thinkingBuffer{}
				p.thinkingBlocks[idx] = tb
				p.thinkingIndexes[idx] = p.nextThinking
				p.nextThinking++
			}
			if err := p.retain(delta.Signature); err != nil {
				return err
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
		if p.completion != nil && p.completion.index == idx {
			payload, err := p.completion.finalPayload()
			if err != nil {
				return fmt.Errorf(
					"anthropic stream: finalize structured output %q: %w",
					p.completion.name,
					err,
				)
			}
			completion := p.completion
			p.completion = nil
			p.completionSeen = true
			return p.emit(model.CompletionChunk{
				Completion: model.Completion{
					Name:    completion.name,
					Payload: payload,
				},
			})
		}
		if tb := p.thinkingBlocks[idx]; tb != nil {
			delete(p.thinkingBlocks, idx)
			part, err := tb.finalize(p.thinkingIndexes[idx])
			if err != nil {
				return fmt.Errorf("anthropic stream: finalize thinking block %d: %w", idx, err)
			}
			if part != nil {
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
			payload := rawjson.Message(`{}`)
			if !tb.noArguments {
				var err error
				payload, err = decodeToolPayload(tb.finalInput())
				if err != nil {
					return fmt.Errorf("anthropic stream: finalize tool payload %q: %w", tb.id, err)
				}
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
		// Anthropic sends cumulative usage but may omit fields that have not
		// changed. Keep the last value for each omitted field before computing
		// the newly consumed tokens emitted to callers.
		cumulative := p.usage
		if ev.Usage.JSON.InputTokens.Valid() {
			cumulative.InputTokens = int(ev.Usage.InputTokens)
		}
		if ev.Usage.JSON.OutputTokens.Valid() {
			cumulative.OutputTokens = int(ev.Usage.OutputTokens)
		}
		if ev.Usage.JSON.CacheReadInputTokens.Valid() {
			cumulative.CacheReadTokens = int(ev.Usage.CacheReadInputTokens)
		}
		if ev.Usage.JSON.CacheCreationInputTokens.Valid() {
			cumulative.CacheWriteTokens = int(ev.Usage.CacheCreationInputTokens)
		}
		cumulative.TotalTokens = cumulative.InputTokens + cumulative.OutputTokens
		delta, err := subtractAnthropicUsage(cumulative, p.usage)
		if err != nil {
			return err
		}
		p.usage = cumulative
		if delta == (model.TokenUsage{}) {
			return nil
		}
		return p.emit(model.UsageChunk{Usage: delta})
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
		if p.output != nil && !p.completionSeen {
			return fmt.Errorf(
				"anthropic stream: structured output %q did not produce a text block",
				p.output.Name,
			)
		}
		chunk := model.StopChunk{
			Reason:        p.stopReason,
			OutputLimited: anthropicOutputLimited(p.stopReason),
		}
		p.complete = true
		return p.emit(chunk)
	default:
		return fmt.Errorf("anthropic stream: unsupported event %T", ev)
	}
}

// anthropicUsage converts one cumulative Anthropic usage snapshot into the
// provider-neutral token fields validated by the model stream.
func anthropicUsage(usage sdk.Usage) model.TokenUsage {
	return model.TokenUsage{
		InputTokens:      int(usage.InputTokens),
		OutputTokens:     int(usage.OutputTokens),
		TotalTokens:      int(usage.InputTokens + usage.OutputTokens),
		CacheReadTokens:  int(usage.CacheReadInputTokens),
		CacheWriteTokens: int(usage.CacheCreationInputTokens),
	}
}

// subtractAnthropicUsage turns successive cumulative provider snapshots into
// non-negative deltas whose sum equals the terminal response usage.
func subtractAnthropicUsage(current, previous model.TokenUsage) (model.TokenUsage, error) {
	delta := model.TokenUsage{
		InputTokens:      current.InputTokens - previous.InputTokens,
		OutputTokens:     current.OutputTokens - previous.OutputTokens,
		TotalTokens:      current.TotalTokens - previous.TotalTokens,
		CacheReadTokens:  current.CacheReadTokens - previous.CacheReadTokens,
		CacheWriteTokens: current.CacheWriteTokens - previous.CacheWriteTokens,
	}
	if delta.InputTokens < 0 ||
		delta.OutputTokens < 0 ||
		delta.TotalTokens < 0 ||
		delta.CacheReadTokens < 0 ||
		delta.CacheWriteTokens < 0 {
		return model.TokenUsage{}, errors.New("anthropic stream: cumulative usage decreased")
	}
	return delta, nil
}

// attributedUsage returns the cumulative counters accepted by the stream
// processor with the model identity owned by this invocation.
func (p *anthropicChunkProcessor) attributedUsage(modelID string, modelClass model.ModelClass) model.TokenUsage {
	usage := p.usage
	usage.Model = modelID
	usage.ModelClass = modelClass
	return usage
}

type toolBuffer struct {
	name               string
	id                 string
	noArguments        bool
	initialEmptyObject bool
	deltaSeen          bool
	fragments          strings.Builder
}

// appendFragment replaces Anthropic's initial empty-object placeholder with
// the JSON deltas that contain the model-authored arguments.
func (tb *toolBuffer) appendFragment(fragment string) {
	if !tb.deltaSeen && tb.initialEmptyObject {
		tb.fragments.Reset()
	}
	tb.deltaSeen = true
	tb.fragments.WriteString(fragment)
}

// finalInput returns the initial input when Anthropic emitted no JSON deltas,
// or the complete delta document when the streamed arguments replaced it.
func (tb *toolBuffer) finalInput() string {
	return tb.fragments.String()
}

// completionBuffer retains the one text block Anthropic uses for native
// structured output until the complete JSON document can be validated.
type completionBuffer struct {
	name      string
	index     int
	fragments strings.Builder
}

// finalPayload returns the complete JSON document emitted for structured
// output. Invalid or incomplete JSON fails the stream.
func (cb *completionBuffer) finalPayload() (rawjson.Message, error) {
	data := []byte(cb.fragments.String())
	if !json.Valid(data) {
		return nil, errors.New("structured completion payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

type thinkingBuffer struct {
	text      strings.Builder
	signature string
	redacted  []byte
}

// emitCompletionDelta retains one native structured-output fragment and emits
// the same fragment as a preview for streaming callers.
func (p *anthropicChunkProcessor) emitCompletionDelta(delta string) error {
	if delta == "" {
		return nil
	}
	if p.completion == nil {
		return errors.New("anthropic stream: structured output fragment has no text block")
	}
	if err := p.retain(delta); err != nil {
		return err
	}
	p.completion.fragments.WriteString(delta)
	return p.emit(model.CompletionDeltaChunk{
		Delta: model.CompletionDelta{
			Name:  p.completion.name,
			Delta: delta,
		},
	})
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
	if tb.signature == "" {
		return nil, errors.New("thinking plaintext is missing provider signature")
	}
	// Signature with empty text is canonical output for thinking display
	// "omitted" (the Opus 4.8-class default); preserve it for verbatim replay.
	return &model.ThinkingPart{
		Text:      text,
		Signature: tb.signature,
		Index:     index,
		Final:     true,
	}, nil
}

func decodeToolPayload(raw string) (rawjson.Message, error) {
	data := []byte(raw)
	if !json.Valid(data) {
		return nil, errors.New("tool payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

// retain charges provider fragments before any private accumulator grows.
func (p *anthropicChunkProcessor) retain(value string) error {
	if p.retainedValues >= 100_000 {
		return errors.New("anthropic stream: retained output exceeds 100000 values")
	}
	if len(value) > 16<<20-p.retainedBytes {
		return errors.New("anthropic stream: retained output exceeds 16777216 bytes")
	}
	p.retainedValues++
	p.retainedBytes += len(value)
	return nil
}

// retainSDKSnapshot charges the provider event before the Anthropic SDK
// accumulator copies it into the final response snapshot.
func (p *anthropicChunkProcessor) retainSDKSnapshot(event sdk.MessageStreamEventUnion) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("anthropic stream: encode provider event for retention budget: %w", err)
	}
	if p.retainedValues >= 100_000 {
		return errors.New("anthropic stream: retained output exceeds 100000 values")
	}
	if len(data) > 16<<20-p.retainedBytes {
		return errors.New("anthropic stream: retained output exceeds 16777216 bytes")
	}
	p.retainedValues++
	p.retainedBytes += len(data)
	return nil
}
