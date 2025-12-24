package toolregistry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// InjectTraceContext encodes the trace context in ctx as W3C Trace Context
// header values. If ctx does not contain a valid trace context, it returns
// empty strings.
func InjectTraceContext(ctx context.Context) (traceParent, traceState, baggage string) {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier["traceparent"], carrier["tracestate"], carrier["baggage"]
}

// ExtractTraceContext returns a context that carries the W3C Trace Context
// represented by traceParent and traceState. Empty values are allowed and will
// yield the input ctx unchanged.
func ExtractTraceContext(ctx context.Context, traceParent, traceState, baggage string) context.Context {
	if traceParent == "" && traceState == "" && baggage == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{}
	if traceParent != "" {
		carrier["traceparent"] = traceParent
	}
	if traceState != "" {
		carrier["tracestate"] = traceState
	}
	if baggage != "" {
		carrier["baggage"] = baggage
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
