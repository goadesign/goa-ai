// These tests hold decoded calls in the provider queue and verify that work
// retains acknowledgment metadata without retaining a second encoded call.
package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestServeReleasesEncodedCallsWithoutChangingDeliveredEvents(t *testing.T) {
	for _, drain := range []bool{false, true} {
		name := "complete queued call"
		if drain {
			name = "drain with queued call"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			gate := newJoinGate()
			events := make(chan *streaming.Event, 4)
			acknowledgments := make(chan *streaming.Event, 4)
			handled := make(chan toolregistry.ToolCallMessage, 2)
			draining := make(chan struct{})
			sink := mockpulse.NewSink(t)
			sink.SetSubscribe(func() <-chan *streaming.Event { return events })
			sink.SetClose(func(context.Context) error { return nil })
			sink.SetAck(func(_ context.Context, event *streaming.Event) error {
				acknowledgments <- event
				return nil
			})
			stream := mockpulse.NewStream(t)
			stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
			client := mockpulse.NewClient(t)
			client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
			registration := successfulRegistration()
			registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
				close(draining)
				return nil
			}
			handler := joinHandlerFunc(func(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
				handled <- msg
				if msg.ToolUseID == "active" {
					gate.hold(ctx)
				}
				return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
			})
			result := startJoinServe(t, cancel, func() error {
				return ServeAndWait(ctx, client, "test.toolset", handler, registration, Options{
					ProviderID:             testProviderID,
					Pong:                   func(context.Context, string, string, string) error { return nil },
					MaxConcurrentToolCalls: 1, MaxQueuedToolCalls: 1, ShutdownTimeout: time.Second,
				})
			}, gate)

			// Escaped label bytes occupy more encoded space than decoded space. Neither
			// releasing their encoding nor draining may alter what the handler receives.
			acker := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379"})
			t.Cleanup(func() { assert.NoError(t, acker.Close()) })
			originals := make(map[string]*streaming.Event)
			payloads := make(map[string][]byte)
			expectedCalls := make(map[string]toolregistry.ToolCallMessage)
			for _, id := range []string{"active", "queued", "overloaded"} {
				event := testToolCallEvent(t, id)
				var msg toolregistry.ToolCallMessage
				require.NoError(t, json.Unmarshal(event.Payload, &msg))
				msg.Meta.Labels = map[string]string{"escaped": strings.Repeat("\x00", 1024)}
				encoded, err := json.Marshal(msg)
				require.NoError(t, err)
				event.Payload = encoded
				event.StreamName = "requests"
				event.StreamGeneration = "generation"
				event.SinkName = "consumer"
				event.Topic = "topic"
				event.Acker = acker
				originals[id] = event
				payloads[id] = append([]byte(nil), encoded...)
				expectedCalls[id] = msg
			}
			events <- originals["active"]
			awaitJoinValue(t, gate.entered)
			assert.Equal(t, expectedCalls["active"], awaitJoinValue(t, handled))
			events <- originals["queued"]
			events <- originals["overloaded"]
			assert.Same(t, originals["overloaded"], awaitJoinValue(t, acknowledgments))

			// Old call frames without admission metadata are still rejected and
			// acknowledged directly, without altering the delivered event.
			oldFrame := &streaming.Event{ID: "old-frame", Payload: []byte(`{"type":"call","tool_use_id":"old","payload":{"x":1}}`)}
			oldPayload := append([]byte(nil), oldFrame.Payload...)
			events <- oldFrame
			assert.Same(t, oldFrame, awaitJoinValue(t, acknowledgments))
			assert.Equal(t, oldPayload, oldFrame.Payload)

			if drain {
				cancel()
				awaitJoinValue(t, draining)
			}
			gate.open()
			count := 2
			if drain {
				count = 1
			}
			for range count {
				ack := awaitJoinValue(t, acknowledgments)
				original := originals[ack.ID]
				require.NotNil(t, original)
				expected := *original
				expected.Payload = nil
				assert.NotSame(t, original, ack)
				assert.Equal(t, expected, *ack)
			}
			if !drain {
				assert.Equal(t, expectedCalls["queued"], awaitJoinValue(t, handled))
				cancel()
			}
			require.ErrorIs(t, awaitJoinValue(t, result), context.Canceled)
			assert.Empty(t, handled)
			assert.Empty(t, acknowledgments)
			for id, event := range originals {
				assert.Equal(t, payloads[id], event.Payload)
			}
		})
	}
}
