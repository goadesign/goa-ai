package stream_test

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/stream"
	streambridge "goa.design/goa-ai/runtime/agent/stream/bridge"
)

// collectSink is a simple in-memory sink used in examples to capture events.
type collectSink struct{ events []stream.Event }

func (s *collectSink) Send(ctx context.Context, e stream.Event) error {
	s.events = append(s.events, e)
	return nil
}
func (s *collectSink) Close(context.Context) error { return nil }

// Example demonstrating global broadcast streaming by configuring the runtime
// with a stream sink. The runtime automatically registers a hooks subscriber
// that forwards user-facing events to the sink.
func Example_broadcast() {
	ctx := context.Background()
	sink := &collectSink{}

	rt := agentsruntime.New(agentsruntime.WithRunEventStore(runloginmem.New()))

	// Attach a subscriber that forwards user-facing hook events to the sink.
	sub, _ := streambridge.Register(rt.Bus, sink)
	defer func() { _ = sub.Close() }()

	// Publish a user-facing hook event; the subscriber forwards it.
	_ = rt.Bus.Publish(ctx, hooks.NewAssistantMessageEvent("run-1", "svc.agent", "session-1", "hello", nil))

	// The sink received a typed stream event.
	fmt.Println(sink.events[0].Type())
	// Output: assistant_reply
}

// Example demonstrating per-request streaming by registering a temporary
// subscriber that bridges hooks events to a connection-scoped stream sink.
func Example_perRequest() {
	ctx := context.Background()
	bus := hooks.NewBus()
	sink := &collectSink{}

	// Attach a temporary subscriber for this request/connection.
	sub, _ := streambridge.Register(bus, sink)
	defer func() { _ = sub.Close() }()

	// Publish a planner note; the subscriber forwards it as a stream event.
	_ = bus.Publish(ctx, hooks.NewPlannerNoteEvent("run-1", "svc.agent", "", "thinking", nil))

	// The sink received a typed stream event.
	fmt.Println(sink.events[0].Type())
	// Output: planner_thought
}
