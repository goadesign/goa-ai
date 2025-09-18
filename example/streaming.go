package assistantapi

import (
	"context"

	streaming "example.com/assistant/gen/streaming"
	"goa.design/clue/log"
)

// streaming service example implementation.
// The example methods log the requests and return zero values.
type streamingsrvc struct{}

// NewStreaming returns the streaming service implementation.
func NewStreaming() streaming.Service {
	return &streamingsrvc{}
}

// Stream events from server to client using SSE
func (s *streamingsrvc) StreamEvents(ctx context.Context, p *streaming.StreamEventsPayload, stream streaming.StreamEventsServerStream) (err error) {
	log.Printf(ctx, "streaming.stream_events")
	return
}

// Stream logs using SSE
func (s *streamingsrvc) StreamLogs(ctx context.Context, p *streaming.StreamLogsPayload, stream streaming.StreamLogsServerStream) (err error) {
	log.Printf(ctx, "streaming.stream_logs")
	return
}

// Monitor resource changes with server streaming
func (s *streamingsrvc) MonitorResourceChanges(ctx context.Context, p *streaming.MonitorResourceChangesPayload, stream streaming.MonitorResourceChangesServerStream) (err error) {
	log.Printf(ctx, "streaming.monitor_resource_changes")
	return
}

// Flexible data streaming
func (s *streamingsrvc) FlexibleData(ctx context.Context, p *streaming.FlexibleDataPayload, stream streaming.FlexibleDataServerStream) (err error) {
	log.Printf(ctx, "streaming.flexible_data")
	return
}
