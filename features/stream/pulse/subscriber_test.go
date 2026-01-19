package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/stream"
)

func TestSubscribeEmitsEvents(t *testing.T) {
	ctx := context.Background()
	client := mockpulse.NewClient(t)
	streamMock := mockpulse.NewStream(t)
	sinkMock := mockpulse.NewSink(t)

	eventCh := make(chan *streaming.Event, 1)
	sinkMock.AddSubscribe(func() <-chan *streaming.Event { return eventCh })
	sinkMock.AddAck(func(ctx context.Context, evt *streaming.Event) error {
		require.Equal(t, "1-0", evt.ID)
		return nil
	})
	sinkMock.AddClose(func(ctx context.Context) {})

	client.AddStream(func(name string, _ ...streamopts.Stream) (clientspulse.Stream, error) {
		require.Equal(t, "run/run-123", name)
		return streamMock, nil
	})
	streamMock.AddNewSink(func(ctx context.Context, name string, opts ...streamopts.Sink) (clientspulse.Sink, error) {
		require.Equal(t, "goa_ai_subscriber", name)
		return sinkMock, nil
	})

	sub, err := NewSubscriber(SubscriberOptions{Client: client, Buffer: 2})
	require.NoError(t, err)

	events, errs, cancel, err := sub.Subscribe(ctx, "run/run-123")
	require.NoError(t, err)
	defer cancel()

	payload, _ := json.Marshal(map[string]any{
		"type":      "assistant_reply",
		"run_id":    "run-123",
		"timestamp": time.Now(),
		"payload":   map[string]string{"chunk": "hi"},
	})
	eventCh <- &streaming.Event{ID: "1-0", Payload: payload}
	close(eventCh)

	e := <-events
	require.Equal(t, stream.EventAssistantReply, e.Type())
	body := make(map[string]string)
	require.NoError(t, json.Unmarshal(e.Payload().(json.RawMessage), &body))
	require.Equal(t, "hi", body["chunk"])
	require.Empty(t, errs)
}

func TestSubscribeDecoderError(t *testing.T) {
	client := mockpulse.NewClient(t)
	streamMock := mockpulse.NewStream(t)
	sinkMock := mockpulse.NewSink(t)
	eventCh := make(chan *streaming.Event, 1)

	client.AddStream(func(name string, _ ...streamopts.Stream) (clientspulse.Stream, error) { return streamMock, nil })
	streamMock.AddNewSink(func(ctx context.Context, name string, opts ...streamopts.Sink) (clientspulse.Sink, error) {
		return sinkMock, nil
	})
	sinkMock.AddSubscribe(func() <-chan *streaming.Event { return eventCh })
	sinkMock.AddClose(func(ctx context.Context) {})

	sub, err := NewSubscriber(SubscriberOptions{
		Client: client,
		Decoder: func([]byte) (stream.Event, error) {
			return nil, errors.New("decode error")
		},
	})
	require.NoError(t, err)

	events, errs, cancel, err := sub.Subscribe(context.Background(), "run/run-1")
	require.NoError(t, err)
	defer cancel()
	eventCh <- &streaming.Event{Payload: []byte("{}")}
	close(eventCh)

	require.Empty(t, events)
	require.EqualError(t, <-errs, "pulse decode payload: decode error")
}
