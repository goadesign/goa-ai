package pulse

import (
	"context"
	"testing"
	"time"

	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
)

func TestRuntimeStreamsSinkLifecycle(t *testing.T) {
	client := &fakeClient{stream: &fakeStream{sink: &fakeSink{events: make(chan *streaming.Event)}}}
	streams, err := NewRuntimeStreams(RuntimeStreamsOptions{Client: client})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if streams.Sink() == nil {
		t.Fatalf("expected sink")
	}

	if err := streams.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}
	if client.closeCount != 1 {
		t.Fatalf("expected client close")
	}
}

func TestRuntimeStreamsSubscriberUsesClient(t *testing.T) {
	eventsCh := make(chan *streaming.Event)
	fakeSink := &fakeSink{events: eventsCh}
	client := &fakeClient{stream: &fakeStream{sink: fakeSink}}
	streams, err := NewRuntimeStreams(RuntimeStreamsOptions{Client: client})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sub, err := streams.NewSubscriber(SubscriberOptions{SinkName: "front", Buffer: 1})
	if err != nil {
		t.Fatalf("subscriber error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events, errs, stop, err := sub.Subscribe(ctx, "run/test")
	if err != nil {
		cancel()
		t.Fatalf("subscribe error: %v", err)
	}
	close(eventsCh)
	stop()
	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatalf("expected closed events channel")
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for events close")
	}
	select {
	case _, ok := <-errs:
		if ok {
			t.Fatalf("expected closed errs channel")
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for errs close")
	}
	if !fakeSink.closed {
		t.Fatalf("expected sink close")
	}
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
