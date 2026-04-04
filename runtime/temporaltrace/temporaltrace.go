// Package temporaltrace defines the OpenTelemetry tracing contract for Temporal
// activity execution in goa-ai powered runtimes.
//
// Contract:
//   - Temporal activities execute in a distinct trace domain from synchronous
//     request handling (HTTP/gRPC). Each activity execution starts a new root
//     span (new trace ID) to avoid long-lived traces that fragment in collectors
//     and sampling pipelines.
//   - If an activity is causally triggered by an upstream request trace, the
//     upstream trace is preserved via an OTel link, not parenthood. This keeps
//     trace trees semantically honest while retaining navigability across
//     domains.
//   - Control-plane and retry scheduling are not "children" in the request tree.
//     Durable scheduling becomes a link between traces.
package temporaltrace

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
	"goa.design/goa-ai/runtime/agent/telemetry"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	originTraceParentKey struct{}

	// LinkPropagator carries an origin traceparent string through Temporal
	// workflow/activity headers under HeaderTraceParent.
	//
	// The payload is encoded with Temporal's data converter so it can safely flow
	// across SDK versions and language boundaries.
	LinkPropagator struct {
		Converter converter.DataConverter
	}

	// ActivityInterceptor starts a new root span for each activity execution and
	// attaches an OTel link to the origin trace (when present).
	ActivityInterceptor struct {
		interceptor.WorkerInterceptorBase

		Tracer trace.Tracer
	}

	activityInbound struct {
		interceptor.ActivityInboundInterceptorBase

		tracer trace.Tracer
	}
)

const (
	// HeaderTraceParent is the Temporal header key that carries the upstream W3C
	// traceparent string for trace-linking.
	HeaderTraceParent = "ck.traceparent"

	instrumentationName = "goa.design/goa-ai/runtime/temporaltrace"
)

// WithOriginTraceParent returns a derived context carrying the origin
// traceparent string. This value is used to link durable execution traces back
// to the initiating request trace.
func WithOriginTraceParent(ctx context.Context, traceparent string) context.Context {
	return context.WithValue(ctx, originTraceParentKey{}, traceparent)
}

// OriginTraceParent returns the origin traceparent string stored by
// WithOriginTraceParent, if any.
func OriginTraceParent(ctx context.Context) (string, bool) {
	tp, ok := ctx.Value(originTraceParentKey{}).(string)
	return tp, ok
}

// NewLinkPropagator returns a Temporal context propagator that carries the
// origin traceparent string across workflow/activity headers.
func NewLinkPropagator() workflow.ContextPropagator {
	return &LinkPropagator{
		Converter: converter.GetDefaultDataConverter(),
	}
}

// NewActivityInterceptor returns a worker interceptor that starts a new root
// span for each activity execution and links it to the origin trace (when
// present).
func NewActivityInterceptor() interceptor.WorkerInterceptor {
	return &ActivityInterceptor{
		Tracer: otel.Tracer(instrumentationName),
	}
}

func (p *LinkPropagator) Inject(ctx context.Context, writer workflow.HeaderWriter) error {
	traceparent, ok := OriginTraceParent(ctx)
	if !ok {
		traceparent = TraceParent(ctx)
	}
	if traceparent == "" {
		return nil
	}
	payload, err := p.Converter.ToPayload(traceparent)
	if err != nil {
		return fmt.Errorf("temporaltrace: encode traceparent: %w", err)
	}
	writer.Set(HeaderTraceParent, payload)
	return nil
}

func (p *LinkPropagator) Extract(ctx context.Context, reader workflow.HeaderReader) (context.Context, error) {
	payload, ok := reader.Get(HeaderTraceParent)
	if !ok {
		return ctx, nil
	}
	var traceparent string
	if err := p.Converter.FromPayload(payload, &traceparent); err != nil {
		return nil, fmt.Errorf("temporaltrace: decode traceparent: %w", err)
	}
	return WithOriginTraceParent(ctx, traceparent), nil
}

func (p *LinkPropagator) InjectFromWorkflow(ctx workflow.Context, writer workflow.HeaderWriter) error {
	traceparent, ok := ctx.Value(originTraceParentKey{}).(string)
	if !ok {
		return nil
	}
	if traceparent == "" {
		return nil
	}
	payload, err := p.Converter.ToPayload(traceparent)
	if err != nil {
		return fmt.Errorf("temporaltrace: encode traceparent: %w", err)
	}
	writer.Set(HeaderTraceParent, payload)
	return nil
}

func (p *LinkPropagator) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (workflow.Context, error) {
	payload, ok := reader.Get(HeaderTraceParent)
	if !ok {
		return ctx, nil
	}
	var traceparent string
	if err := p.Converter.FromPayload(payload, &traceparent); err != nil {
		return nil, fmt.Errorf("temporaltrace: decode traceparent: %w", err)
	}
	return workflow.WithValue(ctx, originTraceParentKey{}, traceparent), nil
}

func (i *ActivityInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	tracer := i.Tracer
	if tracer == nil {
		tracer = otel.Tracer(instrumentationName)
	}
	return &activityInbound{
		ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{
			Next: next,
		},
		tracer: tracer,
	}
}

func (i *ActivityInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return next
}

func (a *activityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	info := activity.GetInfo(ctx)
	spanName := "temporal.activity." + info.ActivityType.Name

	attrs := []attribute.KeyValue{
		attribute.String("name", spanName),
		attribute.String("temporal.workflow.id", info.WorkflowExecution.ID),
		attribute.String("temporal.workflow.run_id", info.WorkflowExecution.RunID),
		attribute.String("temporal.activity.type", info.ActivityType.Name),
		attribute.String("temporal.task_queue", info.TaskQueue),
		attribute.Int("temporal.activity.attempt", int(info.Attempt)),
	}

	var links []trace.Link
	if traceparent, ok := OriginTraceParent(ctx); ok && traceparent != "" {
		origin, err := ParseTraceParent(traceparent)
		if err != nil {
			return nil, err
		}
		links = append(links, trace.Link{SpanContext: origin})
	}

	opts := []trace.SpanStartOption{
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attrs...),
	}
	if len(links) > 0 {
		opts = append(opts, trace.WithLinks(links...))
	}

	ctx, span := a.tracer.Start(ctx, spanName, opts...)
	defer span.End()

	out, err := a.Next.ExecuteActivity(ctx, in)
	if err == nil {
		return out, nil
	}
	if telemetry.ShouldRecordSpanError(ctx, err) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return nil, err
}

// TraceParent returns the W3C traceparent string for the active span in ctx.
// Returns an empty string if no active span exists.
func TraceParent(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	flags := byte(sc.TraceFlags())
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + hex.EncodeToString([]byte{flags})
}

// ParseTraceParent parses a W3C traceparent header into a remote span context.
//
// This function enforces the core traceparent validity rules (shape, hex
// encoding, non-zero trace/span IDs). If traceparent is invalid, it returns an
// error to surface contract violations early.
func ParseTraceParent(traceparent string) (trace.SpanContext, error) {
	parts := strings.Split(traceparent, "-")
	if len(parts) < 4 {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent %q", traceparent)
	}

	version := parts[0]
	if len(version) != 2 {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent version %q", traceparent)
	}
	if strings.EqualFold(version, "ff") {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent version %q", traceparent)
	}
	if strings.EqualFold(version, "00") && len(parts) != 4 {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent shape %q", traceparent)
	}

	traceIDHex := parts[1]
	spanIDHex := parts[2]
	flagsHex := parts[3]

	if len(traceIDHex) != 32 || len(spanIDHex) != 16 || len(flagsHex) != 2 {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent fields %q", traceparent)
	}

	traceIDBytes, err := hex.DecodeString(traceIDHex)
	if err != nil {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent trace id %q: %w", traceparent, err)
	}
	spanIDBytes, err := hex.DecodeString(spanIDHex)
	if err != nil {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent span id %q: %w", traceparent, err)
	}
	flagsBytes, err := hex.DecodeString(flagsHex)
	if err != nil {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid traceparent flags %q: %w", traceparent, err)
	}

	var traceID trace.TraceID
	copy(traceID[:], traceIDBytes)
	var spanID trace.SpanID
	copy(spanID[:], spanIDBytes)

	if !traceID.IsValid() || !spanID.IsValid() {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid trace/span id %q", traceparent)
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.TraceFlags(flagsBytes[0]),
		Remote:     true,
	})
	if !sc.IsValid() {
		return trace.SpanContext{}, fmt.Errorf("temporaltrace: invalid span context %q", traceparent)
	}
	return sc, nil
}
