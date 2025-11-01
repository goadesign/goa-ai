// Package bridge provides a discoverable entrypoint to wire the runtime hook bus
// to a stream.Sink without importing the hooks subscriber directly. It avoids
// coupling the stream and hooks packages while giving users a simple API.
package bridge

import (
	"goa.design/goa-ai/runtime/agents/hooks"
	"goa.design/goa-ai/runtime/agents/stream"
)

// NewSubscriber returns a hooks.Subscriber that forwards selected hook events
// (assistant replies, planner thoughts, tool start/end) to the provided sink
// as typed stream.Event values.
func NewSubscriber(sink stream.Sink) (hooks.Subscriber, error) {
	return hooks.NewStreamSubscriber(sink)
}

// Register creates a stream subscriber for the given sink and registers it
// on the provided bus. The returned subscription can be closed to detach the
// subscriber.
func Register(bus hooks.Bus, sink stream.Sink) (hooks.Subscription, error) {
	sub, err := NewSubscriber(sink)
	if err != nil {
		return nil, err
	}
	return bus.Register(sub)
}
