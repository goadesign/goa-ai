// These tests redeliver provider events while their original deliveries remain
// queued, running, or awaiting acknowledgement. The provider must retain the
// originals without treating their repeated deliveries as additional work.
package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestServeRetainsOriginalDeliveryUntilAcknowledged(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	activeGate, queuedGate, ackGate := newJoinGate(), newJoinGate(), newJoinGate()
	events := make(chan *streaming.Event, 16)
	acknowledged := make(chan *streaming.Event, 16)
	overloaded := make(chan string, 16)
	handled := make(chan string, 16)
	completed := make(chan string, 16)
	claimed := make(chan string, 16)
	pongs := make(chan string, 4)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(ctx context.Context, event *streaming.Event) error {
		if event.ID == "active" {
			ackGate.hold(ctx)
		}
		acknowledged <- event
		return nil
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		completed <- result.ToolUseID
		return nil
	})
	registration.ReportOverload = func(_ context.Context, _, _, _, _, _, _ string, eventID string) error {
		overloaded <- eventID
		return nil
	}
	registration.Claim = func(_ context.Context, request ClaimRequest) (ClaimDisposition, error) {
		claimed <- request.RequestEventID
		return ClaimExecute, nil
	}
	handler := joinHandlerFunc(func(ctx context.Context, message toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
		handled <- message.ToolUseID
		if message.ToolUseID == "active" {
			activeGate.hold(ctx)
		} else {
			queuedGate.hold(ctx)
		}
		return toolregistry.NewToolResultMessage(message.RegistrationToken, message.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
	})
	result := startJoinServe(t, cancel, func() error {
		return ServeAndWait(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID: testProviderID,
			Pong: func(_ context.Context, _, _, id string) error {
				pongs <- id
				return nil
			},
			MaxConcurrentToolCalls: 1, MaxQueuedToolCalls: 1, ShutdownTimeout: time.Second,
		})
	}, activeGate, queuedGate, ackGate)
	active, queued := testToolCallEvent(t, "active"), testToolCallEvent(t, "queued")
	for _, event := range []*streaming.Event{active, queued} {
		event.StreamName = "requests"
		event.StreamGeneration = "generation-one"
	}
	events <- active
	awaitJoinValue(t, activeGate.entered)
	require.Equal(t, "active", awaitJoinValue(t, handled))
	require.Equal(t, "active", awaitJoinValue(t, claimed))
	events <- queued
	activeAgain, queuedAgain := *active, *queued
	events <- &activeAgain
	events <- &queuedAgain

	// New event identities still need queue capacity, including an identical ID
	// from another stream or generation. Their acknowledgements prove intake has
	// passed both duplicate deliveries before the original handlers are released.
	newEvent := *queued
	newEvent.ID = "overflow"
	newGeneration, newStream := *queued, *queued
	newGeneration.StreamGeneration = "generation-two"
	newStream.StreamName = "other-requests"
	for _, event := range []*streaming.Event{&newEvent, &newGeneration, &newStream} {
		events <- event
		require.Equal(t, event.ID, awaitJoinValue(t, overloaded))
		require.Same(t, event, awaitJoinValue(t, acknowledged))
	}
	assert.Empty(t, overloaded)
	assert.Empty(t, acknowledged)
	assert.Empty(t, completed)
	assert.Empty(t, handled)
	assert.Empty(t, claimed)

	activeGate.open()
	awaitJoinValue(t, ackGate.entered)
	awaitJoinValue(t, queuedGate.entered)
	require.Equal(t, "active", awaitJoinValue(t, completed))
	require.Equal(t, "queued", awaitJoinValue(t, handled))
	require.Equal(t, "queued", awaitJoinValue(t, claimed))
	events <- &activeAgain
	// The earlier overflow event never entered the queue. Its redelivery must
	// now occupy the free slot, even while the original acknowledgement waits.
	events <- &newEvent
	ping := toolregistry.NewPingMessage(testRegistrationTokenA, "after-ack-duplicate")
	encoded, err := json.Marshal(ping)
	require.NoError(t, err)
	events <- &streaming.Event{ID: "ping", Payload: encoded}
	require.Equal(t, ping.PingID, awaitJoinValue(t, pongs))
	assert.Empty(t, handled)
	assert.Empty(t, claimed)
	assert.Empty(t, overloaded)
	assert.Empty(t, completed)
	// Ping acknowledgement is independent of the blocked original acknowledgement.
	require.Equal(t, "ping", awaitJoinValue(t, acknowledged).ID)
	assert.Empty(t, acknowledged)

	ackGate.open()
	require.Equal(t, "active", awaitJoinValue(t, acknowledged).ID)
	queuedGate.open()
	require.Equal(t, "queued", awaitJoinValue(t, completed))
	require.Equal(t, "queued", awaitJoinValue(t, acknowledged).ID)
	// This mock grants every distinct transport event a claim. Seeing the
	// previously rejected event here proves failed admission left no entry;
	// production Registry still decides whether its call may execute again.
	require.Equal(t, "overflow", awaitJoinValue(t, claimed))
	require.Equal(t, "queued", awaitJoinValue(t, handled))
	require.Equal(t, "queued", awaitJoinValue(t, completed))
	require.Equal(t, "overflow", awaitJoinValue(t, acknowledged).ID)
	// Acknowledgements run in order, so acknowledging the later queued event
	// proves the active event's successful acknowledgement has removed its entry.
	// A subsequent delivery must ask Registry for a fresh claim decision.
	events <- &activeAgain
	require.Equal(t, "active", awaitJoinValue(t, claimed))
	require.Equal(t, "active", awaitJoinValue(t, handled))
	require.Equal(t, "active", awaitJoinValue(t, completed))
	require.Equal(t, "active", awaitJoinValue(t, acknowledged).ID)
	cancel()
	require.ErrorIs(t, awaitJoinValue(t, result), context.Canceled)
	assert.Empty(t, handled)
	assert.Empty(t, claimed)
	assert.Empty(t, completed)
	assert.Empty(t, acknowledged)
	assert.Empty(t, overloaded)
}
