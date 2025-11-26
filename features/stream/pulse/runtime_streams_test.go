package pulse

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
)

func TestRuntimeStreamsSinkLifecycle(t *testing.T) {
	client := &fakeClient{stream: &fakeStream{sink: &fakeSink{events: make(chan *streaming.Event)}}}
	streams, err := NewRuntimeStreams(RuntimeStreamsOptions{Client: client})
	require.NoError(t, err)
	require.NotNil(t, streams.Sink())
	require.NoError(t, streams.Close(context.Background()))
	require.Equal(t, 1, client.closeCount)
}

func TestRuntimeStreamsSubscriberUsesClient(t *testing.T) {
	eventsCh := make(chan *streaming.Event)
	fakeSink := &fakeSink{events: eventsCh}
	client := &fakeClient{stream: &fakeStream{sink: fakeSink}}
	streams, err := NewRuntimeStreams(RuntimeStreamsOptions{Client: client})
	require.NoError(t, err)

	sub, err := streams.NewSubscriber(SubscriberOptions{SinkName: "front", Buffer: 1})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	events, errs, stop, err := sub.Subscribe(ctx, "run/test")
	if err != nil {
		cancel()
		require.FailNowf(t, "subscribe", "subscribe error: %v", err)
	}
	close(eventsCh)
	stop()
	cancel()

	select {
	case _, ok := <-events:
		require.False(t, ok, "expected closed events channel")
	case <-time.After(time.Second):
		require.FailNow(t, "timeout waiting for events close")
	}
	select {
	case _, ok := <-errs:
		require.False(t, ok, "expected closed errs channel")
	case <-time.After(time.Second):
		require.FailNow(t, "timeout waiting for errs close")
	}
	require.True(t, fakeSink.closed)
}

type fakeClient struct {
	stream     *fakeStream
	closeCount int
	lastStream string
}

func (f *fakeClient) Stream(name string) (clientspulse.Stream, error) {
	f.lastStream = name
	return f.stream, nil
}

func (f *fakeClient) Close(ctx context.Context) error {
	f.closeCount++
	return nil
}

type fakeStream struct {
	sink       *fakeSink
	lastSink   string
	addPayload []byte
}

func (f *fakeStream) Add(ctx context.Context, event string, payload []byte) (string, error) {
	f.addPayload = payload
	return "0-0", nil
}

func (f *fakeStream) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (clientspulse.Sink, error) {
	f.lastSink = name
	return f.sink, nil
}

type fakeSink struct {
	events chan *streaming.Event
	closed bool
}

func (f *fakeSink) Subscribe() <-chan *streaming.Event { return f.events }

func (f *fakeSink) Ack(context.Context, *streaming.Event) error { return nil }

func (f *fakeSink) Close(context.Context) { f.closed = true }
