package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func addTraceMeta(ctx context.Context, meta *mcp.Meta) {
	if ctx == nil {
		return
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return
	}
	if *meta == nil {
		*meta = make(mcp.Meta, len(carrier))
	}
	for k, v := range carrier {
		(*meta)[k] = v
	}
}
