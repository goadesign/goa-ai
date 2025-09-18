package assistantapi

import (
	"context"

	grpcstream "example.com/assistant/gen/grpcstream"
	"goa.design/clue/log"
)

// grpcstream service example implementation.
// The example methods log the requests and return zero values.
type grpcstreamsrvc struct{}

// NewGrpcstream returns the grpcstream service implementation.
func NewGrpcstream() grpcstream.Service {
	return &grpcstreamsrvc{}
}

// List items with server streaming
func (s *grpcstreamsrvc) ListItems(ctx context.Context, p *grpcstream.ListItemsPayload, stream grpcstream.ListItemsServerStream) (err error) {
	log.Printf(ctx, "grpcstream.list_items")
	return
}

// Collect metrics via client stream
func (s *grpcstreamsrvc) CollectMetrics(ctx context.Context, stream grpcstream.CollectMetricsServerStream) (err error) {
	log.Printf(ctx, "grpcstream.collect_metrics")
	return
}

// Echo service with bidirectional streaming
func (s *grpcstreamsrvc) Echo(ctx context.Context, stream grpcstream.EchoServerStream) (err error) {
	log.Printf(ctx, "grpcstream.echo")
	return
}
