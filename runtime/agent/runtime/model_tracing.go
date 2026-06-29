// Package runtime records model client spans and stream lifecycle telemetry.
//
// Contract:
//   - Each complete or stream request owns exactly one client span.
//   - Stream spans aggregate token usage across chunks and end at most once.
//   - A non-nil error marks the span failed only when telemetry classifies it as
//     a real operation failure instead of a context-driven termination.
package runtime

import (
	"context"
	"errors"
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
	tracedClient struct {
		inner  model.Client
		tracer telemetry.Tracer
		logger telemetry.Logger

		modelID         string
		genAI           telemetry.GenAIContext
		captureMessages bool
	}

	tracedStream struct {
		inner model.Streamer
		span  telemetry.Span
		ctx   context.Context

		mu    sync.Mutex
		usage model.TokenUsage

		// output accumulates streamed assistant parts so a single
		// gen_ai.output.messages attribute can be emitted at stream end.
		output *genAIStreamAccumulator

		startedAt          time.Time
		firstChunkRecorded bool
		sawUsageDelta      bool
		endOnce            sync.Once
	}
)

func newTracedClient(inner model.Client, tracer telemetry.Tracer, logger telemetry.Logger, modelID string, genAI telemetry.GenAIContext, captureMessages bool) model.Client {
	if inner == nil {
		return nil
	}
	if tracer == nil {
		tracer = telemetry.NewNoopTracer()
	}
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	return &tracedClient{
		inner:           inner,
		tracer:          tracer,
		logger:          logger,

		modelID:         modelID,
		genAI:           genAI,
		captureMessages: captureMessages,
	}
}

func (c *tracedClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	ctx = telemetry.WithGenAIContext(ctx, c.genAI)
	ctx, span := c.tracer.Start(
		ctx,
		modelSpanName(req),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(modelSpanAttrs(ctx, req)...),
	)
	defer span.End()
	c.recordInputMessages(span, req.Messages)

	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		if !telemetry.ShouldRecordSpanError(ctx, err) {
			span.SetStatus(codes.Unset, "")
			return resp, err
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "model complete failed")
		c.logger.Error(
			ctx,
			"model complete failed",
			"model_id", c.modelID,
			"err", err,
		)
		return resp, err
	}
	if (resp.Usage != model.TokenUsage{}) {
		span.SetAttributes(modelUsageAttrs(resp.Usage)...)
	}
	if resp.StopReason != "" {
		span.SetAttributes(telemetry.AttrGenAIResponseFinishReasons.StringSlice([]string{resp.StopReason}))
	}
	c.recordOutputMessages(span, resp.Content, resp.StopReason)
	span.SetStatus(codes.Ok, "ok")
	return resp, nil
}

func (c *tracedClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	startedAt := time.Now()
	ctx = telemetry.WithGenAIContext(ctx, c.genAI)
	ctx, span := c.tracer.Start(
		ctx,
		modelSpanName(req),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(modelSpanAttrs(ctx, req)...),
	)
	c.recordInputMessages(span, req.Messages)

	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		if !telemetry.ShouldRecordSpanError(ctx, err) {
			span.SetStatus(codes.Unset, "")
			span.End()
			return nil, err
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "model stream failed")
		span.End()
		c.logger.Error(
			ctx,
			"model stream failed",
			"model_id", c.modelID,
			"err", err,
		)
		return nil, err
	}
	ts := &tracedStream{
		inner:     st,
		span:      span,
		ctx:       ctx,
		startedAt: startedAt,
	}
	if c.captureMessages {
		ts.output = newGenAIStreamAccumulator()
	}
	return ts, nil
}

func (s *tracedStream) Recv() (model.Chunk, error) {
	ch, err := s.inner.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			s.end(codes.Ok, "eof")
			return ch, err
		}
		if !telemetry.ShouldRecordSpanError(s.ctx, err) {
			s.end(codes.Unset, "")
			return ch, err
		}
		s.span.RecordError(err)
		s.end(codes.Error, "stream recv failed")
		return ch, err
	}
	if ch.UsageDelta != nil {
		s.mu.Lock()
		s.sawUsageDelta = true
		if s.usage.Model == "" {
			s.usage.Model = ch.UsageDelta.Model
		}
		if s.usage.ModelClass == "" {
			s.usage.ModelClass = ch.UsageDelta.ModelClass
		}
		s.usage.InputTokens += ch.UsageDelta.InputTokens
		s.usage.OutputTokens += ch.UsageDelta.OutputTokens
		s.usage.TotalTokens += ch.UsageDelta.TotalTokens
		s.usage.CacheReadTokens += ch.UsageDelta.CacheReadTokens
		s.usage.CacheWriteTokens += ch.UsageDelta.CacheWriteTokens
		s.mu.Unlock()
	}
	if isFirstGenAIOutputChunk(ch.Type) {
		s.recordFirstChunk()
	}
	s.recordOutputChunk(ch)
	if ch.Type == model.ChunkTypeStop && ch.StopReason != "" {
		s.span.SetAttributes(telemetry.AttrGenAIResponseFinishReasons.StringSlice([]string{ch.StopReason}))
	}
	return ch, nil
}

func (s *tracedStream) Close() error {
	err := s.inner.Close()
	if err != nil {
		if !telemetry.ShouldRecordSpanError(s.ctx, err) {
			s.end(codes.Unset, "")
			return err
		}
		s.span.RecordError(err)
		s.end(codes.Error, "stream close failed")
		return err
	}
	s.end(codes.Ok, "closed")
	return nil
}

func (s *tracedStream) Metadata() map[string]any {
	return s.inner.Metadata()
}

func (s *tracedStream) end(code codes.Code, desc string) {
	s.endOnce.Do(func() {
		s.mu.Lock()
		usage := s.usage
		sawUsageDelta := s.sawUsageDelta
		output := s.output
		s.mu.Unlock()
		if !sawUsageDelta {
			usage = mergeStreamMetadataUsage(usage, s.inner.Metadata())
		}

		if (usage != model.TokenUsage{}) {
			s.span.SetAttributes(modelUsageAttrs(usage)...)
		}
		if output != nil {
			if messages, stopReason, ok := output.finish(); ok {
				s.applyOutputMessages(messages, stopReason)
			}
		}
		s.span.SetStatus(code, desc)
		s.span.End()
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

func mergeStreamMetadataUsage(base model.TokenUsage, meta map[string]any) model.TokenUsage {
	value, ok := meta["usage"]
	if !ok {
		return base
	}
	usage, ok := value.(model.TokenUsage)
	if !ok {
		panic("runtime: stream metadata usage must be model.TokenUsage")
	}
	base.InputTokens += usage.InputTokens
	base.OutputTokens += usage.OutputTokens
	base.TotalTokens += usage.TotalTokens
	base.CacheReadTokens += usage.CacheReadTokens
	base.CacheWriteTokens += usage.CacheWriteTokens
	if usage.Model != "" {
		base.Model = usage.Model
	}
	if usage.ModelClass != "" {
		base.ModelClass = usage.ModelClass
	}
	return base
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
func (c *tracedClient) recordInputMessages(span telemetry.Span, messages []*model.Message) {
	if !c.captureMessages {
		return
	}
	attr, ok, err := telemetry.GenAIInputMessagesAttr(messages)
	setGenAIMessagesAttr(span, attr, ok, err, "input")
}

// recordOutputMessages stamps the chat-turn span with the complete non-streaming
// assistant response when sensitive GenAI message capture is enabled.
func (c *tracedClient) recordOutputMessages(span telemetry.Span, messages []model.Message, stopReason string) {
	if !c.captureMessages {
		return
	}
	attr, ok, err := telemetry.GenAIOutputMessagesAttr(messages, stopReason)
	setGenAIMessagesAttr(span, attr, ok, err, "output")
}

// applyOutputMessages stamps the chat-turn span with the buffered streaming
// assistant output. It is called once at stream end; s.output is nil unless
// capture is enabled, so the default hot path is unchanged.
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

// recordOutputChunk feeds a streamed chunk to the accumulator so a single
// output message can be emitted at stream end. No-op when capture is disabled.
func (s *tracedStream) recordOutputChunk(chunk model.Chunk) {
	if s.output == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output.recordChunk(chunk)
}

// genAIStreamAccumulator coalesces streamed assistant chunks into one canonical
// output message: adjacent text deltas merge into a single text part, only
// final reasoning blocks are kept, and the terminal stop reason is captured.
type genAIStreamAccumulator struct {
	parts      []model.Part
	stopReason string
}

func newGenAIStreamAccumulator() *genAIStreamAccumulator {
	return &genAIStreamAccumulator{}
}

func (a *genAIStreamAccumulator) recordChunk(chunk model.Chunk) {
	switch chunk.Type {
	case model.ChunkTypeText:
		if chunk.Message != nil {
			a.appendOutputParts(chunk.Message.Parts)
		}
	case model.ChunkTypeThinking:
		if chunk.Message != nil {
			a.appendFinalThinkingParts(chunk.Message.Parts)
		}
	case model.ChunkTypeToolCall:
		if chunk.ToolCall != nil {
			a.parts = append(a.parts, model.ToolUsePart{
				ID:    chunk.ToolCall.ID,
				Name:  string(chunk.ToolCall.Name),
				Input: chunk.ToolCall.Payload,
			})
		}
	case model.ChunkTypeCompletion:
		if chunk.Completion != nil {
			a.parts = append(a.parts, model.TextPart{Text: string(chunk.Completion.Payload)})
		}
	case model.ChunkTypeStop:
		a.stopReason = chunk.StopReason
	}
}

// finish returns the coalesced assistant message and stop reason. It reports
// ok=false when no output parts were observed so callers can skip emitting an
// empty gen_ai.output.messages attribute.
func (a *genAIStreamAccumulator) finish() ([]model.Message, string, bool) {
	if len(a.parts) == 0 {
		return nil, a.stopReason, false
	}
	return []model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: a.parts,
	}}, a.stopReason, true
}

func (a *genAIStreamAccumulator) appendOutputParts(parts []model.Part) {
	for _, part := range parts {
		text, ok := part.(model.TextPart)
		if !ok {
			a.parts = append(a.parts, part)
			continue
		}
		if text.Text == "" {
			continue
		}
		if len(a.parts) > 0 {
			if last, ok := a.parts[len(a.parts)-1].(model.TextPart); ok {
				last.Text += text.Text
				a.parts[len(a.parts)-1] = last
				continue
			}
		}
		a.parts = append(a.parts, text)
	}
}

func (a *genAIStreamAccumulator) appendFinalThinkingParts(parts []model.Part) {
	for _, part := range parts {
		thinking, ok := part.(model.ThinkingPart)
		if !ok {
			a.parts = append(a.parts, part)
			continue
		}
		if thinking.Final || len(thinking.Redacted) > 0 {
			a.parts = append(a.parts, thinking)
		}
	}
}
