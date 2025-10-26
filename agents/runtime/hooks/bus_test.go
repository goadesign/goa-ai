package hooks

import (
	"context"
	"testing"

	"goa.design/goa-ai/agents/runtime/run"
)

func TestBusPublishFanOut(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	count := 0
	sub := SubscriberFunc(func(ctx context.Context, event Event) error {
		count++
		return nil
	})
	if _, err := bus.Register(sub); err != nil {
		t.Fatalf("register: %v", err)
	}
	evt1 := NewRunStartedEvent("run1", "agent1", run.Context{}, nil)
	if err := bus.Publish(ctx, evt1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	evt2 := NewRunCompletedEvent("run1", "agent1", "success", nil)
	if err := bus.Publish(ctx, evt2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 events, got %d", count)
	}
}

func TestBusRegisterNil(t *testing.T) {
	bus := NewBus()
	if _, err := bus.Register(nil); err == nil {
		t.Fatal("expected error registering nil subscriber")
	}
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
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	evt1 := NewRunStartedEvent("run1", "agent1", run.Context{}, nil)
	if err := bus.Publish(ctx, evt1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	evt2 := NewRunCompletedEvent("run1", "agent1", "success", nil)
	if err := bus.Publish(ctx, evt2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected only first event delivered, got %d", count)
	}
}
