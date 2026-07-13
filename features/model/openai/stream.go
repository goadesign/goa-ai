// Package openai adapts the OpenAI Responses API stream to the provider-neutral
// model.Streamer contract used by planners and runtimes.
package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/openai/openai-go/responses"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// openAIStreamer drains the provider stream on a background goroutine and
	// emits provider-neutral chunks through a buffered channel.
	openAIStreamer struct {
		ctx    context.Context
		cancel context.CancelFunc
		stream responseStream

		chunks chan model.Chunk

		errMu    sync.Mutex
		errSet   bool
		finalErr error

		metaMu   sync.RWMutex
		metadata map[string]any
		response *model.Response
	}

	// openAIChunkProcessor converts streamed OpenAI events into provider-neutral
	// model chunks.
	openAIChunkProcessor struct {
		emit           func(model.Chunk) error
		recordUsage    func(model.TokenUsage)
		recordResponse func(*model.Response)

		toolCalls map[string]*streamToolBuffer

		codec      *toolCodec
		modelID    string
		modelClass model.ModelClass
		output     *model.StructuredOutput

		completed bool
		sawText   bool
	}

	streamToolBuffer struct {
		itemID  string
		callID  string
		name    string
		pending []string
	}
)

func newOpenAIStreamer(
	ctx context.Context,
	stream responseStream,
	codec *toolCodec,
	modelID string,
	modelClass model.ModelClass,
	output *model.StructuredOutput,
) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	streamer := &openAIStreamer{
		ctx:    cctx,
		cancel: cancel,
		stream: stream,
		chunks: make(chan model.Chunk, 32),
	}
	processor := &openAIChunkProcessor{
		emit:           streamer.emitChunk,
		recordUsage:    streamer.recordUsage,
		recordResponse: streamer.recordResponse,
		toolCalls:      make(map[string]*streamToolBuffer),
		codec:          codec,
		modelID:        modelID,
		modelClass:     modelClass,
		output:         output,
	}
	go streamer.run(processor)
	return streamer
}

func (s *openAIStreamer) Recv() (model.Chunk, error) {
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

func (s *openAIStreamer) Close() error {
	s.cancel()
	if s.stream == nil {
		return nil
	}
	return s.stream.Close()
}

func (s *openAIStreamer) Response() *model.Response {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	return s.response
}

func (s *openAIStreamer) Metadata() map[string]any {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	if len(s.metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(s.metadata))
	for key, value := range s.metadata {
		out[key] = value
	}
	return out
}

func (s *openAIStreamer) run(processor *openAIChunkProcessor) {
	defer close(s.chunks)
	defer func() {
		if s.stream != nil {
			if err := s.stream.Close(); err != nil {
				s.setErr(err)
			}
		}
	}()

	for {
		select {
		case <-s.ctx.Done():
			s.setErr(s.ctx.Err())
			return
		default:
		}

		if !s.stream.Next() {
			err := s.stream.Err()
			if err != nil {
				s.setErr(wrapOpenAIError("responses.stream", err))
				return
			}
			if !processor.completed {
				s.setErr(errors.New("openai: stream ended before response.completed"))
				return
			}
			return
		}
		if err := processor.Handle(s.stream.Current()); err != nil {
			s.setErr(err)
			return
		}
	}
}

func (s *openAIStreamer) emitChunk(chunk model.Chunk) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		return nil
	}
}

func (s *openAIStreamer) recordUsage(usage model.TokenUsage) {
	s.metaMu.Lock()
	if s.metadata == nil {
		s.metadata = make(map[string]any)
	}
	s.metadata["usage"] = usage
	s.metaMu.Unlock()
}

func (s *openAIStreamer) recordResponse(response *model.Response) {
	s.metaMu.Lock()
	s.response = response
	s.metaMu.Unlock()
}

func (s *openAIStreamer) setErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.errSet {
		return
	}
	s.errSet = true
	s.finalErr = err
}

func (s *openAIStreamer) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.finalErr
}

func (p *openAIChunkProcessor) Handle(event responses.ResponseStreamEventUnion) error {
	if p.completed {
		return errors.New("openai: event received after response completion")
	}
	switch actual := event.AsAny().(type) {
	case responses.ResponseOutputItemAddedEvent:
		return p.registerOutputItem(actual.Item)
	case responses.ResponseOutputItemDoneEvent:
		return p.registerOutputItem(actual.Item)
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		return p.handleToolCallArgumentsDelta(actual)
	case responses.ResponseTextDeltaEvent:
		return p.handleTextDelta(actual.Delta, actual.ItemID, actual.OutputIndex)
	case responses.ResponseRefusalDeltaEvent:
		return p.handleTextDelta(actual.Delta, actual.ItemID, actual.OutputIndex)
	case responses.ResponseReasoningSummaryTextDeltaEvent:
		return p.handleThinkingDelta(actual)
	case responses.ResponseCompletedEvent:
		return p.handleCompleted(actual.Response)
	case responses.ResponseIncompleteEvent:
		return p.handleCompleted(actual.Response)
	case responses.ResponseFailedEvent:
		return providerErrorFromResponseFailure(
			"responses.stream",
			string(actual.Response.Error.Code),
			actual.Response.Error.Message,
			errors.New(actual.Response.Error.Message),
		)
	case responses.ResponseErrorEvent:
		return providerErrorFromResponseFailure(
			"responses.stream",
			actual.Code,
			actual.Message,
			errors.New(actual.Message),
		)
	case responses.ResponseContentPartAddedEvent,
		responses.ResponseContentPartDoneEvent,
		responses.ResponseCreatedEvent,
		responses.ResponseFunctionCallArgumentsDoneEvent,
		responses.ResponseInProgressEvent,
		responses.ResponseOutputTextAnnotationAddedEvent,
		responses.ResponseTextDoneEvent,
		responses.ResponseQueuedEvent,
		responses.ResponseReasoningSummaryPartAddedEvent,
		responses.ResponseReasoningSummaryPartDoneEvent,
		responses.ResponseReasoningSummaryTextDoneEvent,
		responses.ResponseRefusalDoneEvent:
		return nil
	default:
		return fmt.Errorf("openai: unsupported stream event %q (%T)", event.Type, actual)
	}
}

func (p *openAIChunkProcessor) registerOutputItem(item responses.ResponseOutputItemUnion) error {
	switch actual := item.AsAny().(type) {
	case responses.ResponseFunctionToolCall:
		buffer := p.toolCalls[actual.ID]
		if buffer == nil {
			buffer = &streamToolBuffer{itemID: actual.ID}
			p.toolCalls[actual.ID] = buffer
		}
		if actual.CallID != "" {
			buffer.callID = actual.CallID
		}
		if actual.Name != "" {
			buffer.name = p.codec.canonicalName(actual.Name)
		}
		return p.flushPendingToolDeltas(buffer)
	default:
		return nil
	}
}

func (p *openAIChunkProcessor) handleToolCallArgumentsDelta(event responses.ResponseFunctionCallArgumentsDeltaEvent) error {
	if p.output != nil {
		return errors.New("openai: structured output emitted tool calls")
	}
	buffer := p.toolCalls[event.ItemID]
	if buffer == nil {
		buffer = &streamToolBuffer{itemID: event.ItemID}
		p.toolCalls[event.ItemID] = buffer
	}
	if buffer.callID == "" || buffer.name == "" {
		buffer.pending = append(buffer.pending, event.Delta)
		return nil
	}
	return p.emitToolCallDelta(buffer, event.Delta)
}

func (p *openAIChunkProcessor) flushPendingToolDeltas(buffer *streamToolBuffer) error {
	if buffer == nil || buffer.callID == "" || buffer.name == "" || len(buffer.pending) == 0 {
		return nil
	}
	for _, delta := range buffer.pending {
		if err := p.emitToolCallDelta(buffer, delta); err != nil {
			return err
		}
	}
	buffer.pending = nil
	return nil
}

func (p *openAIChunkProcessor) emitToolCallDelta(buffer *streamToolBuffer, delta string) error {
	if delta == "" {
		return nil
	}
	return p.emit(model.ToolCallDeltaChunk{
		Delta: model.ToolCallDelta{
			Name:  tools.Ident(buffer.name),
			ID:    buffer.callID,
			Delta: delta,
		},
	})
}

func (p *openAIChunkProcessor) handleTextDelta(delta string, itemID string, outputIndex int64) error {
	if delta == "" {
		return nil
	}
	p.sawText = true
	if p.output != nil {
		return p.emit(model.CompletionDeltaChunk{
			Delta: model.CompletionDelta{
				Name:  structuredOutputName(p.output),
				Delta: delta,
			},
		})
	}
	return p.emit(model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: delta}},
			Meta: map[string]any{
				"item_id":      itemID,
				"output_index": outputIndex,
			},
		},
	})
}

func (p *openAIChunkProcessor) handleThinkingDelta(event responses.ResponseReasoningSummaryTextDeltaEvent) error {
	if event.Delta == "" {
		return nil
	}
	return p.emit(model.ThinkingChunk{
		Message: model.Message{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ThinkingPart{
				Text:  event.Delta,
				Index: int(event.SummaryIndex),
				Final: false,
			}},
			Meta: map[string]any{
				"item_id":      event.ItemID,
				"output_index": event.OutputIndex,
			},
		},
	})
}

func (p *openAIChunkProcessor) handleCompleted(resp responses.Response) error {
	p.completed = true
	p.modelID = chooseModelID(resp.Model, p.modelID)
	translated, err := translateResponse(&resp, p.codec, p.modelID, p.modelClass, p.output)
	if err != nil {
		return err
	}
	if p.output != nil {
		payload, err := structuredOutputPayload(translated.Content, p.output)
		if err != nil {
			return err
		}
		if err := p.emit(model.CompletionChunk{
			Completion: model.Completion{
				Name:    structuredOutputName(p.output),
				Payload: payload,
			},
		}); err != nil {
			return err
		}
	} else {
		for _, call := range translated.ToolCalls() {
			if err := p.emit(model.ToolCallChunk{
				ToolCall: call,
			}); err != nil {
				return err
			}
		}
		if !p.sawText {
			if text := extractAssistantText(translated.Content); text != "" {
				if err := p.emit(model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: text}},
					},
				}); err != nil {
					return err
				}
			}
		}
	}
	if translated.Usage != (model.TokenUsage{}) {
		if p.recordUsage != nil {
			p.recordUsage(translated.Usage)
		}
		if err := p.emit(model.UsageChunk{
			Usage: translated.Usage,
		}); err != nil {
			return err
		}
	}
	if err := p.emit(model.StopChunk{
		Reason: translated.StopReason,
	}); err != nil {
		return err
	}
	p.recordResponse(translated)
	return nil
}

func structuredOutputName(output *model.StructuredOutput) string {
	if output == nil || output.Name == "" {
		return structuredOutputDefaultName
	}
	return output.Name
}
