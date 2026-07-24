package provider

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type blockingHandler struct {
	started  chan struct{}
	unblock  chan struct{}
	callSeen atomic.Bool
}

const pulseAddEventID = "0-0"
const testProviderID = "pod-a/test.toolset"

type pongCall struct {
	providerID string
	pingID     string
}

func (h *blockingHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	if !h.callSeen.Swap(true) {
		close(h.started)
	}
	<-h.unblock
	return toolregistry.NewToolResultMessage(msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
}

func TestServe_RejectsEmptyProviderID(t *testing.T) {
	t.Parallel()

	err := Serve(context.Background(), mockpulse.NewClient(t), "test.toolset", &blockingHandler{}, Options{
		Pong: func(context.Context, string, string) error {
			return nil
		},
	})
	require.ErrorContains(t, err, "provider id is required")
}

func TestServe_EnsureLoopRepairsGroupAndReregisters(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"

	eventsCh := make(chan *streaming.Event)

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetClose(func(_ context.Context) {})

	var ensuredGroups atomic.Int64
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	toolsetStream.SetEnsureGroup(func(_ context.Context, group string) error {
		require.Equal(t, "provider", group)
		ensuredGroups.Add(1)
		return nil
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(_ string, _ ...streamopts.Stream) (pulse.Stream, error) {
		return toolsetStream, nil
	})

	var reregistrations atomic.Int64
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, &blockingHandler{}, Options{
			ProviderID: testProviderID,
			Pong:       func(_ context.Context, _, _ string) error { return nil },
			EnsureRegistration: func(_ context.Context) error {
				reregistrations.Add(1)
				return nil
			},
			EnsureInterval: 10 * time.Millisecond,
		})
	}()

	// The ensure loop must keep repairing the consumer group and re-asserting
	// registration while the provider serves.
	deadline := time.After(2 * time.Second)
	for ensuredGroups.Load() < 2 || reregistrations.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("ensure loop stalled: groups=%d registrations=%d", ensuredGroups.Load(), reregistrations.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
	}
}

// TestServe_EnsureLoopFailuresDoNotStopProvider verifies that ensure-loop
// errors (registry unreachable, Redis down) are retried on the next interval
// and never terminate the provider loop.
func TestServe_EnsureLoopFailuresDoNotStopProvider(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventsCh := make(chan *streaming.Event)

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetClose(func(_ context.Context) {})

	var attempts atomic.Int64
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	toolsetStream.SetEnsureGroup(func(_ context.Context, _ string) error {
		attempts.Add(1)
		return errors.New("redis unavailable")
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(_ string, _ ...streamopts.Stream) (pulse.Stream, error) {
		return toolsetStream, nil
	})

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", &blockingHandler{}, Options{
			ProviderID: testProviderID,
			Pong:       func(_ context.Context, _, _ string) error { return nil },
			EnsureRegistration: func(_ context.Context) error {
				return errors.New("registry unreachable")
			},
			EnsureInterval: 10 * time.Millisecond,
		})
	}()

	// The loop must keep retrying despite persistent failures.
	deadline := time.After(2 * time.Second)
	for attempts.Load() < 3 {
		select {
		case err := <-errc:
			t.Fatalf("Serve stopped on ensure failure: %v", err)
		case <-deadline:
			t.Fatalf("ensure loop stalled after failures: attempts=%d", attempts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
	}
}

func TestServe_RespondsToPingWhileToolCallInFlight(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"
	toolsetStreamID := toolregistry.ToolsetStreamID(toolset)

	eventsCh := make(chan *streaming.Event, 10)

	// Toolset stream + sink.
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetAck(func(_ context.Context, _ *streaming.Event) error { return nil })
	sink.SetClose(func(_ context.Context) {})

	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})

	// Result stream capture.
	var adds atomic.Int64
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, _ string, _ []byte) (string, error) {
		adds.Add(1)
		return pulseAddEventID, nil
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		switch name {
		case toolsetStreamID:
			return toolsetStream, nil
		default:
			// Result streams.
			return resultStream, nil
		}
	})

	h := &blockingHandler{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}

	pongs := make(chan pongCall, 10)

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, Options{
			ProviderID: testProviderID,
			Pong: func(_ context.Context, providerID, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				return nil
			},
		})
	}()

	// Send a tool call first, then wait until the handler is running (blocked).
	call := toolregistry.NewToolCallMessage(
		"tooluse_1",
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1"},
	)
	callPayload, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal call: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "1-0", EventName: "call", Payload: callPayload}

	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Now send a ping and assert Pong is handled promptly while the tool call is still blocked.
	ping := toolregistry.NewPingMessage("ping_1")
	pingPayload, err := json.Marshal(ping)
	if err != nil {
		t.Fatalf("marshal ping: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "2-0", EventName: "ping", Payload: pingPayload}

	select {
	case got := <-pongs:
		require.Equal(t, testProviderID, got.providerID)
		require.Equal(t, "ping_1", got.pingID)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected pong while tool call is in flight")
	}

	// Let the tool call complete (publish result), then stop the server.
	close(h.unblock)
	deadline := time.After(2 * time.Second)
	for adds.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("expected at least 1 result publish, got %d", adds.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
		// The server should stop on context cancellation.
	}

	// The provider should have published exactly one result.
	if adds.Load() != 1 {
		t.Fatalf("expected 1 result publish, got %d", adds.Load())
	}
}

func TestServe_RespondsToPingWhenQueueIsFull(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"
	toolsetStreamID := toolregistry.ToolsetStreamID(toolset)

	eventsCh := make(chan *streaming.Event, 10)

	// Toolset stream + sink.
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetAck(func(_ context.Context, _ *streaming.Event) error { return nil })
	sink.SetClose(func(_ context.Context) {})

	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})

	// Result stream capture.
	var adds atomic.Int64
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, _ string, _ []byte) (string, error) {
		adds.Add(1)
		return pulseAddEventID, nil
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		switch name {
		case toolsetStreamID:
			return toolsetStream, nil
		default:
			// Result streams.
			return resultStream, nil
		}
	})

	h := &blockingHandler{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}

	pongs := make(chan pongCall, 10)

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, Options{
			ProviderID:             testProviderID,
			MaxConcurrentToolCalls: 1,
			MaxQueuedToolCalls:     0,
			Pong: func(_ context.Context, providerID, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				return nil
			},
		})
	}()

	// Send one tool call that will start and block.
	call1 := toolregistry.NewToolCallMessage(
		"tooluse_1",
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1"},
	)
	call1Payload, err := json.Marshal(call1)
	if err != nil {
		t.Fatalf("marshal call1: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "1-0", EventName: "call", Payload: call1Payload}

	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Send a second tool call while the first is blocked. With a single worker
	// and a tiny queue, the provider must not block pings while it buffers.
	call2 := toolregistry.NewToolCallMessage(
		"tooluse_2",
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":2}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1"},
	)
	call2Payload, err := json.Marshal(call2)
	if err != nil {
		t.Fatalf("marshal call2: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "2-0", EventName: "call", Payload: call2Payload}

	ping := toolregistry.NewPingMessage("ping_1")
	pingPayload, err := json.Marshal(ping)
	if err != nil {
		t.Fatalf("marshal ping: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "3-0", EventName: "ping", Payload: pingPayload}

	select {
	case got := <-pongs:
		require.Equal(t, testProviderID, got.providerID)
		require.Equal(t, "ping_1", got.pingID)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected pong while queue is full")
	}

	// Let the tool call complete (publish result), then stop the server.
	close(h.unblock)
	deadline := time.After(2 * time.Second)
	for adds.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("expected at least 1 result publish, got %d", adds.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
		// The server should stop on context cancellation.
	}
}

func TestServe_DoesNotExitOnPongFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"
	toolsetStreamID := toolregistry.ToolsetStreamID(toolset)

	eventsCh := make(chan *streaming.Event, 10)

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetAck(func(_ context.Context, _ *streaming.Event) error { return nil })
	sink.SetClose(func(_ context.Context) {})

	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})

	var adds atomic.Int64
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, _ string, _ []byte) (string, error) {
		adds.Add(1)
		return pulseAddEventID, nil
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		switch name {
		case toolsetStreamID:
			return toolsetStream, nil
		default:
			return resultStream, nil
		}
	})

	h := &blockingHandler{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}

	var attempts atomic.Int64
	pongs := make(chan pongCall, 10)

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, Options{
			ProviderID:  testProviderID,
			PongTimeout: 50 * time.Millisecond,
			Pong: func(_ context.Context, providerID, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				if attempts.Add(1) == 1 {
					return errors.New("pong failed")
				}
				return nil
			},
		})
	}()

	// Send a ping that will fail Pong. Serve must not exit.
	ping1 := toolregistry.NewPingMessage("ping_1")
	ping1Payload, err := json.Marshal(ping1)
	require.NoError(t, err)
	eventsCh <- &streaming.Event{ID: "1-0", EventName: "ping", Payload: ping1Payload}

	select {
	case got := <-pongs:
		require.Equal(t, testProviderID, got.providerID)
		require.Equal(t, "ping_1", got.pingID)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected pong attempt for ping_1")
	}
	select {
	case err := <-errc:
		t.Fatalf("Serve exited unexpectedly: %v", err)
	default:
	}

	// Send a second ping which should succeed.
	ping2 := toolregistry.NewPingMessage("ping_2")
	ping2Payload, err := json.Marshal(ping2)
	require.NoError(t, err)
	eventsCh <- &streaming.Event{ID: "2-0", EventName: "ping", Payload: ping2Payload}

	select {
	case got := <-pongs:
		require.Equal(t, testProviderID, got.providerID)
		require.Equal(t, "ping_2", got.pingID)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected pong attempt for ping_2")
	}

	// Send a tool call to prove the provider still executes calls after a failed Pong.
	call := toolregistry.NewToolCallMessage(
		"tooluse_1",
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1"},
	)
	callPayload, err := json.Marshal(call)
	require.NoError(t, err)
	eventsCh <- &streaming.Event{ID: "3-0", EventName: "call", Payload: callPayload}

	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	close(h.unblock)

	deadline := time.After(2 * time.Second)
	for adds.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected at least 1 result publish")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
	}
}

type outputDeltaHandler struct {
	errc chan error
}

func (h *outputDeltaHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	pub, ok := toolregistry.OutputDeltaPublisherFromContext(ctx)
	if !ok {
		select {
		case h.errc <- errors.New("missing output delta publisher in context"):
		default:
		}
		return toolregistry.NewToolResultMessage(msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
	}
	if err := pub.PublishToolOutputDelta(ctx, "stdout", "hello\n"); err != nil {
		select {
		case h.errc <- err:
		default:
		}
	}
	return toolregistry.NewToolResultMessage(msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
}

func TestServe_PublishesOutputDeltaToResultStream(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"
	toolsetStreamID := toolregistry.ToolsetStreamID(toolset)

	eventsCh := make(chan *streaming.Event, 10)

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetAck(func(_ context.Context, _ *streaming.Event) error { return nil })
	sink.SetClose(func(_ context.Context) {})

	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})

	addEvents := make(chan string, 8)
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, event string, _ []byte) (string, error) {
		addEvents <- event
		return pulseAddEventID, nil
	})

	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		switch name {
		case toolsetStreamID:
			return toolsetStream, nil
		default:
			return resultStream, nil
		}
	})

	handlerErrs := make(chan error, 1)
	h := &outputDeltaHandler{errc: handlerErrs}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, Options{
			ProviderID: testProviderID,
			Pong:       func(_ context.Context, _, _ string) error { return nil },
		})
	}()

	call := toolregistry.NewToolCallMessage(
		"tooluse_1",
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1"},
	)
	callPayload, err := json.Marshal(call)
	require.NoError(t, err)
	eventsCh <- &streaming.Event{ID: "1-0", EventName: "call", Payload: callPayload}

	seen := map[string]int{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-addEvents:
			seen[ev] += 1
		case <-deadline:
			t.Fatalf("timed out waiting for result stream events, saw=%v", seen)
		}
	}

	select {
	case err := <-handlerErrs:
		if err != nil {
			t.Fatalf("handler delta publish failed: %v", err)
		}
	default:
	}
	if seen[toolregistry.OutputDeltaEventKey] < 1 {
		t.Fatalf("expected at least 1 %q event, saw=%v", toolregistry.OutputDeltaEventKey, seen)
	}
	if seen[toolregistry.ResultEventKey] < 1 {
		t.Fatalf("expected at least 1 %q event, saw=%v", toolregistry.ResultEventKey, seen)
	}

	cancel()
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	case <-errc:
	}
}
