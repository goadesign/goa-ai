package hooks

import (
    "context"
    "errors"

    "goa.design/goa-ai/agents/runtime/stream"
)

type (
	// StreamSubscriber is a Subscriber implementation that bridges hook events
	// to a stream.Sink, enabling real-time updates to be pushed to clients
	// (e.g., via Server-Sent Events or WebSockets).
	//
	// Only user-facing events are forwarded to the stream:
	//   - AssistantMessage → EventAssistantReply
	//   - PlannerNote → EventPlannerThought
    //   - ToolResultReceived → EventToolEnd
	//
	// Other hook events (WorkflowStarted, WorkflowCompleted, etc.) are silently
	// ignored as they are typically used for internal observability rather than
	// client streaming.
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
//   - AssistantMessage events are sent as EventAssistantReply
//   - PlannerNote events are sent as EventPlannerThought
    //   - ToolResultReceived events are sent as EventToolEnd
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
    default:
        return nil
    }
}
