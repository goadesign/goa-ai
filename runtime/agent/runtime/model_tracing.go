package runtime

import (
	"context"
	"errors"
	"io"
	"sync"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	tracedClient struct {
		inner   model.Client
		tracer  telemetry.Tracer
		logger  telemetry.Logger
		modelID string
	}

	tracedStream struct {
		inner model.Streamer
		span  telemetry.Span

		mu    sync.Mutex
		usage model.TokenUsage

		endOnce sync.Once
	}
)

func newTracedClient(inner model.Client, tracer telemetry.Tracer, logger telemetry.Logger, modelID string) model.Client {
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
		inner:   inner,
		tracer:  tracer,
		logger:  logger,
		modelID: modelID,
	}
}

func (c *tracedClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	ctx, span := c.tracer.Start(
		ctx,
		"model.complete",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(modelSpanAttrs(c.modelID, req)...),
	)
	defer span.End()

	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
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
		span.AddEvent(
			"model.usage",
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", resp.Usage.TotalTokens,
			"cache_read_tokens", resp.Usage.CacheReadTokens,
			"cache_write_tokens", resp.Usage.CacheWriteTokens,
		)
	}
	if resp.StopReason != "" {
		span.AddEvent("model.stop", "reason", resp.StopReason)
	}
	span.SetStatus(codes.Ok, "ok")
	return resp, nil
}

func (c *tracedClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	ctx, span := c.tracer.Start(
		ctx,
		"model.stream",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(modelSpanAttrs(c.modelID, req)...),
	)

	st, err := c.inner.Stream(ctx, req)
	if err != nil {
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
	return &tracedStream{
		inner: st,
		span:  span,
	}, nil
}

func (s *tracedStream) Recv() (model.Chunk, error) {
	ch, err := s.inner.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			s.end(codes.Ok, "eof")
			return ch, err
		}
		s.span.RecordError(err)
		s.end(codes.Error, "stream recv failed")
		return ch, err
	}
	if ch.UsageDelta != nil {
		s.mu.Lock()
		s.usage.InputTokens += ch.UsageDelta.InputTokens
		s.usage.OutputTokens += ch.UsageDelta.OutputTokens
		s.usage.TotalTokens += ch.UsageDelta.TotalTokens
		s.usage.CacheReadTokens += ch.UsageDelta.CacheReadTokens
		s.usage.CacheWriteTokens += ch.UsageDelta.CacheWriteTokens
		s.mu.Unlock()
	}
	if ch.Type == model.ChunkTypeStop && ch.StopReason != "" {
		s.span.AddEvent("model.stop", "reason", ch.StopReason)
	}
	return ch, nil
}

func (s *tracedStream) Close() error {
	err := s.inner.Close()
	if err != nil {
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
		s.mu.Unlock()

		if (usage != model.TokenUsage{}) {
			s.span.AddEvent(
				"model.usage",
				"input_tokens", usage.InputTokens,
				"output_tokens", usage.OutputTokens,
				"total_tokens", usage.TotalTokens,
				"cache_read_tokens", usage.CacheReadTokens,
				"cache_write_tokens", usage.CacheWriteTokens,
			)
		}
		s.span.SetStatus(code, desc)
		s.span.End()
	})
}

func modelSpanAttrs(modelID string, req *model.Request) []attribute.KeyValue {
	if req == nil {
		return nil
	}
	return []attribute.KeyValue{
		attribute.String("goa_ai.model_id", modelID),
		attribute.String("goa_ai.run_id", req.RunID),
		attribute.String("goa_ai.model", req.Model),
		attribute.String("goa_ai.model_class", string(req.ModelClass)),
		attribute.Bool("goa_ai.stream", req.Stream),
		attribute.Bool("goa_ai.thinking", req.Thinking != nil && req.Thinking.Enable),
		attribute.Int("goa_ai.max_tokens", req.MaxTokens),
	}
}
