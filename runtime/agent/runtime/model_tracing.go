// Package runtime records model client spans and stream lifecycle telemetry.
//
// Contract:
//   - Each complete or stream request owns exactly one client span.
//   - Unary responses have already passed the inner model-invocation boundary;
//     tracing observes them without claiming or repeating validation.
//   - Stream spans aggregate token usage across chunks and end at most once.
//   - A non-nil error marks the span failed only when telemetry classifies it as
//     a real operation failure instead of a context-driven termination.
package runtime

import (
	"context"
	"io"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	tracedProvider struct {
		inner  model.Provider
		tracer telemetry.Tracer
		logger telemetry.Logger

		modelID         string
		genAI           telemetry.GenAIContext
		captureMessages bool
	}

	tracedCall struct {
		provider   *tracedProvider
		span       telemetry.Span
		ctx        context.Context
		startedAt  time.Time
		finishOnce sync.Once
	}

	tracedStream struct {
		span telemetry.Span
		ctx  context.Context

		mu       sync.Mutex
		usage    model.TokenUsage
		response *model.Response

		startedAt          time.Time
		firstChunkRecorded bool
		sawUsageDelta      bool
		captureMessages    bool
		statusOnce         sync.Once
	}
)

func newTracedClient(inner model.Client, tracer telemetry.Tracer, logger telemetry.Logger, modelID string, genAI telemetry.GenAIContext, captureMessages bool) model.Client {
	if tracer == nil {
		tracer = telemetry.NewNoopTracer()
	}
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	return mustWrapModelClient(inner, func(provider model.Provider) model.Provider {
		return &tracedProvider{
			inner:  provider,
			tracer: tracer,
			logger: logger,

			modelID:         modelID,
			genAI:           genAI,
			captureMessages: captureMessages,
		}
	})
}

// PrepareClientCall starts the client span before provider execution. The
// returned observer closes it after canonical output validation.
//
//nolint:unparam // The observer interface permits setup failures used by journaling middleware.
func (c *tracedProvider) PrepareClientCall(
	ctx context.Context,
	req *model.Request,
) (context.Context, model.ClientCallObserver, error) {
	startedAt := time.Now()
	ctx = telemetry.WithGenAIContext(ctx, c.genAI)
	ctx, span := c.tracer.Start(
		ctx,
		modelSpanName(req),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(modelSpanAttrs(ctx, req)...),
	)
	c.recordInputMessages(span, req.Messages)
	return ctx, &tracedCall{
		provider:  c,
		span:      span,
		ctx:       ctx,
		startedAt: startedAt,
	}, nil
}

// Complete forwards the translated raw response beneath final validation.
func (c *tracedProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	return c.inner.Complete(ctx, req)
}

// Stream forwards the translated raw stream beneath final validation.
func (c *tracedProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	return c.inner.Stream(ctx, req)
}

// ObserveClientComplete records one final validated unary result.
func (c *tracedCall) ObserveClientComplete(resp *model.Response, err error) error {
	if err != nil {
		c.span.SetAttributes(outputValidationAttrs(err)...)
		if !telemetry.ShouldRecordSpanError(c.ctx, err) {
			c.span.SetStatus(codes.Unset, "")
			return nil
		}
		c.span.RecordError(err)
		c.span.SetStatus(codes.Error, "model complete failed")
		c.provider.logger.Error(
			c.ctx,
			"model complete failed",
			"model_id", c.provider.modelID,
			"err", err,
		)
		return nil
	}
	if (resp.Usage != model.TokenUsage{}) {
		c.span.SetAttributes(modelUsageAttrs(resp.Usage)...)
	}
	if resp.StopReason != "" {
		c.span.SetAttributes(telemetry.AttrGenAIResponseFinishReasons.StringSlice([]string{resp.StopReason}))
	}
	c.provider.recordOutputMessages(c.span, responseOutputMessages(resp), resp.StopReason)
	c.span.SetStatus(codes.Ok, "ok")
	return nil
}

// ObserveClientStream returns tracing behavior for a canonically validated
// stream. The model client attaches it to the exact stream.
func (c *tracedCall) ObserveClientStream(err error) (model.StreamObserver, error) {
	if err != nil {
		c.span.SetAttributes(outputValidationAttrs(err)...)
		if !telemetry.ShouldRecordSpanError(c.ctx, err) {
			c.span.SetStatus(codes.Unset, "")
			return nil, nil
		}
		c.span.RecordError(err)
		c.span.SetStatus(codes.Error, "model stream failed")
		c.provider.logger.Error(
			c.ctx,
			"model stream failed",
			"model_id", c.provider.modelID,
			"err", err,
		)
		return nil, nil
	}
	return &tracedStream{
		span:            c.span,
		ctx:             c.ctx,
		startedAt:       c.startedAt,
		captureMessages: c.provider.captureMessages,
	}, nil
}

// Finish ends the span after unary observation, failed stream setup, or the
// exact validated stream's close callback.
func (c *tracedCall) Finish(error) error {
	c.finishOnce.Do(func() {
		c.span.End()
	})
	return nil
}

// Abort records a later observer's preparation failure and ends the span.
func (c *tracedCall) Abort(err error) error {
	if err != nil {
		if !telemetry.ShouldRecordSpanError(c.ctx, err) {
			c.span.SetStatus(codes.Unset, "")
		} else {
			c.span.RecordError(err)
			c.span.SetStatus(codes.Error, "model observer setup failed")
		}
	}
	return c.Finish(err)
}

// ObserveStreamRecv records timing, usage, and span outcome from one exact
// validated stream result.
func (s *tracedStream) ObserveStreamRecv(observation model.StreamObservation) error {
	err := observation.Err
	if err != nil {
		// Only literal EOF completes a model stream. A wrapped EOF reports the
		// provider failure that added the wrapper.
		//nolint:errorlint // Exact equality is required by the model stream contract.
		if err == io.EOF {
			s.mu.Lock()
			if !s.sawUsageDelta {
				s.usage = observation.Response.Usage
			}
			s.response = observation.Response
			s.mu.Unlock()
			s.end(codes.Ok, "eof")
			return nil
		}
		if !telemetry.ShouldRecordSpanError(s.ctx, err) {
			s.end(codes.Unset, "")
			return nil
		}
		s.span.SetAttributes(outputValidationAttrs(err)...)
		s.span.RecordError(err)
		s.end(codes.Error, "stream recv failed")
		return nil
	}
	if usage, ok := observation.Chunk.(model.UsageChunk); ok {
		s.mu.Lock()
		s.sawUsageDelta = true
		if s.usage.Model == "" {
			s.usage.Model = usage.Usage.Model
		}
		if s.usage.ModelClass == "" {
			s.usage.ModelClass = usage.Usage.ModelClass
		}
		s.usage.InputTokens += usage.Usage.InputTokens
		s.usage.OutputTokens += usage.Usage.OutputTokens
		s.usage.TotalTokens += usage.Usage.TotalTokens
		s.usage.CacheReadTokens += usage.Usage.CacheReadTokens
		s.usage.CacheWriteTokens += usage.Usage.CacheWriteTokens
		s.mu.Unlock()
	}
	if isFirstGenAIOutputChunk(observation.Chunk.Kind()) {
		s.recordFirstChunk()
	}
	if stop, ok := observation.Chunk.(model.StopChunk); ok && stop.Reason != "" {
		s.span.SetAttributes(telemetry.AttrGenAIResponseFinishReasons.StringSlice([]string{stop.Reason}))
	}
	return nil
}

// ObserveStreamClose records the result after the exact validated stream has
// released its provider resources.
func (s *tracedStream) ObserveStreamClose(err error) error {
	if err != nil {
		if !telemetry.ShouldRecordSpanError(s.ctx, err) {
			s.end(codes.Unset, "")
			return nil
		}
		s.span.RecordError(err)
		s.end(codes.Error, "stream close failed")
		return nil
	}
	s.end(codes.Ok, "closed")
	return nil
}

func (s *tracedStream) end(code codes.Code, desc string) {
	s.statusOnce.Do(func() {
		s.mu.Lock()
		usage := s.usage
		s.mu.Unlock()

		if (usage != model.TokenUsage{}) {
			s.span.SetAttributes(modelUsageAttrs(usage)...)
		}
		if response := s.response; s.captureMessages && response != nil {
			s.applyOutputMessages(response.Content, response.StopReason)
		}
		s.span.SetStatus(code, desc)
	})
}

func modelSpanAttrs(ctx context.Context, req *model.Request) []attribute.KeyValue {
	attrs := telemetry.GenAIOperationAttrs(ctx, telemetry.GenAIOperationChat)
	attrs = append(attrs, telemetry.AttrGenAIRequestModel.String(requestedModelName(req)))
	if req.MaxTokens > 0 {
		attrs = append(attrs, telemetry.AttrGenAIRequestMaxTokens.Int(req.MaxTokens))
	}
	return attrs
}

func modelSpanName(req *model.Request) string {
	return telemetry.GenAIOperationChat + " " + requestedModelName(req)
}

func modelUsageAttrs(usage model.TokenUsage) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if usage.Model != "" {
		attrs = append(attrs, telemetry.AttrGenAIResponseModel.String(usage.Model))
	}
	if hasTokenUsageCounts(usage) {
		attrs = append(attrs, telemetry.GenAIUsageAttrs(
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheWriteTokens,
		)...)
	}
	return attrs
}

// outputValidationAttrs exposes only the closed response category. The error
// summary, rejected output, provider cause, tool details, and schema paths are
// deliberately absent.
func outputValidationAttrs(err error) []attribute.KeyValue {
	outputErr, ok := exactModelOutputValidation(err)
	if !ok {
		return nil
	}
	return []attribute.KeyValue{
		attribute.String("gen_ai.response.validation.kind", string(outputErr.Kind())),
	}
}

func requestedModelName(req *model.Request) string {
	if req.Model != "" {
		return req.Model
	}
	if req.ModelClass != "" {
		return string(req.ModelClass)
	}
	panic("runtime: model request must set Model or ModelClass for GenAI tracing")
}

func hasTokenUsageCounts(usage model.TokenUsage) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.CacheReadTokens != 0 ||
		usage.CacheWriteTokens != 0
}

func (s *tracedStream) recordFirstChunk() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstChunkRecorded {
		return
	}
	s.firstChunkRecorded = true
	s.span.SetAttributes(telemetry.AttrGenAIResponseTTFT.Float64(time.Since(s.startedAt).Seconds()))
}

func isFirstGenAIOutputChunk(chunkType string) bool {
	switch chunkType {
	case model.ChunkTypeText,
		model.ChunkTypeThinking,
		model.ChunkTypeToolCall,
		model.ChunkTypeToolCallDelta,
		model.ChunkTypeCompletion,
		model.ChunkTypeCompletionDelta:
		return true
	default:
		return false
	}
}

// recordInputMessages stamps the chat-turn span with the provider-ready input
// transcript when sensitive GenAI message capture is enabled.
func (c *tracedProvider) recordInputMessages(span telemetry.Span, messages []*model.Message) {
	if !c.captureMessages {
		return
	}
	attr, ok, err := telemetry.GenAIInputMessagesAttr(messages)
	setGenAIMessagesAttr(span, attr, ok, err, "input")
}

// recordOutputMessages stamps the chat-turn span with the complete non-streaming
// assistant response when sensitive GenAI message capture is enabled.
func (c *tracedProvider) recordOutputMessages(span telemetry.Span, messages []model.Message, stopReason string) {
	if !c.captureMessages {
		return
	}
	attr, ok, err := telemetry.GenAIOutputMessagesAttr(messages, stopReason)
	setGenAIMessagesAttr(span, attr, ok, err, "output")
}

// applyOutputMessages stamps the chat-turn span with the validated complete
// provider response.
func (s *tracedStream) applyOutputMessages(messages []model.Message, stopReason string) {
	attr, ok, err := telemetry.GenAIOutputMessagesAttr(messages, stopReason)
	setGenAIMessagesAttr(s.span, attr, ok, err, "output")
}

func setGenAIMessagesAttr(span telemetry.Span, attr attribute.KeyValue, ok bool, err error, direction string) {
	if err != nil {
		span.AddEvent("gen_ai.messages_serialize_failed",
			"gen_ai.message.direction", direction,
			"exception.message", err.Error())
		return
	}
	if ok {
		span.SetAttributes(attr)
	}
}

// responseOutputMessages returns the canonical ordered provider response used
// by chat-turn spans.
func responseOutputMessages(resp *model.Response) []model.Message {
	return resp.Content
}
