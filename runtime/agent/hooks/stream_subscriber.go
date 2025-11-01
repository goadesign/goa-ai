package hooks

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/stream"
)

type (
	// StreamSubscriber is a Subscriber implementation that RECEIVES hook events
	// and forwards selected ones to a stream.Sink. Think of it as a bridge
	// between the internal observability bus and an external streaming transport
	// (SSE, WebSockets, Pulse, etc.).
	//
	// Naming note: only the sink exposes a Send method. The subscriber itself does
	// not "send"; it handles incoming hook events and calls sink.Send under the
	// hood. This separation avoids confusion between receiving from the bus and
	// transmitting to the client transport.
	//
	// Forwarded (client‑facing) events:
	//   - AssistantMessage → EventAssistantReply
	//   - PlannerNote → EventPlannerThought
	//   - ToolCallScheduled → EventToolStart
	//   - ToolCallUpdated → EventToolUpdate
	//   - ToolResultReceived → EventToolEnd
	//
	// Internal‑only events (e.g., workflow lifecycle) are ignored as they are
	// primarily used for observability, not client streaming.
	StreamSubscriber struct {
		sink stream.Sink
	}
)

// NewStreamSubscriber constructs a subscriber that forwards selected hook
// events to the provided stream sink. The sink is typically backed by a
// message bus like Pulse or a direct WebSocket/SSE connection.
//
// NewStreamSubscriber returns an error if sink is nil, as the subscriber
// requires a valid sink to function.
//
// Example:
//
//	sink := myStreamImplementation
//	sub, err := hooks.NewStreamSubscriber(sink)
//	if err != nil {
//	    return err
//	}
//	subscription, _ := bus.Register(sub)
//	defer subscription.Close()
func NewStreamSubscriber(sink stream.Sink) (Subscriber, error) {
	if sink == nil {
		return nil, errors.New("stream sink is required")
	}
	return &StreamSubscriber{sink: sink}, nil
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
func (s *StreamSubscriber) HandleEvent(ctx context.Context, event Event) error {
	switch evt := event.(type) {
	case *ToolCallScheduledEvent:
		payload := stream.ToolStartPayload{
			ToolCallID:            evt.ToolCallID,
			ToolName:              evt.ToolName,
			Payload:               evt.Payload,
			Queue:                 evt.Queue,
			ParentToolCallID:      evt.ParentToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
		}
		return s.sink.Send(ctx, stream.ToolStart{
			Base: stream.Base{T: stream.EventToolStart, R: evt.RunID(), P: payload},
			Data: payload,
		})
	case *AssistantMessageEvent:
		return s.sink.Send(ctx, stream.AssistantReply{
			Base: stream.Base{T: stream.EventAssistantReply, R: evt.RunID(), P: evt.Message},
			Text: evt.Message,
		})
	case *PlannerNoteEvent:
		return s.sink.Send(ctx, stream.PlannerThought{
			Base: stream.Base{T: stream.EventPlannerThought, R: evt.RunID(), P: evt.Note},
			Note: evt.Note,
		})
	case *ToolResultReceivedEvent:
		payload := stream.ToolEndPayload{
			ToolCallID:       evt.ToolCallID,
			ParentToolCallID: evt.ParentToolCallID,
			ToolName:         evt.ToolName,
			Result:           evt.Result,
			Duration:         evt.Duration,
			Telemetry:        evt.Telemetry,
			Error:            evt.Error,
		}
		return s.sink.Send(ctx, stream.ToolEnd{
			Base: stream.Base{T: stream.EventToolEnd, R: evt.RunID(), P: payload},
			Data: payload,
		})
	case *ToolCallUpdatedEvent:
		up := stream.ToolUpdatePayload{
			ToolCallID:            evt.ToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
		}
		return s.sink.Send(ctx, stream.ToolUpdate{
			Base: stream.Base{T: stream.EventToolUpdate, R: evt.RunID(), P: up},
			Data: up,
		})
	default:
		return nil
	}
}
