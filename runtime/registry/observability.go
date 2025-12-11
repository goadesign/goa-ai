// Package registry provides runtime components for managing MCP registry
// connections, tool discovery, and catalog synchronization.
package registry

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// OperationType identifies the type of registry operation for observability.
type OperationType string

const (
	// OpDiscoverToolset is the operation type for toolset discovery.
	OpDiscoverToolset OperationType = "discover_toolset"
	// OpSearch is the operation type for registry search.
	OpSearch OperationType = "search"
	// OpListToolsets is the operation type for listing toolsets.
	OpListToolsets OperationType = "list_toolsets"
	// OpGetToolset is the operation type for getting a single toolset.
	OpGetToolset OperationType = "get_toolset"
	// OpSync is the operation type for registry synchronization.
	OpSync OperationType = "sync"
	// OpRegister is the operation type for adding a registry to the manager.
	OpRegister OperationType = "register"
	// OpCacheGet is the operation type for cache get operations.
	OpCacheGet OperationType = "cache_get"
	// OpCacheSet is the operation type for cache set operations.
	OpCacheSet OperationType = "cache_set"
)

// OperationOutcome represents the result of an operation.
type OperationOutcome string

const (
	// OutcomeSuccess indicates the operation completed successfully.
	OutcomeSuccess OperationOutcome = "success"
	// OutcomeError indicates the operation failed with an error.
	OutcomeError OperationOutcome = "error"
	// OutcomeCacheHit indicates a cache hit occurred.
	OutcomeCacheHit OperationOutcome = "cache_hit"
	// OutcomeCacheMiss indicates a cache miss occurred.
	OutcomeCacheMiss OperationOutcome = "cache_miss"
	// OutcomeFallback indicates fallback to cached data was used.
	OutcomeFallback OperationOutcome = "fallback"
)

// OperationEvent represents a structured log event for registry operations.
type OperationEvent struct {
	// Operation is the type of operation performed.
	Operation OperationType
	// Registry is the name of the registry involved.
	Registry string
	// Toolset is the name of the toolset involved (if applicable).
	Toolset string
	// Query is the search query (if applicable).
	Query string
	// Duration is how long the operation took.
	Duration time.Duration
	// Outcome is the result of the operation.
	Outcome OperationOutcome
	// Error is the error message if the operation failed.
	Error string
	// ResultCount is the number of results returned (if applicable).
	ResultCount int
	// CacheKey is the cache key used (if applicable).
	CacheKey string
}

// Observability provides structured logging, metrics, and tracing for registry operations.
type Observability struct {
	logger  telemetry.Logger
	metrics telemetry.Metrics
	tracer  telemetry.Tracer
}

// NewObservability creates a new Observability instance with the given telemetry components.
func NewObservability(logger telemetry.Logger, metrics telemetry.Metrics, tracer telemetry.Tracer) *Observability {
	obs := &Observability{
		logger:  logger,
		metrics: metrics,
		tracer:  tracer,
	}
	// Use noop implementations if not provided
	if obs.logger == nil {
		obs.logger = &noopLogger{}
	}
	if obs.metrics == nil {
		obs.metrics = &noopMetrics{}
	}
	if obs.tracer == nil {
		obs.tracer = &noopTracer{}
	}
	return obs
}

// LogOperation emits a structured log event for a registry operation.
func (o *Observability) LogOperation(ctx context.Context, event OperationEvent) {
	keyvals := []any{
		"operation", string(event.Operation),
		"outcome", string(event.Outcome),
		"duration_ms", event.Duration.Milliseconds(),
	}

	if event.Registry != "" {
		keyvals = append(keyvals, "registry", event.Registry)
	}
	if event.Toolset != "" {
		keyvals = append(keyvals, "toolset", event.Toolset)
	}
	if event.Query != "" {
		keyvals = append(keyvals, "query", event.Query)
	}
	if event.ResultCount > 0 {
		keyvals = append(keyvals, "result_count", event.ResultCount)
	}
	if event.CacheKey != "" {
		keyvals = append(keyvals, "cache_key", event.CacheKey)
	}
	if event.Error != "" {
		keyvals = append(keyvals, "error", event.Error)
	}

	msg := "registry operation completed"
	switch event.Outcome {
	case OutcomeError:
		o.logger.Error(ctx, msg, keyvals...)
	case OutcomeFallback:
		o.logger.Warn(ctx, msg, keyvals...)
	case OutcomeSuccess, OutcomeCacheHit, OutcomeCacheMiss:
		o.logger.Info(ctx, msg, keyvals...)
	}
}

// RecordOperationMetrics records metrics for a registry operation.
// Metrics recorded:
//   - registry.operation.duration: Histogram of operation latency
//   - registry.operation.success: Counter of successful operations
//   - registry.operation.error: Counter of failed operations
//   - registry.cache.hit: Counter of cache hits
//   - registry.cache.miss: Counter of cache misses
//   - registry.operation.fallback: Counter of fallback to cached data
//   - registry.operation.result_count: Gauge of results returned
//   - registry.cache.hit_ratio: Gauge of cache hit ratio (computed)
func (o *Observability) RecordOperationMetrics(event OperationEvent) {
	tags := []string{
		"operation", string(event.Operation),
		"outcome", string(event.Outcome),
	}
	if event.Registry != "" {
		tags = append(tags, "registry", event.Registry)
	}

	// Record latency
	o.metrics.RecordTimer("registry.operation.duration", event.Duration, tags...)

	// Record success/error counters
	switch event.Outcome {
	case OutcomeSuccess:
		o.metrics.IncCounter("registry.operation.success", 1, tags...)
	case OutcomeError:
		o.metrics.IncCounter("registry.operation.error", 1, tags...)
	case OutcomeCacheHit:
		o.metrics.IncCounter("registry.cache.hit", 1, tags...)
		// Also count as success for overall success rate
		o.metrics.IncCounter("registry.operation.success", 1, tags...)
	case OutcomeCacheMiss:
		o.metrics.IncCounter("registry.cache.miss", 1, tags...)
	case OutcomeFallback:
		o.metrics.IncCounter("registry.operation.fallback", 1, tags...)
		// Fallback is a degraded success
		o.metrics.IncCounter("registry.operation.success", 1, tags...)
	}

	// Record result count if applicable
	if event.ResultCount > 0 {
		o.metrics.RecordGauge("registry.operation.result_count", float64(event.ResultCount), tags...)
	}
}

// RecordCacheMetrics records cache-specific metrics.
// This should be called periodically to track cache statistics.
func (o *Observability) RecordCacheMetrics(registry string, hits, misses int64) {
	tags := []string{"registry", registry}

	total := hits + misses
	if total > 0 {
		hitRatio := float64(hits) / float64(total)
		o.metrics.RecordGauge("registry.cache.hit_ratio", hitRatio, tags...)
	}

	o.metrics.RecordGauge("registry.cache.hits_total", float64(hits), tags...)
	o.metrics.RecordGauge("registry.cache.misses_total", float64(misses), tags...)
}

// StartSpan starts a new trace span for a registry operation.
func (o *Observability) StartSpan(ctx context.Context, operation OperationType, attrs ...attribute.KeyValue) (context.Context, telemetry.Span) {
	spanName := "registry." + string(operation)
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	}
	return o.tracer.Start(ctx, spanName, opts...)
}

// EndSpan ends a trace span with the operation outcome.
func (o *Observability) EndSpan(span telemetry.Span, outcome OperationOutcome, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, string(outcome))
	}
	span.End()
}

// InjectTraceContext injects trace context into HTTP headers for propagation.
func InjectTraceContext(ctx context.Context, header http.Header) {
	if ctx == nil || header == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// ExtractTraceContext extracts trace context from HTTP headers.
func ExtractTraceContext(ctx context.Context, header http.Header) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if header == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(header))
}

// noopTracer is a no-op tracer implementation.
type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	return ctx, &noopSpan{}
}

func (noopTracer) Span(_ context.Context) telemetry.Span {
	return &noopSpan{}
}

// noopSpan is a no-op span implementation.
type noopSpan struct{}

func (noopSpan) End(_ ...trace.SpanEndOption)                {}
func (noopSpan) AddEvent(_ string, _ ...any)                 {}
func (noopSpan) SetStatus(_ codes.Code, _ string)            {}
func (noopSpan) RecordError(_ error, _ ...trace.EventOption) {}
