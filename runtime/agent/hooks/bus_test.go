package hooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestBusPublishFanOut(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	count := 0
	sub := SubscriberFunc(func(ctx context.Context, event Event) error {
		count++
		return nil
	})
	_, err := bus.Register(sub)
	require.NoError(t, err)
	evt1 := NewRunStartedEvent("run1", "agent1", run.Context{}, nil)
	require.NoError(t, bus.Publish(ctx, evt1))
	evt2 := NewRunCompletedEvent("run1", "agent1", "success", nil)
	require.NoError(t, bus.Publish(ctx, evt2))
	require.Equal(t, 2, count)
}

func TestBusRegisterNil(t *testing.T) {
	bus := NewBus()
	_, err := bus.Register(nil)
	require.Error(t, err)
}

func TestSubscriptionClose(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()
	count := 0
	sub := SubscriberFunc(func(ctx context.Context, event Event) error {
		count++
		return nil
	})
	subscription, err := bus.Register(sub)
	require.NoError(t, err)
	evt1 := NewRunStartedEvent("run1", "agent1", run.Context{}, nil)
	require.NoError(t, bus.Publish(ctx, evt1))
	require.NoError(t, subscription.Close())
	evt2 := NewRunCompletedEvent("run1", "agent1", "success", nil)
	require.NoError(t, bus.Publish(ctx, evt2))
	require.Equal(t, 1, count)
}
