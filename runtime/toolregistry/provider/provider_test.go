package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	canceled chan struct{}
	deadline chan time.Time
	callSeen atomic.Bool
}

type recordingHandler struct {
	calls atomic.Int64
}

type backlogHandler struct {
	firstStarted chan struct{}
	unblockFirst chan struct{}
	handled      chan string
	calls        atomic.Int64
}

type invalidResultHandler struct{}

const pulseAddEventID = "0-0"
const (
	testAdmissionRevision     = "rollout-2026.07.23.1"
	testProviderID            = "pod-a/test.toolset"
	testProviderIncarnationID = "11111111-1111-4111-8111-111111111111"
	testRegistrationTokenA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRegistrationTokenB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type pongCall struct {
	providerID string
	pingID     string
}

func (h *blockingHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	if !h.callSeen.Swap(true) {
		close(h.started)
	}
	if h.deadline != nil {
		deadline, _ := ctx.Deadline()
		h.deadline <- deadline
	}
	if h.canceled == nil {
		<-h.unblock
	} else {
		select {
		case <-ctx.Done():
			close(h.canceled)
			<-h.unblock
		case <-h.unblock:
		}
	}
	return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
}

func (h *recordingHandler) HandleToolCall(_ context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	h.calls.Add(1)
	return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
}

func (h *backlogHandler) HandleToolCall(_ context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	if h.calls.Add(1) == 1 {
		close(h.firstStarted)
		<-h.unblockFirst
	}
	h.handled <- msg.ToolUseID
	return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
}

func (h *invalidResultHandler) HandleToolCall(context.Context, toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	result := toolregistry.NewToolResultErrorMessage("", "", "execution_failed", "failed")
	result.Result = json.RawMessage(`{"ok":true}`)
	return result, nil
}

func TestServe_RejectsEmptyProviderID(t *testing.T) {
	t.Parallel()

	err := Serve(context.Background(), mockpulse.NewClient(t), "test.toolset", &blockingHandler{}, successfulRegistration(), Options{
		Pong: func(context.Context, string, string, string) error {
			return nil
		},
	})
	require.ErrorContains(t, err, "provider id is required")
}

func TestServeRejectsNULProviderID(t *testing.T) {
	t.Parallel()

	err := Serve(
		context.Background(),
		mockpulse.NewClient(t),
		"test.toolset",
		&blockingHandler{},
		successfulRegistration(),
		Options{
			ProviderID: "provider\x00alias",
			Pong:       func(context.Context, string, string, string) error { return nil },
		},
	)
	require.ErrorContains(t, err, "provider id must not contain NUL")
}

func TestServeRejectsUnsafeCapacityConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		options Options
	}{
		{name: "negative concurrency", options: Options{MaxConcurrentToolCalls: -1}},
		{name: "excessive concurrency", options: Options{MaxConcurrentToolCalls: MaxConcurrentToolCalls + 1}},
		{name: "negative queue", options: Options{MaxQueuedToolCalls: -1}},
		{name: "excessive queue", options: Options{MaxQueuedToolCalls: MaxQueuedToolCalls + 1}},
		{name: "negative shutdown timeout", options: Options{ShutdownTimeout: -1}},
		{
			name: "shutdown exceeds settlement",
			options: Options{
				ShutdownTimeout: MaxShutdownTimeout + time.Millisecond,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.options.ProviderID = testProviderID
			test.options.Pong = func(context.Context, string, string, string) error { return nil }
			err := Serve(
				context.Background(),
				mockpulse.NewClient(t),
				"test.toolset",
				&recordingHandler{},
				successfulRegistration(),
				test.options,
			)
			require.Error(t, err)
		})
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
	sink.SetClose(func(_ context.Context) error { return nil })

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
		started:  make(chan struct{}),
		unblock:  make(chan struct{}),
		deadline: make(chan time.Time, 1),
	}

	pongs := make(chan pongCall, 10)

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, successfulRegistration(func(toolregistry.ToolResultMessage) error {
			adds.Add(1)
			return nil
		}), Options{
			ProviderID: testProviderID,
			Pong: func(_ context.Context, providerID, _ string, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				return nil
			},
		})
	}()

	// Send a tool call first, then wait until the handler is running (blocked).
	executionDeadline := time.Now().Add(toolregistry.MaxToolCallWait).UTC().Truncate(time.Millisecond)
	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse_1",
		executionDeadline,
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
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
	assert.True(t, executionDeadline.Equal(<-h.deadline))

	// Now send a ping and assert Pong is handled promptly while the tool call is still blocked.
	ping := toolregistry.NewPingMessage(testRegistrationTokenA, "ping_1")
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

func TestServeAcknowledgesRegistrySettledStaleQueuedCall(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan *streaming.Event, 2)
	acked := make(chan *streaming.Event, 2)
	results := make(chan toolregistry.ToolResultMessage, 1)
	rejected := make(chan [3]string, 1)

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(_ context.Context, event *streaming.Event) error {
		acked <- event
		return nil
	})
	sink.SetClose(func(context.Context) error { return nil })
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, _ string, payload []byte) (string, error) {
		var result toolregistry.ToolResultMessage
		require.NoError(t, json.Unmarshal(payload, &result))
		results <- result
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, opts ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		retention := streamopts.ParseStreamOptions(opts...)
		assert.True(t, retention.DeadlineSet)
		assert.WithinDuration(t, time.Now().Add(toolregistry.DefaultResultStreamTTL), retention.Deadline, time.Second)
		return resultStream, nil
	})

	handler := &recordingHandler{}
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		results <- result
		return nil
	})
	registration.Claim = func(
		_ context.Context,
		_, _, _, providerToken, callToken, toolUseID, _ string,
	) (ClaimDisposition, error) {
		rejected <- [3]string{providerToken, callToken, toolUseID}
		return ClaimTerminal, nil
	}
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID: testProviderID,
			Pong:       func(context.Context, string, string, string) error { return nil },
		})
	}()

	malformed := toolregistry.NewToolCallMessage(
		"ABC123",
		"tooluse_malformed",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
	)
	malformedPayload, err := json.Marshal(malformed)
	require.NoError(t, err)
	malformedEvent := &streaming.Event{ID: "0-0", EventName: "call", Payload: malformedPayload}
	events <- malformedEvent
	select {
	case got := <-acked:
		assert.Same(t, malformedEvent, got)
	case <-time.After(time.Second):
		t.Fatal("provider did not acknowledge malformed-token call")
	}
	select {
	case result := <-results:
		t.Fatalf("malformed-token call published result: %+v", result)
	default:
	}

	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenB,
		"tooluse_stale",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-2"},
	)
	payload, err := json.Marshal(call)
	require.NoError(t, err)
	event := &streaming.Event{ID: "1-0", EventName: "call", Payload: payload}
	events <- event

	select {
	case tokens := <-rejected:
		assert.Equal(t, [3]string{
			testRegistrationTokenA,
			testRegistrationTokenB,
			"tooluse_stale",
		}, tokens)
	case <-time.After(time.Second):
		t.Fatal("provider did not ask registry to reject stale queued call")
	}
	assert.Empty(t, results)
	select {
	case got := <-acked:
		assert.Same(t, event, got)
	case <-time.After(time.Second):
		t.Fatal("provider did not acknowledge stale queued call")
	}
	assert.Zero(t, handler.calls.Load())

	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
}

func TestServeShutdownPublishesThenAcknowledgesBeforeRelease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event, 1)
	handler := &blockingHandler{started: make(chan struct{}), unblock: make(chan struct{})}
	resultPublished := make(chan struct{})
	allowAck := make(chan struct{})
	released := make(chan struct{})
	drainDuration := make(chan time.Duration, 1)

	var (
		orderMu sync.Mutex
		order   []string
	)
	record := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(closeCtx context.Context) error {
		_, hasDeadline := closeCtx.Deadline()
		assert.True(t, hasDeadline)
		require.NoError(t, closeCtx.Err())
		record("close")
		return nil
	})
	sink.SetAck(func(context.Context, *streaming.Event) error {
		<-allowAck
		record("ack")
		return nil
	})
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		record("result")
		close(resultPublished)
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		return resultStream, nil
	})
	registration := successfulRegistration()
	registration.Complete = func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
		record("result")
		close(resultPublished)
		return nil
	}
	registration.Drain = func(_ context.Context, _, _, _, _ string, duration time.Duration) error {
		drainDuration <- duration
		record("drain")
		return nil
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		record("release")
		close(released)
		return nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: time.Second,
		})
	}()

	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse-shutdown",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
	)
	payload, err := json.Marshal(call)
	require.NoError(t, err)
	events <- &streaming.Event{ID: "shutdown-1", EventName: "call", Payload: payload}
	<-handler.started

	cancel()
	close(handler.unblock)
	<-resultPublished
	select {
	case <-released:
		t.Fatal("provider lease released before acknowledgement settled")
	default:
	}
	close(allowAck)
	require.ErrorIs(t, <-errc, context.Canceled)
	<-released
	assert.Equal(t, time.Second+SettlementAuthorityMargin, <-drainDuration)

	orderMu.Lock()
	resultIndex := slices.Index(order, "result")
	drainIndex := slices.Index(order, "drain")
	closeIndex := slices.Index(order, "close")
	ackIndex := slices.Index(order, "ack")
	releaseIndex := slices.Index(order, "release")
	assert.Less(t, resultIndex, ackIndex)
	assert.Less(t, drainIndex, closeIndex)
	assert.Less(t, closeIndex, releaseIndex)
	assert.Less(t, ackIndex, releaseIndex)
	orderMu.Unlock()
}

func TestServeDrainingLeaseProcessesAcceptedBufferedCalls(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event)
	drainStarted := make(chan struct{})
	allowDrain := make(chan struct{})
	handler := &blockingHandler{started: make(chan struct{}), unblock: make(chan struct{})}
	completed := make(chan string, 2)
	acked := make(chan string, 2)
	released := make(chan struct{})
	subscribed := make(chan struct{})

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(_ context.Context, ev *streaming.Event) error {
		acked <- ev.ID
		return nil
	})
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		close(subscribed)
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		return resultStream, nil
	})
	registration := successfulRegistration()
	registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
		close(drainStarted)
		<-allowDrain
		return nil
	}
	registration.Complete = func(
		_ context.Context,
		_, _, _, _, _ string,
		result toolregistry.ToolResultMessage,
	) error {
		completed <- result.ToolUseID
		return nil
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		close(released)
		return nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:             testProviderID,
			Pong:                   func(context.Context, string, string, string) error { return nil },
			MaxConcurrentToolCalls: 1,
			ShutdownTimeout:        time.Second,
		})
	}()
	<-subscribed

	events <- testToolCallEvent(t, "drain-active")
	<-handler.started
	secondAccepted := make(chan struct{})
	bufferedEvent := testToolCallEvent(t, "drain-buffered")
	go func() {
		events <- bufferedEvent
		close(secondAccepted)
	}()
	<-secondAccepted
	cancel()
	<-drainStarted
	close(allowDrain)
	close(handler.unblock)

	require.ErrorIs(t, <-errc, context.Canceled)
	assert.ElementsMatch(t, []string{"drain-active", "drain-buffered"}, []string{<-completed, <-completed})
	assert.ElementsMatch(t, []string{"drain-active", "drain-buffered"}, []string{<-acked, <-acked})
	<-released
}

func TestServeAckFailureIsExplicitAndPreventsRelease(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event, 1)
	released := atomic.Bool{}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(context.Context, *streaming.Event) error {
		return errors.New("ack unavailable")
	})
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		return resultStream, nil
	})
	registration := successfulRegistration()
	registration.Release = func(context.Context, string, string, string, string) error {
		released.Store(true)
		return nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(context.Background(), client, "test.toolset", &recordingHandler{}, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: time.Second,
		})
	}()
	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse-ack-failure",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
	)
	payload, err := json.Marshal(call)
	require.NoError(t, err)
	events <- &streaming.Event{ID: "ack-failure-1", EventName: "call", Payload: payload}

	err = <-errc
	require.ErrorContains(t, err, "ack unavailable")
	assert.False(t, released.Load())
}

func TestServeDrainFailureStillClosesAndJoinsConsumer(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event, 2)
	closed := make(chan struct{})
	released := atomic.Bool{}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error {
		close(closed)
		return nil
	})
	sink.SetAck(func(context.Context, *streaming.Event) error {
		return errors.New("ack unavailable")
	})
	requestStream := mockpulse.NewStream(t)
	requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return requestStream, nil
		}
		return resultStream, nil
	})
	handler := &recordingHandler{}
	registration := successfulRegistration()
	registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
		return errors.New("drain unavailable")
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		released.Store(true)
		return nil
	}
	registration.RetryInitialInterval = time.Millisecond
	registration.RetryMaxInterval = time.Millisecond
	registration.AttemptTimeout = 2 * time.Millisecond
	registration.ReleaseTimeout = 10 * time.Millisecond

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(
			context.Background(),
			client,
			"test.toolset",
			handler,
			registration,
			Options{
				ProviderID:      testProviderID,
				Pong:            func(context.Context, string, string, string) error { return nil },
				ShutdownTimeout: 100 * time.Millisecond,
			},
		)
	}()
	events <- testToolCallEvent(t, "drain-failure")

	err := <-errc
	require.ErrorContains(t, err, "ack unavailable")
	require.ErrorContains(t, err, "drain unavailable")
	<-closed
	assert.Equal(t, int64(1), handler.calls.Load())
	assert.False(t, released.Load())

	events <- testToolCallEvent(t, "orphan-probe")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int64(1), handler.calls.Load())
}

func TestServeSinkCloseTimeoutPreventsRelease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event)
	subscribed := make(chan struct{})
	released := atomic.Bool{}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event {
		close(subscribed)
		return events
	})
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(closeCtx context.Context) error {
		<-closeCtx.Done()
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
	registration := successfulRegistration()
	registration.Release = func(context.Context, string, string, string, string) error {
		released.Store(true)
		return nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: 20 * time.Millisecond,
		})
	}()
	<-subscribed
	cancel()
	err := <-errc
	require.ErrorContains(t, err, "close toolset sink")
	assert.False(t, released.Load())
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
	sink.SetClose(func(_ context.Context) error { return nil })

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
		errc <- Serve(ctx, client, toolset, h, successfulRegistration(func(toolregistry.ToolResultMessage) error {
			adds.Add(1)
			return nil
		}), Options{
			ProviderID:             testProviderID,
			MaxConcurrentToolCalls: 1,
			MaxQueuedToolCalls:     0,
			Pong: func(_ context.Context, providerID, _ string, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				return nil
			},
		})
	}()

	// Send one tool call that will start and block.
	call1 := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse_1",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
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
		testRegistrationTokenA,
		"tooluse_2",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":2}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-2"},
	)
	call2Payload, err := json.Marshal(call2)
	if err != nil {
		t.Fatalf("marshal call2: %v", err)
	}
	eventsCh <- &streaming.Event{ID: "2-0", EventName: "call", Payload: call2Payload}

	ping := toolregistry.NewPingMessage(testRegistrationTokenA, "ping_1")
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

func TestServeMaxQueuedToolCallsBoundsTotalWaitingWork(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event, 4)
	handler := &blockingHandler{started: make(chan struct{}), unblock: make(chan struct{})}
	var (
		published  atomic.Int64
		acked      atomic.Int64
		overloaded atomic.Int64
	)
	ponged := make(chan struct{}, 1)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(context.Context, *streaming.Event) error {
		acked.Add(1)
		return nil
	})
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		t.Fatalf("provider opened unexpected result stream %q", name)
		return nil, nil
	})

	registration := successfulRegistration(func(toolregistry.ToolResultMessage) error {
		published.Add(1)
		return nil
	})
	registration.ReportOverload = func(
		_ context.Context,
		_, _, _, providerToken, callToken, _ string,
		_ string,
	) error {
		assert.Equal(t, testRegistrationTokenA, providerToken)
		assert.Equal(t, testRegistrationTokenA, callToken)
		published.Add(1)
		overloaded.Add(1)
		return nil
	}
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID: testProviderID,
			Pong: func(context.Context, string, string, string) error {
				ponged <- struct{}{}
				return nil
			},
			MaxConcurrentToolCalls: 1,
			MaxQueuedToolCalls:     1,
			ShutdownTimeout:        time.Second,
		})
	}()
	events <- testToolCallEvent(t, "queue-1")
	<-handler.started
	events <- testToolCallEvent(t, "queue-2")
	events <- testToolCallEvent(t, "queue-3")
	ping := toolregistry.NewPingMessage(testRegistrationTokenA, "ping-capacity")
	pingPayload, err := json.Marshal(ping)
	require.NoError(t, err)
	events <- &streaming.Event{ID: "ping-capacity", EventName: "ping", Payload: pingPayload}
	select {
	case <-ponged:
	case <-time.After(time.Second):
		t.Fatal("provider did not process ping while work queue was saturated")
	}
	cancel()
	close(handler.unblock)
	require.ErrorIs(t, <-errc, context.Canceled)
	assert.Equal(t, int64(3), published.Load())
	assert.Equal(t, int64(1), overloaded.Load())
	assert.Equal(t, int64(4), acked.Load())
}

func TestServeSettlesExpiredQueuedCallAndKeepsServing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event, 3)
	acked := make(chan string, 3)
	settled := make(chan string, 1)
	completed := make(chan string, 2)
	handler := &backlogHandler{
		firstStarted: make(chan struct{}),
		unblockFirst: make(chan struct{}),
		handled:      make(chan string, 2),
	}

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(_ context.Context, event *streaming.Event) error {
		acked <- event.ID
		return nil
	})
	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return toolsetStream, nil
		}
		assert.NotEqual(t, toolregistry.ResultStreamID("expired"), name)
		return resultStream, nil
	})

	start := time.Now().Truncate(time.Millisecond)
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		completed <- result.ToolUseID
		return nil
	})
	registration.Claim = func(
		_ context.Context,
		_, _, _, providerToken, callToken, toolUseID, _ string,
	) (ClaimDisposition, error) {
		assert.Equal(t, testRegistrationTokenA, providerToken)
		assert.Equal(t, testRegistrationTokenA, callToken)
		if toolUseID != "expired" {
			return ClaimExecute, nil
		}
		settled <- toolUseID
		return ClaimExpired, nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(
			ctx,
			client,
			"test.toolset",
			handler,
			registration,
			Options{
				ProviderID:             testProviderID,
				Pong:                   func(context.Context, string, string, string) error { return nil },
				MaxConcurrentToolCalls: 1,
				MaxQueuedToolCalls:     1,
				ShutdownTimeout:        time.Second,
			},
		)
	}()

	events <- testToolCallEventAt(t, "first", start.Add(time.Hour))
	<-handler.firstStarted
	events <- testToolCallEventAt(t, "expired", start.Add(time.Minute))
	close(handler.unblockFirst)

	assert.Equal(t, "first", <-handler.handled)
	assert.Equal(t, "first", <-completed)
	assert.Equal(t, "expired", <-settled)
	assert.ElementsMatch(t, []string{"first", "expired"}, []string{<-acked, <-acked})
	assert.Equal(t, int64(1), handler.calls.Load())
	select {
	case err := <-errc:
		t.Fatalf("provider stopped after expired queued call: %v", err)
	default:
	}

	events <- testToolCallEventAt(t, "after-expiration", start.Add(time.Hour))
	assert.Equal(t, "after-expiration", <-handler.handled)
	assert.Equal(t, "after-expiration", <-completed)
	assert.Equal(t, "after-expiration", <-acked)
	assert.Equal(t, int64(2), handler.calls.Load())

	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
}

func TestServeUsesRegistryExpirationForDeliveredCall(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name            string
		registryExpired bool
	}{
		{name: "Redis confirms expired", registryExpired: true},
		{name: "provider clock is ahead"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			events := make(chan *streaming.Event, 1)
			acked := make(chan string, 1)
			settled := make(chan string, 1)
			completed := make(chan string, 1)
			sink := mockpulse.NewSink(t)
			sink.SetSubscribe(func() <-chan *streaming.Event { return events })
			sink.SetClose(func(context.Context) error { return nil })
			sink.SetAck(func(_ context.Context, event *streaming.Event) error {
				acked <- event.ID
				return nil
			})
			requestStream := mockpulse.NewStream(t)
			requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
				return sink, nil
			})
			resultStream := mockpulse.NewStream(t)
			var resultStreamOpens atomic.Int64
			client := mockpulse.NewClient(t)
			client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
				if name == toolregistry.ToolsetStreamID("test.toolset") {
					return requestStream, nil
				}
				resultStreamOpens.Add(1)
				return resultStream, nil
			})
			handler := &recordingHandler{}
			registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
				completed <- result.ToolUseID
				return nil
			})
			registration.Claim = func(
				_ context.Context,
				_, _, _, providerToken, callToken, toolUseID, _ string,
			) (ClaimDisposition, error) {
				assert.Equal(t, testRegistrationTokenA, providerToken)
				assert.Equal(t, testRegistrationTokenA, callToken)
				settled <- toolUseID
				if test.registryExpired {
					return ClaimExpired, nil
				}
				return ClaimExecute, nil
			}

			localNow := time.Now().Truncate(time.Millisecond)
			errc := make(chan error, 1)
			go func() {
				errc <- Serve(
					ctx,
					client,
					"test.toolset",
					handler,
					registration,
					Options{
						ProviderID:      testProviderID,
						Pong:            func(context.Context, string, string, string) error { return nil },
						ShutdownTimeout: time.Second,
					},
				)
			}()
			events <- testToolCallEventAt(t, "delivered-expired-looking", localNow.Add(-time.Minute))

			assert.Equal(t, "delivered-expired-looking", <-settled)
			assert.Equal(t, "delivered-expired-looking", <-acked)
			if test.registryExpired {
				assert.Zero(t, handler.calls.Load())
				assert.Zero(t, resultStreamOpens.Load())
				select {
				case toolUseID := <-completed:
					t.Fatalf("expired call completed by handler: %s", toolUseID)
				default:
				}
			} else {
				assert.Equal(t, "delivered-expired-looking", <-completed)
				assert.Equal(t, int64(1), handler.calls.Load())
				assert.Zero(t, resultStreamOpens.Load())
			}

			cancel()
			require.ErrorIs(t, <-errc, context.Canceled)
		})
	}
}

func TestServeDuplicateDeliveryPublishesAndAcknowledgesExactResult(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event, 2)
	acks := make(chan *streaming.Event, 2)
	results := make(chan toolregistry.ToolResultMessage, 2)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(_ context.Context, event *streaming.Event) error {
		acks <- event
		return nil
	})
	requestStream := mockpulse.NewStream(t)
	requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(_ context.Context, _ string, payload []byte) (string, error) {
		var result toolregistry.ToolResultMessage
		require.NoError(t, json.Unmarshal(payload, &result))
		results <- result
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return requestStream, nil
		}
		return resultStream, nil
	})
	handler := &recordingHandler{}
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, successfulRegistration(func(result toolregistry.ToolResultMessage) error {
			results <- result
			return nil
		}), Options{
			ProviderID: testProviderID,
			Pong:       func(context.Context, string, string, string) error { return nil },
		})
	}()

	events <- testToolCallEvent(t, "reused-transport-id")
	events <- testToolCallEvent(t, "reused-transport-id")
	for range 2 {
		result := <-results
		assert.Equal(t, "reused-transport-id", result.ToolUseID)
		assert.Equal(t, testRegistrationTokenA, result.RegistrationToken)
		<-acks
	}
	assert.Equal(t, int64(2), handler.calls.Load())
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
}

func TestServeRejectsInvalidHandlerResultBeforePublication(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	events := make(chan *streaming.Event, 1)
	var (
		acked     atomic.Int64
		published atomic.Int64
	)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(context.Context, *streaming.Event) error {
		acked.Add(1)
		return nil
	})
	requestStream := mockpulse.NewStream(t)
	requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		published.Add(1)
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return requestStream, nil
		}
		return resultStream, nil
	})

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", &invalidResultHandler{}, successfulRegistration(), Options{
			ProviderID: testProviderID,
			Pong:       func(context.Context, string, string, string) error { return nil },
		})
	}()
	events <- testToolCallEvent(t, "invalid-result")

	err := <-errc
	require.ErrorContains(t, err, "tool call handler returned invalid result")
	assert.Zero(t, published.Load())
	assert.Zero(t, acked.Load())
}

func TestServeReportsHandlerSettlementTimeoutAndWithholdsRelease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *streaming.Event, 1)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	requestStream := mockpulse.NewStream(t)
	requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		return pulseAddEventID, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID("test.toolset") {
			return requestStream, nil
		}
		return resultStream, nil
	})
	handler := &blockingHandler{
		started:  make(chan struct{}),
		unblock:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	released := atomic.Bool{}
	registration := successfulRegistration()
	registration.Release = func(context.Context, string, string, string, string) error {
		released.Store(true)
		return nil
	}
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: 20 * time.Millisecond,
		})
	}()
	events <- testToolCallEvent(t, "slow-handler")
	<-handler.started
	cancel()
	select {
	case err := <-errc:
		t.Fatalf("Serve returned before the claimed call's worker joined: %v", err)
	case <-handler.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not cancel the tool worker after its settlement deadline")
	}
	close(handler.unblock)
	err := <-errc
	require.ErrorContains(t, err, "settle tool worker")
	assert.False(t, released.Load())
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
	sink.SetClose(func(_ context.Context) error { return nil })

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
		errc <- Serve(ctx, client, toolset, h, successfulRegistration(func(toolregistry.ToolResultMessage) error {
			adds.Add(1)
			return nil
		}), Options{
			ProviderID:  testProviderID,
			PongTimeout: 50 * time.Millisecond,
			Pong: func(_ context.Context, providerID, _ string, pingID string) error {
				pongs <- pongCall{providerID: providerID, pingID: pingID}
				if attempts.Add(1) == 1 {
					return errors.New("pong failed")
				}
				return nil
			},
		})
	}()

	// Send a ping that will fail Pong. Serve must not exit.
	ping1 := toolregistry.NewPingMessage(testRegistrationTokenA, "ping_1")
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
	ping2 := toolregistry.NewPingMessage(testRegistrationTokenA, "ping_2")
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
		testRegistrationTokenA,
		"tooluse_1",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
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

func testToolCallEvent(t *testing.T, toolUseID string) *streaming.Event {
	t.Helper()
	return testToolCallEventAt(t, toolUseID, time.Now().Add(toolregistry.DefaultResultStreamTTL))
}

func testToolCallEventAt(t *testing.T, toolUseID string, expiresAt time.Time) *streaming.Event {
	t.Helper()
	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		toolUseID,
		expiresAt.Add(-toolregistry.ResultStreamTransportBudget),
		expiresAt,
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-2"},
	)
	payload, err := json.Marshal(call)
	require.NoError(t, err)
	return &streaming.Event{ID: toolUseID, EventName: "call", Payload: payload}
}

type outputDeltaHandler struct {
	errc chan error
}

func (h *outputDeltaHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	toolUseID, ok := toolregistry.ToolUseIDFromContext(ctx)
	if !ok || toolUseID != msg.ToolUseID {
		select {
		case h.errc <- errors.New("missing canonical tool use ID in context"):
		default:
		}
		return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
	}
	pub, ok := toolregistry.OutputDeltaPublisherFromContext(ctx)
	if !ok {
		select {
		case h.errc <- errors.New("missing output delta publisher in context"):
		default:
		}
		return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
	}
	if err := pub.PublishToolOutputDelta(ctx, "stdout", "hello\n"); err != nil {
		select {
		case h.errc <- err:
		default:
		}
	}
	return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
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
	sink.SetClose(func(_ context.Context) error { return nil })

	toolsetStream := mockpulse.NewStream(t)
	toolsetStream.SetNewSink(func(_ context.Context, _ string, _ ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})

	addEvents := make(chan struct {
		name    string
		payload []byte
	}, 8)
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolsetStreamID {
			return toolsetStream, nil
		}
		t.Fatalf("provider opened unexpected result stream %q", name)
		return nil, nil
	})

	handlerErrs := make(chan error, 1)
	h := &outputDeltaHandler{errc: handlerErrs}
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		addEvents <- struct {
			name    string
			payload []byte
		}{name: toolregistry.ResultEventKey, payload: payload}
		return nil
	})
	registration.PublishOutputDelta = func(
		_ context.Context,
		_, _, _, providerToken, callToken, toolUseID, requestEventID, stream, delta string,
	) error {
		assert.Equal(t, testRegistrationTokenA, providerToken)
		assert.Equal(t, testRegistrationTokenA, callToken)
		assert.Equal(t, "tooluse_1", toolUseID)
		assert.Equal(t, "1-0", requestEventID)
		payload, err := json.Marshal(toolregistry.NewToolOutputDeltaMessage(callToken, toolUseID, stream, delta))
		if err != nil {
			return err
		}
		addEvents <- struct {
			name    string
			payload []byte
		}{name: toolregistry.OutputDeltaEventKey, payload: payload}
		return nil
	}

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, h, registration, Options{
			ProviderID: testProviderID,
			Pong:       func(_ context.Context, _, _, _ string) error { return nil },
		})
	}()

	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse_1",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "r1", SessionID: "s1", ToolCallID: "call-1"},
	)
	callPayload, err := json.Marshal(call)
	require.NoError(t, err)
	eventsCh <- &streaming.Event{ID: "1-0", EventName: "call", Payload: callPayload}

	seen := map[string]int{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-addEvents:
			seen[ev.name]++
			if ev.name == toolregistry.OutputDeltaEventKey {
				var delta toolregistry.ToolOutputDeltaMessage
				require.NoError(t, json.Unmarshal(ev.payload, &delta))
				assert.Equal(t, testRegistrationTokenA, delta.RegistrationToken)
				assert.Equal(t, "tooluse_1", delta.ToolUseID)
			}
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

// TestServeEnsureLoopRepairsConsumerGroup verifies Serve keeps recreating the
// toolset stream consumer group while serving, so a group Redis lost is
// repaired without redeploying the provider.
func TestServeEnsureLoopRepairsConsumerGroup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventsCh := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetClose(func(_ context.Context) error { return nil })

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

	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, "test.toolset", &blockingHandler{}, successfulRegistration(), Options{
			ProviderID:     testProviderID,
			Pong:           func(context.Context, string, string, string) error { return nil },
			EnsureInterval: 10 * time.Millisecond,
		})
	}()

	deadline := time.After(2 * time.Second)
	for ensuredGroups.Load() < 2 {
		select {
		case err := <-errc:
			t.Fatalf("Serve stopped early: %v", err)
		case <-deadline:
			t.Fatalf("ensure loop stalled: groups=%d", ensuredGroups.Load())
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

// TestServeEnsureLoopFailuresDoNotStopProvider verifies ensure-loop errors
// (Redis down) are retried on the next interval and never terminate Serve.
func TestServeEnsureLoopFailuresDoNotStopProvider(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventsCh := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return eventsCh })
	sink.SetClose(func(_ context.Context) error { return nil })

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
		errc <- Serve(ctx, client, "test.toolset", &blockingHandler{}, successfulRegistration(), Options{
			ProviderID:     testProviderID,
			Pong:           func(context.Context, string, string, string) error { return nil },
			EnsureInterval: 10 * time.Millisecond,
		})
	}()

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
