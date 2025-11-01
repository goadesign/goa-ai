package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/stream"
)

func TestSendPublishesEnvelope(t *testing.T) {
	cli := mockpulse.NewClient(t)
	str := mockpulse.NewStream(t)

	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		require.Equal(t, "run/run-123", name)
		return str, nil
	})
	str.AddAdd(func(ctx context.Context, event string, payload []byte) (string, error) {
		require.Equal(t, string(stream.EventToolEnd), event)
		var env envelope
		require.NoError(t, json.Unmarshal(payload, &env))
		require.Equal(t, "run-123", env.RunID)
		require.Equal(t, "tool_end", env.Type)
		body, ok := env.Payload.(map[string]any)
		require.True(t, ok)
		res, ok := body["result"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "ok", res["status"])
		return "1-0", nil
	})

	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)

	endPayload := stream.ToolEndPayload{Result: map[string]string{"status": "ok"}}
	err = sink.Send(context.Background(), stream.ToolEnd{
		Base: stream.Base{T: stream.EventToolEnd, R: "run-123", P: endPayload},
		Data: endPayload,
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
			return "custom/" + e.RunID(), nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, sink.Send(
		context.Background(),
		stream.PlannerThought{
			Base: stream.Base{T: stream.EventPlannerThought, R: "run-1", P: "n"},
			Note: "n",
		},
	))
}

func TestSendRequiresRunID(t *testing.T) {
	sink, err := NewSink(Options{Client: mockpulse.NewClient(t)})
	require.NoError(t, err)
	err = sink.Send(context.Background(), stream.AssistantReply{Text: "hi"})
	require.EqualError(t, err, "stream event missing run id")
}

func TestStreamCreationError(t *testing.T) {
	cli := mockpulse.NewClient(t)
	cli.AddStream(func(name string) (clientspulse.Stream, error) {
		return nil, errors.New("boom")
	})
	sink, err := NewSink(Options{Client: cli})
	require.NoError(t, err)
	err = sink.Send(
		context.Background(),
		stream.AssistantReply{
			Base: stream.Base{T: stream.EventAssistantReply, R: "r", P: "ok"},
			Text: "ok",
		},
	)
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
	err = sink.Send(
		context.Background(),
		stream.AssistantReply{
			Base: stream.Base{T: stream.EventAssistantReply, R: "r", P: "ok"},
			Text: "ok",
		},
	)
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
