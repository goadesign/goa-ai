package stream_test

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/stream"
)

// collectSink is a simple in-memory sink used in examples to capture events.
type collectSink struct{ events []stream.Event }

func (s *collectSink) Send(_ context.Context, e stream.Event) error {
	s.events = append(s.events, e)
	return nil
}
func (s *collectSink) Close(context.Context) error { return nil }

// Example demonstrating host-owned streaming by registering a subscriber on
// the runtime event bus. These events still require an application-owned public
// contract before they can be sent to a browser.
func Example_broadcast() {
	ctx := context.Background()
	sink := &collectSink{}

	rt := agentsruntime.New(storageinmem.New())

	// Attach the trusted-host profile, which excludes separate diagnostic thoughts.
	subscriber, err := stream.NewSubscriber(sink, stream.RuntimeHostProfile())
	if err != nil {
		panic(err)
	}
	sub, err := rt.Bus.Register(subscriber)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := sub.Close(); err != nil {
			panic(err)
		}
	}()

	// Publish a completed assistant turn; the subscriber forwards it.
	if err := rt.Bus.Publish(ctx, hooks.NewAssistantTurnCommittedEvent(
		"run-1",
		"svc.agent",
		"session-1",
		"00000000-0000-4000-8000-000000000001",
		[]*model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
	)); err != nil {
		panic(err)
	}

	// The sink received a typed stream event.
	fmt.Println(sink.events[0].Type())
	// Output: assistant_turn
}

// Example demonstrating a restricted diagnostic subscriber. Provider thinking
// can contain sensitive internal reasoning and must not feed an end-user interface.
func Example_diagnosticSubscriber() {
	ctx := context.Background()
	bus := hooks.NewBus()
	sink := &collectSink{}

	// Attach the all-events profile only to the restricted diagnostic sink.
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	if err != nil {
		panic(err)
	}
	sub, err := bus.Register(subscriber)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := sub.Close(); err != nil {
			panic(err)
		}
	}()

	// Publish a planner note; the subscriber forwards it as a stream event.
	if err := bus.Publish(ctx, hooks.NewPlannerNoteEvent(
		"run-1",
		"svc.agent",
		"",
		"thinking",
		nil,
	)); err != nil {
		panic(err)
	}

	// The sink received a typed stream event.
	fmt.Println(sink.events[0].Type())
	// Output: planner_thought
}
