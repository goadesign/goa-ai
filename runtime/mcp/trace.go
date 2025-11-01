package mcp

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func injectTraceHeaders(ctx context.Context, header http.Header) {
	if ctx == nil || header == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

func addTraceMeta(ctx context.Context, params map[string]any) {
	if ctx == nil || params == nil {
		return
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return
	}
	meta := make(map[string]string, len(carrier))
	for k, v := range carrier {
		meta[k] = v
	}
	params["_meta"] = meta
}
