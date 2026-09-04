// Package bridge registers a stream subscriber on the runtime hook bus.
package bridge

import (
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/stream"
)

// Register creates a subscriber for the selected events and registers it on
// bus. The returned subscription can be closed to detach the subscriber.
func Register(bus hooks.Bus, sink stream.Sink, profile stream.StreamProfile) (hooks.Subscription, error) {
	sub, err := stream.NewSubscriber(sink, profile)
	if err != nil {
		return nil, err
	}
	return bus.Register(sub)
}
