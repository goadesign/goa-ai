package provider

// A deadline-settled completion finishes delivery without claiming that the
// provider result was published. The same lifecycle must still execute its
// next call and release its lease normally when stopped.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// completionTracer records only lifecycle event names; the other tracing
	// operations use the existing no-op implementation.
	completionTracer struct {
		telemetry.Tracer
		events chan string
	}

	completionSpan struct {
		telemetry.Span
		events chan string
	}
)

func TestServeDeadlineSettlementAcknowledgesAndContinues(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan *streaming.Event, 1)
	acks := make(chan string, 2)
	tracer := &completionTracer{
		Tracer: telemetry.NewNoopTracer(), events: make(chan string, 8),
	}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(_ context.Context, event *streaming.Event) error {
		acks <- event.ID
		return nil
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})
	var completions, releases atomic.Int64
	registration := successfulRegistration()
	registration.Complete = func(
		_ context.Context,
		_, _, _, _, _ string,
		result toolregistry.ToolResultMessage,
	) (bool, error) {
		completions.Add(1)
		return result.ToolUseID == "following-call", nil
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		releases.Add(1)
		return nil
	}
	handler := &recordingHandler{}
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("provider did not finish during cleanup")
		}
	})
	go func() {
		defer close(finished)
		done <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:             testProviderID,
			Pong:                   func(context.Context, string, string, string) error { return nil },
			MaxConcurrentToolCalls: 1,
			MaxQueuedToolCalls:     1,
			ShutdownTimeout:        time.Second,
			Tracer:                 tracer,
		})
	}()
	for _, call := range []struct {
		id    string
		event string
	}{
		{id: "deadline-call", event: "toolregistry.tool_call_settled"},
		{id: "following-call", event: "toolregistry.tool_result_published"},
	} {
		events <- testToolCallEvent(t, call.id)
		select {
		case ack := <-acks:
			assert.Equal(t, call.id, ack)
		case err := <-done:
			t.Fatalf("provider stopped before acknowledging %s: %v", call.id, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("provider did not acknowledge %s", call.id)
		}
		select {
		case event := <-tracer.events:
			assert.Equal(t, call.event, event)
		case <-time.After(3 * time.Second):
			t.Fatalf("provider did not trace settlement of %s", call.id)
		}
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not finish after cancellation")
	}
	assert.Equal(t, int64(2), handler.calls.Load())
	assert.Equal(t, int64(2), completions.Load())
	assert.Equal(t, int64(1), releases.Load())
	assert.Empty(t, tracer.events)
}

func (t *completionTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	return ctx, &completionSpan{Span: t.Span(ctx), events: t.events}
}

func (s *completionSpan) AddEvent(name string, _ ...any) {
	s.events <- name
}
