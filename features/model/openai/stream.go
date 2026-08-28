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
		ctx      context.Context
		cancel   context.CancelFunc
		stream   responseStream
		contract *model.RequestContract

		chunks chan model.Chunk
		done   chan struct{}

		errMu    sync.Mutex
		errSet   bool
		finalErr error

		closeOnce sync.Once
		closeErr  error

		responseMu    sync.RWMutex
		response      *model.Response
		rejectedUsage model.TokenUsage
	}

	// openAIChunkProcessor converts streamed OpenAI events into provider-neutral
	// model chunks.
	openAIChunkProcessor struct {
		emit           func(model.Chunk) error
		recordResponse func(*model.Response)
		recordUsage    func(model.TokenUsage)

		toolCalls       map[string]*streamToolBuffer
		streamedCallIDs map[string]struct{}
		thinkingIndexes map[int]int
		nextThinking    int

		codec      *toolCodec
		modelID    string
		modelClass model.ModelClass
		output     *model.StructuredOutput
		projection *strictSchemaProjection

		completed      bool
		sawText        bool
		retainedBytes  int
		retainedValues int
	}

	streamToolBuffer struct {
		itemID       string
		callID       string
		name         tools.Ident
		providerName string
	}
)

func newOpenAIStreamer(
	ctx context.Context,
	stream responseStream,
	codec *toolCodec,
	modelID string,
	modelClass model.ModelClass,
	output *model.StructuredOutput,
	projection *strictSchemaProjection,
	contract *model.RequestContract,
) model.Streamer {
	cctx, cancel := context.WithCancel(ctx)
	streamer := &openAIStreamer{
		ctx:      cctx,
		cancel:   cancel,
		stream:   stream,
		contract: contract,
		chunks:   make(chan model.Chunk, 32),
		done:     make(chan struct{}),
		rejectedUsage: model.TokenUsage{
			Model:      modelID,
			ModelClass: modelClass,
		},
	}
	processor := &openAIChunkProcessor{
		emit:            streamer.emitChunk,
		recordResponse:  streamer.recordResponse,
		recordUsage:     streamer.recordUsage,
		toolCalls:       make(map[string]*streamToolBuffer),
		streamedCallIDs: make(map[string]struct{}),
		thinkingIndexes: make(map[int]int),
		codec:           codec,
		modelID:         modelID,
		modelClass:      modelClass,
		output:          output,
		projection:      projection,
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
	closeErr := s.closeProviderStream()
	<-s.done
	return closeErr
}

func (s *openAIStreamer) Response() *model.Response {
	s.responseMu.RLock()
	defer s.responseMu.RUnlock()
	return s.response
}

func (s *openAIStreamer) run(processor *openAIChunkProcessor) {
	defer close(s.chunks)
	defer close(s.done)
	defer func() {
		if err := s.closeProviderStream(); err != nil {
			s.setErr(err)
		}
	}()

	var rejected error
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
				s.setErr(s.outputError(errors.New("openai: stream ended before response.completed")))
				return
			}
			return
		}
		event := s.stream.Current()
		if rejected != nil {
			complete, err := processor.handleRejectedEvent(event)
			if err != nil {
				s.setErr(err)
				return
			}
			if complete {
				s.setErr(s.outputError(rejected))
				return
			}
			continue
		}
		if err := processor.Handle(event); err != nil {
			if _, ok := model.UnadvertisedToolName(err); ok {
				if processor.completed {
					s.setErr(s.outputError(err))
					return
				}
				rejected = err
				processor.discardSemanticOutput()
				continue
			}
			s.setErr(s.outputError(err))
			return
		}
	}
}

// closeProviderStream closes the Responses API stream once and returns the
// cached cleanup result to the pump and every caller of Close.
func (s *openAIStreamer) closeProviderStream() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.stream.Close()
	})
	return s.closeErr
}

// outputError preserves transport, cancellation, and provider failures while
// classifying provider-authored stream data that cannot be translated.
func (s *openAIStreamer) outputError(err error) error {
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
	usage := s.rejectedUsage
	return s.contract.RejectProviderOutput(&usage, err)
}

func (s *openAIStreamer) emitChunk(chunk model.Chunk) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.chunks <- chunk:
		return nil
	}
}

func (s *openAIStreamer) recordResponse(response *model.Response) {
	s.responseMu.Lock()
	s.response = response
	s.responseMu.Unlock()
}

// recordUsage retains validated scalar evidence before terminal content
// translation can fail.
func (s *openAIStreamer) recordUsage(usage model.TokenUsage) {
	s.responseMu.Lock()
	s.rejectedUsage = usage
	s.responseMu.Unlock()
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

// discardSemanticOutput stops chunks after an unadvertised tool name and drops
// provider content that cannot enter the accepted response.
func (p *openAIChunkProcessor) discardSemanticOutput() {
	clear(p.toolCalls)
	clear(p.streamedCallIDs)
	clear(p.thinkingIndexes)
	p.emit = func(model.Chunk) error {
		return nil
	}
}

// handleRejectedEvent ignores later content from a rejected response while
// preserving provider failures and recording usage from its terminal event.
// The boolean result reports that a clean completed or incomplete response was
// received.
func (p *openAIChunkProcessor) handleRejectedEvent(event responses.ResponseStreamEventUnion) (bool, error) {
	switch actual := event.AsAny().(type) {
	case responses.ResponseCompletedEvent:
		p.recordRejectedCompletion(actual.Response)
		return true, nil
	case responses.ResponseIncompleteEvent:
		p.recordRejectedCompletion(actual.Response)
		return true, nil
	case responses.ResponseFailedEvent:
		return false, providerErrorFromResponseFailure(
			"responses.stream",
			string(actual.Response.Error.Code),
			actual.Response.Error.Message,
			errors.New(actual.Response.Error.Message),
		)
	case responses.ResponseErrorEvent:
		return false, providerErrorFromResponseFailure(
			"responses.stream",
			actual.Code,
			actual.Message,
			errors.New(actual.Message),
		)
	case responses.ResponseOutputItemAddedEvent,
		responses.ResponseOutputItemDoneEvent,
		responses.ResponseFunctionCallArgumentsDeltaEvent,
		responses.ResponseTextDeltaEvent,
		responses.ResponseRefusalDeltaEvent,
		responses.ResponseReasoningSummaryTextDeltaEvent,
		responses.ResponseContentPartAddedEvent,
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
		return false, nil
	default:
		return false, fmt.Errorf("openai: unsupported stream event %q (%T)", event.Type, actual)
	}
}

// recordRejectedCompletion retains only model identity and cumulative usage
// from the terminal provider response.
func (p *openAIChunkProcessor) recordRejectedCompletion(resp responses.Response) {
	p.completed = true
	p.modelID = chooseModelID(resp.Model, p.modelID)
	p.recordUsage(translateUsage(resp.Usage, p.modelID, p.modelClass))
}

func (p *openAIChunkProcessor) registerOutputItem(item responses.ResponseOutputItemUnion) error {
	switch actual := item.AsAny().(type) {
	case responses.ResponseFunctionToolCall:
		buffer := p.toolCalls[actual.ID]
		if buffer == nil {
			if err := p.retain(actual.ID); err != nil {
				return err
			}
			buffer = &streamToolBuffer{itemID: actual.ID}
			p.toolCalls[actual.ID] = buffer
		}
		if actual.CallID != "" {
			if err := p.retain(actual.CallID); err != nil {
				return err
			}
			buffer.callID = actual.CallID
		}
		if actual.Name != "" {
			name, ok := p.codec.canonicalName(actual.Name)
			if !ok {
				return fmt.Errorf(
					"openai: translate streamed tool call: %w",
					model.NewUnadvertisedToolNameError(actual.Name),
				)
			}
			if err := p.retain(name); err != nil {
				return err
			}
			buffer.name = tools.Ident(name)
			buffer.providerName = actual.Name
		}
		return nil
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
		if err := p.retain(event.ItemID); err != nil {
			return err
		}
		buffer = &streamToolBuffer{itemID: event.ItemID}
		p.toolCalls[event.ItemID] = buffer
	}
	if event.Delta == "" {
		return nil
	}
	if err := p.retain(event.Delta); err != nil {
		return err
	}
	if buffer.callID == "" || buffer.name == "" || buffer.providerName == "" {
		return errors.New("openai: tool argument delta arrived before tool call identity")
	}
	if !p.codec.streamsCanonicalDeltas(buffer.providerName) {
		return nil
	}
	if err := p.emit(model.ToolCallDeltaChunk{
		Delta: model.ToolCallDelta{
			Name:  buffer.name,
			ID:    buffer.callID,
			Delta: event.Delta,
		},
	}); err != nil {
		return err
	}
	p.streamedCallIDs[buffer.callID] = struct{}{}
	return nil
}

func (p *openAIChunkProcessor) handleTextDelta(delta string, itemID string, outputIndex int64) error {
	if delta == "" {
		return nil
	}
	p.sawText = true
	if p.output != nil {
		// OpenAI can emit transport-only null members for canonical optional
		// fields. The complete document is normalized before it crosses the
		// model boundary, so partial JSON cannot be exposed as canonical deltas.
		return nil
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
	index, err := p.thinkingIndex(int(event.OutputIndex))
	if err != nil {
		return err
	}
	return p.emit(model.ThinkingChunk{
		Message: model.Message{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ThinkingPart{
				Text:  event.Delta,
				Index: index,
				Final: false,
			}},
			Meta: map[string]any{
				"item_id":      event.ItemID,
				"output_index": event.OutputIndex,
			},
		},
	})
}

// thinkingIndex maps OpenAI's position among all output items to the dense
// reasoning-block sequence used by the provider-neutral model contract.
func (p *openAIChunkProcessor) thinkingIndex(outputIndex int) (int, error) {
	if index, ok := p.thinkingIndexes[outputIndex]; ok {
		return index, nil
	}
	index := p.nextThinking
	if err := p.retain(""); err != nil {
		return 0, err
	}
	p.thinkingIndexes[outputIndex] = index
	p.nextThinking++
	return index, nil
}

// retain charges provider data before private stream maps or slices grow.
func (p *openAIChunkProcessor) retain(value string) error {
	if p.retainedValues >= 100_000 {
		return errors.New("openai: retained stream output exceeds 100000 values")
	}
	if len(value) > 16<<20-p.retainedBytes {
		return errors.New("openai: retained stream output exceeds 16777216 bytes")
	}
	p.retainedValues++
	p.retainedBytes += len(value)
	return nil
}

func (p *openAIChunkProcessor) handleCompleted(resp responses.Response) error {
	p.completed = true
	p.modelID = chooseModelID(resp.Model, p.modelID)
	p.recordUsage(translateUsage(resp.Usage, p.modelID, p.modelClass))
	translated, err := translateResponse(
		&resp,
		p.codec,
		p.modelID,
		p.modelClass,
		p.output,
		p.projection,
	)
	if err != nil {
		return err
	}
	if err := p.emitFinalThinking(translated.Content); err != nil {
		return err
	}
	if p.output != nil {
		payload, err := structuredOutputPayload(translated.Content, p.output, p.projection)
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
			if _, streamed := p.streamedCallIDs[call.ID]; !streamed {
				if err := p.emit(model.ToolCallDeltaChunk{
					Delta: model.ToolCallDelta{
						Name:  call.Name,
						ID:    call.ID,
						Delta: string(call.Payload),
					},
				}); err != nil {
					return err
				}
			}
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
		if err := p.emit(model.UsageChunk{
			Usage: translated.Usage,
		}); err != nil {
			return err
		}
	}
	if err := p.emit(model.StopChunk{
		Reason:        translated.StopReason,
		OutputLimited: translated.OutputLimited,
	}); err != nil {
		return err
	}
	p.recordResponse(translated)
	return nil
}

// emitFinalThinking closes every reasoning block with the canonical text and
// provider metadata stored in the complete response.
func (p *openAIChunkProcessor) emitFinalThinking(content []model.Message) error {
	for _, message := range content {
		for _, part := range message.Parts {
			thinking, ok := part.(model.ThinkingPart)
			if !ok {
				continue
			}
			if err := p.emit(model.ThinkingChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{thinking},
				},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func structuredOutputName(output *model.StructuredOutput) string {
	return output.Name
}
