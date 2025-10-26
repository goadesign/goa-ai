package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/agents/runtime/stream"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
)

func TestSendPublishesEnvelope(t *testing.T) {
	cli := mockpulse.NewClient(t)
	str := mockpulse.NewStream(t)

	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		require.Equal(t, "run/run-123", name)
		return str, nil
	})
	str.AddAdd(func(ctx context.Context, event string, payload []byte) (string, error) {
		require.Equal(t, string(stream.EventToolUpdate), event)
		var env envelope
		require.NoError(t, json.Unmarshal(payload, &env))
		require.Equal(t, "run-123", env.RunID)
		require.Equal(t, "tool_update", env.Type)
		body, ok := env.Payload.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "ok", body["status"])
		return "1-0", nil
	})

	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)

	err = sink.Send(context.Background(), stream.Event{
		Type:    stream.EventToolUpdate,
		RunID:   "run-123",
		Content: map[string]string{"status": "ok"},
	})
	require.NoError(t, err)
	require.False(t, str.HasMore())
}

func TestCustomStreamID(t *testing.T) {
	cli := mockpulse.NewClient(t)
	str := mockpulse.NewStream(t)
	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		require.Equal(t, "custom/run-1", name)
		return str, nil
	})
	str.AddAdd(func(ctx context.Context, event string, payload []byte) (string, error) {
		return "1-0", nil
	})
	sink, err := NewSink(Options{
		Client: cli,
		StreamID: func(e stream.Event) (string, error) {
			return "custom/" + e.RunID, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, sink.Send(context.Background(), stream.Event{Type: stream.EventPlannerThought, RunID: "run-1"}))
}

func TestSendRequiresRunID(t *testing.T) {
	sink, err := NewSink(Options{Client: mockpulse.NewClient(t)})
	require.NoError(t, err)
	err = sink.Send(context.Background(), stream.Event{Type: stream.EventAssistantReply})
	require.EqualError(t, err, "stream event missing run id")
}

func TestStreamCreationError(t *testing.T) {
	cli := mockpulse.NewClient(t)
	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		return nil, errors.New("boom")
	})
	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)
	err = sink.Send(context.Background(), stream.Event{Type: stream.EventAssistantReply, RunID: "r"})
	require.EqualError(t, err, "boom")
}

func TestAddError(t *testing.T) {
	cli := mockpulse.NewClient(t)
	str := mockpulse.NewStream(t)
	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		return str, nil
	})
	str.AddAdd(func(ctx context.Context, event string, payload []byte) (string, error) {
		return "", errors.New("add-failed")
	})
	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)
	err = sink.Send(context.Background(), stream.Event{Type: stream.EventAssistantReply, RunID: "r"})
	require.EqualError(t, err, "add-failed")
}

func TestCloseDelegates(t *testing.T) {
	cli := mockpulse.NewClient(t)
	cli.AddClose(func(ctx context.Context) error {
		require.NotNil(t, ctx)
		return nil
	})
	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)
	require.NoError(t, sink.Close(context.Background()))
}
