package stream

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/hooks"
)

type (
	// Subscriber receives runtime events and forwards certain ones to a
	// stream.Sink, such as a WebSocket, SSE, or message bus. It acts as a
	// bridge between the internal event bus and an external stream client.
	//
	// Only the sink actually "sends" messages; the subscriber listens for
	// incoming events, translates those of interest, and hands them off to
	// the sink using its Send method.
	//
	// The following hook events are streamed to clients:
	//   - AssistantMessage      → EventAssistantReply
	//   - PlannerNote           → EventPlannerThought
	//   - ToolCallScheduled     → EventToolStart
	//   - ToolCallUpdated       → EventToolUpdate
	//   - ToolResultReceived    → EventToolEnd
	//
	// All other (internal) events, such as workflow lifecycle changes, are
	// ignored and not sent to clients.
	Subscriber struct {
		sink Sink
	}
)

// NewSubscriber constructs a subscriber that forwards selected hook
// events to the provided stream sink. The sink is typically backed by a
// message bus like Pulse or a direct WebSocket/SSE connection.
//
// NewSubscriber returns an error if sink is nil, as the subscriber
// requires a valid sink to function.
//
// Example:
//
//	sink := myStreamImplementation
//	sub, err := hooks.NewSubscriber(sink)
//	if err != nil {
//	    return err
//	}
//	subscription, _ := bus.Register(sub)
//	defer subscription.Close()
func NewSubscriber(sink Sink) (*Subscriber, error) {
	if sink == nil {
		return nil, errors.New("stream sink is required")
	}
	return &Subscriber{sink: sink}, nil
}

// HandleEvent implements the Subscriber interface by translating hook events
// into stream events and forwarding them to the configured sink.
//
// Event translation:
//   - AssistantMessage → EventAssistantReply
//   - PlannerNote → EventPlannerThought
//   - ToolCallScheduled → EventToolStart
//   - ToolCallUpdated → EventToolUpdate
//   - ToolResultReceived → EventToolEnd
//   - All other event types are ignored (return nil)
//
// If the sink returns an error, HandleEvent propagates it to the bus, which
// stops event delivery to remaining subscribers. This fail-fast behavior
// ensures that streaming failures are visible to the runtime.
func (s *Subscriber) HandleEvent(ctx context.Context, event hooks.Event) error {
	switch evt := event.(type) {
	case *hooks.UsageEvent:
		payload := UsagePayload{
			Model:        evt.Model,
			InputTokens:  evt.InputTokens,
			OutputTokens: evt.OutputTokens,
			TotalTokens:  evt.TotalTokens,
		}
		return s.sink.Send(ctx, Usage{Base: Base{t: EventUsage, r: evt.RunID(), p: payload}, Data: payload})
	case *hooks.AwaitClarificationEvent:
		payload := AwaitClarificationPayload{
			ID:             evt.ID,
			Question:       evt.Question,
			MissingFields:  append([]string(nil), evt.MissingFields...),
			RestrictToTool: string(evt.RestrictToTool),
			ExampleInput:   evt.ExampleInput,
		}
		return s.sink.Send(ctx, AwaitClarification{
			Base: Base{t: EventAwaitClarification, r: evt.RunID(), p: payload},
			Data: payload,
		})
	case *hooks.AwaitExternalToolsEvent:
		items := make([]AwaitToolPayload, 0, len(evt.Items))
		for _, it := range evt.Items {
			items = append(items, AwaitToolPayload{
				ToolName:   string(it.ToolName),
				ToolCallID: it.ToolCallID,
				Payload:    it.Payload,
			})
		}
		payload := AwaitExternalToolsPayload{ID: evt.ID, Items: items}
		return s.sink.Send(ctx, AwaitExternalTools{
			Base: Base{t: EventAwaitExternalTools, r: evt.RunID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolCallScheduledEvent:
		payload := ToolStartPayload{
			ToolCallID:            evt.ToolCallID,
			ToolName:              string(evt.ToolName),
			Payload:               evt.Payload,
			Queue:                 evt.Queue,
			ParentToolCallID:      evt.ParentToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
		}
		return s.sink.Send(ctx, ToolStart{
			Base: Base{t: EventToolStart, r: evt.RunID(), p: payload},
			Data: payload,
		})
	case *hooks.AssistantMessageEvent:
		return s.sink.Send(ctx, AssistantReply{
			Base: Base{t: EventAssistantReply, r: evt.RunID(), p: evt.Message},
			Text: evt.Message,
		})
	case *hooks.PlannerNoteEvent:
		return s.sink.Send(ctx, PlannerThought{
			Base: Base{t: EventPlannerThought, r: evt.RunID(), p: evt.Note},
			Note: evt.Note,
		})
	case *hooks.ToolResultReceivedEvent:
		payload := ToolEndPayload{
			ToolCallID:       evt.ToolCallID,
			ParentToolCallID: evt.ParentToolCallID,
			ToolName:         string(evt.ToolName),
			Result:           evt.Result,
			Duration:         evt.Duration,
			Telemetry:        evt.Telemetry,
			Error:            evt.Error,
		}
		return s.sink.Send(ctx, ToolEnd{
			Base: Base{t: EventToolEnd, r: evt.RunID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolCallUpdatedEvent:
		up := ToolUpdatePayload{
			ToolCallID:            evt.ToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
		}
		return s.sink.Send(ctx, ToolUpdate{
			Base: Base{t: EventToolUpdate, r: evt.RunID(), p: up},
			Data: up,
		})
	default:
		return nil
	}
}
