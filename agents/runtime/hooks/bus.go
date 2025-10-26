package hooks

import (
	"context"
	"errors"
	"sync"
)

type (
	// Bus publishes runtime events to registered subscribers in a fan-out pattern.
	// The bus is thread-safe and supports concurrent Publish, Register, and Close
	// operations.
	//
	// Events are delivered synchronously in the publisher's goroutine, and
	// iteration stops at the first subscriber error. This fail-fast behavior
	// ensures critical subscribers (e.g., memory persistence) can halt execution
	// if they encounter unrecoverable errors.
	Bus interface {
		// Publish delivers the event to every currently registered subscriber.
		// Subscribers are invoked in registration order, and iteration stops at
		// the first error returned by any subscriber.
		//
		// The context is forwarded to each subscriber's HandleEvent method.
		Publish(ctx context.Context, event Event) error

		// Register adds a subscriber to the bus and returns a Subscription that
		// can be closed to unregister. Register returns an error if sub is nil.
		Register(sub Subscriber) (Subscription, error)
	}

	// Subscriber reacts to published runtime events by implementing HandleEvent.
	// Subscribers are registered with a Bus and receive all events in FIFO order
	// until their subscription is closed.
	//
	// Implementations must be thread-safe if the same subscriber instance is
	// registered with multiple buses or if HandleEvent performs concurrent work.
	//
	// HandleEvent should return an error only if event processing fails in a way
	// that should halt the workflow (e.g., critical persistence failure). The
	// Bus stops iterating at the first error, so non-critical failures should be
	// logged and ignored to avoid blocking other subscribers.
	Subscriber interface {
		// HandleEvent processes a single event. The context passed to this method
		// originates from the Bus.Publish call and may have deadlines or
		// cancellation signals that implementations should respect.
		//
		// If HandleEvent returns an error, the Bus immediately stops delivering
		// the event to remaining subscribers and returns the error to the publisher.
		HandleEvent(ctx context.Context, event Event) error
	}

	// Subscription represents an active registration on a Bus. Calling Close
	// removes the subscriber from the bus, ensuring it receives no further events.
	//
	// Subscriptions are safe to close multiple times; subsequent Close calls are
	// no-ops. This makes it safe to use defer or cleanup patterns without tracking
	// whether Close has been called.
	Subscription interface {
		// Close removes the subscriber from the bus. The method is idempotent
		// and thread-safe. After Close returns, the subscriber will not receive
		// new events, though in-flight events may still be delivered if Close
		// is called during a Publish operation.
		//
		// Close always returns nil to satisfy io.Closer-like interfaces.
		Close() error
	}

	// bus is the concrete implementation of the Bus interface. It maintains
	// a thread-safe registry of subscribers and fans out events to all
	// registered subscribers synchronously.
	bus struct {
		// mu protects concurrent access to the subscribers map.
		mu sync.RWMutex
		// subscribers maps subscription handles to their subscriber implementations.
		// The subscription pointer is used as the key to enable efficient removal.
		subscribers map[*subscription]Subscriber
	}

	// subscription represents an active registration on the bus. It holds
	// a reference back to the bus for cleanup and uses sync.Once to ensure
	// idempotent Close operations.
	subscription struct {
		// bus is the parent bus this subscription belongs to.
		bus *bus
		// once ensures Close is idempotent and thread-safe.
		once sync.Once
	}
)

// NewBus constructs a new in-memory event bus for publishing runtime events
// to subscribers. The returned bus is thread-safe and ready for immediate use.
//
// The bus implements a synchronous fan-out pattern: when Publish is called,
// each registered subscriber receives the event in registration order. If any
// subscriber returns an error, iteration stops immediately and that error is
// returned to the publisher.
//
// Typical usage:
//
//	bus := hooks.NewBus()
//	sub := hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
//	    log.Printf("received: %s", evt.Type)
//	    return nil
//	})
//	subscription, _ := bus.Register(sub)
//	defer subscription.Close()
//
//	bus.Publish(ctx, hooks.Event{Type: hooks.WorkflowStarted})
func NewBus() Bus {
	return &bus{subscribers: make(map[*subscription]Subscriber)}
}

// Publish delivers the event to every currently registered subscriber in
// registration order. The method is thread-safe and can be called concurrently
// with Register and subscription Close operations.
//
// Delivery semantics:
//   - Subscribers are invoked synchronously in the caller's goroutine
//   - Iteration stops at the first error returned by any subscriber
//   - The snapshot of subscribers is captured before iteration begins, so
//     registrations/unregistrations during Publish do not affect the current delivery
//
// If no subscribers are registered, Publish returns nil immediately.
//
// The context passed to Publish is forwarded to each subscriber's HandleEvent
// method, allowing subscribers to respect cancellation and deadlines.
func (b *bus) Publish(ctx context.Context, event Event) error {
	b.mu.RLock()
	subs := make([]Subscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()
	for _, sub := range subs {
		if err := sub.HandleEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// Register adds a subscriber to the bus and returns a Subscription handle
// that can be closed to unregister. The operation is thread-safe and can be
// called concurrently with Publish and other Register calls.
//
// Register returns an error if sub is nil. Once registered, the subscriber
// will receive all subsequent events published to the bus until the returned
// subscription is closed.
//
// Example:
//
//	sub := &MySubscriber{}
//	subscription, err := bus.Register(sub)
//	if err != nil {
//	    return err
//	}
//	defer subscription.Close()
func (b *bus) Register(sub Subscriber) (Subscription, error) {
	if sub == nil {
		return nil, errors.New("subscriber is required")
	}
	s := &subscription{bus: b}
	b.mu.Lock()
	b.subscribers[s] = sub
	b.mu.Unlock()
	return s, nil
}

// Close removes the subscriber from the bus, ensuring it receives no further
// events. The method is idempotent and thread-safe: multiple calls to Close
// on the same subscription are safe and subsequent calls are no-ops.
//
// After Close returns, the subscriber will not receive any new events, though
// events already in progress may still be delivered if Close is called during
// a Publish operation.
//
// Close always returns nil to satisfy the Subscription interface.
func (s *subscription) Close() error {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subscribers, s)
		s.bus.mu.Unlock()
	})
	return nil
}
