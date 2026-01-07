package stream

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
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
		sink    Sink
		profile StreamProfile
	}
)

// NewSubscriber constructs a subscriber that forwards selected hook
// events to the provided stream sink using the default stream profile.
// The sink is typically backed by a message bus like Pulse or a direct
// WebSocket/SSE connection.
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
	return NewSubscriberWithProfile(sink, DefaultProfile())
}

// NewSubscriberWithProfile constructs a subscriber that forwards selected
// hook events to the provided stream sink, applying the given StreamProfile
// to determine which event kinds are emitted.
func NewSubscriberWithProfile(sink Sink, profile StreamProfile) (*Subscriber, error) {
	if sink == nil {
		return nil, errors.New("stream sink is required")
	}
	return &Subscriber{
		sink:    sink,
		profile: profile,
	}, nil
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
		if !s.profile.Usage {
			return nil
		}
		payload := UsagePayload{
			Model:            evt.Model,
			InputTokens:      evt.InputTokens,
			OutputTokens:     evt.OutputTokens,
			TotalTokens:      evt.TotalTokens,
			CacheReadTokens:  evt.CacheReadTokens,
			CacheWriteTokens: evt.CacheWriteTokens,
		}
		return s.sink.Send(ctx, Usage{
			Base: Base{t: EventUsage, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.AwaitClarificationEvent:
		if !s.profile.AwaitClarification {
			return nil
		}
		payload := AwaitClarificationPayload{
			ID:             evt.ID,
			Question:       evt.Question,
			MissingFields:  append([]string(nil), evt.MissingFields...),
			RestrictToTool: string(evt.RestrictToTool),
			ExampleInput:   evt.ExampleInput,
		}
		return s.sink.Send(ctx, AwaitClarification{
			Base: Base{t: EventAwaitClarification, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.AwaitConfirmationEvent:
		if !s.profile.AwaitConfirmation {
			return nil
		}
		payload := AwaitConfirmationPayload{
			ID:         evt.ID,
			Title:      evt.Title,
			Prompt:     evt.Prompt,
			ToolName:   string(evt.ToolName),
			ToolCallID: evt.ToolCallID,
			Payload:    evt.Payload,
		}
		return s.sink.Send(ctx, AwaitConfirmation{
			Base: Base{t: EventAwaitConfirmation, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.AwaitExternalToolsEvent:
		if !s.profile.AwaitExternalTools {
			return nil
		}
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
			Base: Base{t: EventAwaitExternalTools, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolAuthorizationEvent:
		if !s.profile.ToolAuthorization {
			return nil
		}
		payload := ToolAuthorizationPayload{
			ToolName:   string(evt.ToolName),
			ToolCallID: evt.ToolCallID,
			Approved:   evt.Approved,
			Summary:    evt.Summary,
			ApprovedBy: evt.ApprovedBy,
		}
		return s.sink.Send(ctx, ToolAuthorization{
			Base: Base{t: EventToolAuthorization, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolCallScheduledEvent:
		if !s.profile.ToolStart {
			return nil
		}
		payload := ToolStartPayload{
			ToolCallID:            evt.ToolCallID,
			ToolName:              string(evt.ToolName),
			Payload:               evt.Payload,
			Queue:                 evt.Queue,
			ParentToolCallID:      evt.ParentToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
			DisplayHint:           rthints.FormatCallHint(evt.ToolName, evt.Payload),
		}
		return s.sink.Send(ctx, ToolStart{
			Base: Base{t: EventToolStart, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.AssistantMessageEvent:
		if !s.profile.Assistant {
			return nil
		}
		// Publish a typed payload object on the wire (no string-wrapping).
		payload := AssistantReplyPayload{
			Text: evt.Message,
		}
		return s.sink.Send(ctx, AssistantReply{
			Base: Base{t: EventAssistantReply, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.PlannerNoteEvent:
		if !s.profile.Thoughts {
			return nil
		}
		// Publish a typed payload object on the wire (no string-wrapping).
		payload := PlannerThoughtPayload{
			Note: evt.Note,
		}
		return s.sink.Send(ctx, PlannerThought{
			Base: Base{t: EventPlannerThought, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.ThinkingBlockEvent:
		if !s.profile.Thoughts {
			return nil
		}
		// Map structured thinking block to PlannerThought with enriched payload.
		// Text/Signature/Redacted always carry the provider-issued block for
		// ledger and replay. Note is reserved for streaming deltas only.
		payload := PlannerThoughtPayload{
			Text:         evt.Text,
			Signature:    evt.Signature,
			Redacted:     evt.Redacted,
			ContentIndex: evt.ContentIndex,
			Final:        evt.Final,
		}
		// Emit Note only for non-final deltas so UIs can append incremental
		// reasoning without duplicating the final aggregated block.
		if !evt.Final && evt.Text != "" {
			payload.Note = evt.Text
		}
		return s.sink.Send(ctx, PlannerThought{
			Base: Base{t: EventPlannerThought, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolResultReceivedEvent:
		if !s.profile.ToolEnd {
			return nil
		}
		if evt.ToolCallID == "" {
			return errors.New("stream: tool_end missing tool_call_id")
		}
		if evt.ToolName == "" {
			return errors.New("stream: tool_end missing tool_name")
		}
		if evt.Error == nil && evt.Result == nil {
			return fmt.Errorf("stream: tool_end %s missing result (success without result)", evt.ToolName)
		}
		var artifacts []ArtifactPayload
		if len(evt.Artifacts) > 0 {
			artifacts = make([]ArtifactPayload, 0, len(evt.Artifacts))
			for _, a := range evt.Artifacts {
				if a.Kind == "" {
					return fmt.Errorf("stream: tool_end %s has artifact with empty kind", evt.ToolName)
				}
				if a.SourceTool == "" {
					return fmt.Errorf("stream: tool_end %s has artifact %q with empty source_tool", evt.ToolName, a.Kind)
				}
				ap := ArtifactPayload{
					Kind:       a.Kind,
					Data:       a.Data,
					SourceTool: string(a.SourceTool),
				}
				if a.RunLink != nil {
					ap.RunLink = &RunLinkPayload{
						RunID:            a.RunLink.RunID,
						AgentID:          a.RunLink.AgentID,
						ParentRunID:      a.RunLink.ParentRunID,
						ParentToolCallID: a.RunLink.ParentToolCallID,
					}
				}
				artifacts = append(artifacts, ap)
			}
		}
		payload := ToolEndPayload{
			ToolCallID:       evt.ToolCallID,
			ParentToolCallID: evt.ParentToolCallID,
			ToolName:         string(evt.ToolName),
			Result:           evt.Result,
			Bounds:           evt.Bounds,
			Artifacts:        artifacts,
			Duration:         evt.Duration,
			Telemetry:        evt.Telemetry,
			Error:            evt.Error,
		}
		if preview := clampPreview(evt.ResultPreview); preview != "" {
			payload.ResultPreview = preview
		}
		return s.sink.Send(ctx, ToolEnd{
			Base: Base{t: EventToolEnd, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.ToolCallUpdatedEvent:
		if !s.profile.ToolUpdate {
			return nil
		}
		up := ToolUpdatePayload{
			ToolCallID:            evt.ToolCallID,
			ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
		}
		return s.sink.Send(ctx, ToolUpdate{
			Base: Base{t: EventToolUpdate, r: evt.RunID(), s: evt.SessionID(), p: up},
			Data: up,
		})
	case *hooks.AgentRunStartedEvent:
		if !s.profile.AgentRuns {
			return nil
		}
		payload := AgentRunStartedPayload{
			ToolName:     string(evt.ToolName),
			ToolCallID:   evt.ToolCallID,
			ChildRunID:   evt.ChildRunID,
			ChildAgentID: evt.ChildAgentID,
		}
		return s.sink.Send(ctx, AgentRunStarted{
			Base: Base{t: EventAgentRunStarted, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.RunCompletedEvent:
		if !s.profile.Workflow {
			return nil
		}
		// Prefer the terminal run phase when present; fall back to status for
		// back-compat with older emitters/tests.
		phase := string(evt.Phase)
		if phase == "" {
			switch evt.Status {
			case "success":
				phase = "completed"
			case "failed":
				phase = "failed"
			case "canceled":
				phase = "canceled"
			default:
				phase = evt.Status
			}
		}
		payload := WorkflowPayload{
			Phase:          phase,
			Status:         evt.Status,
			ErrorProvider:  evt.ErrorProvider,
			ErrorOperation: evt.ErrorOperation,
			ErrorKind:      evt.ErrorKind,
			ErrorCode:      evt.ErrorCode,
			HTTPStatus:     evt.HTTPStatus,
			Retryable:      evt.Retryable,
		}
		if evt.Error != nil {
			payload.Error = evt.Error.Error()
		}
		return s.sink.Send(ctx, Workflow{
			Base: Base{t: EventWorkflow, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	case *hooks.RunPhaseChangedEvent:
		if !s.profile.Workflow {
			return nil
		}
		// Terminal lifecycle is streamed via RunCompletedEvent (which also carries status).
		// Avoid emitting a second terminal workflow event for the same run.
		if evt.Phase == run.PhaseCompleted || evt.Phase == run.PhaseFailed || evt.Phase == run.PhaseCanceled {
			return nil
		}
		payload := WorkflowPayload{
			Phase: string(evt.Phase),
		}
		return s.sink.Send(ctx, Workflow{
			Base: Base{t: EventWorkflow, r: evt.RunID(), s: evt.SessionID(), p: payload},
			Data: payload,
		})
	default:
		return nil
	}
}

// clampPreview normalizes whitespace and clamps result previews to a reasonable
// length for UI display.
func clampPreview(in string) string {
	if in == "" {
		return ""
	}
	// normalize whitespace
	out := make([]rune, 0, len(in))
	prevSpace := false
	for _, r := range in {
		switch r {
		case '\n', '\r', '\t', ' ':
			if !prevSpace {
				out = append(out, ' ')
			}
			prevSpace = true
		default:
			out = append(out, r)
			prevSpace = false
		}
	}
	const max = 140
	if len(out) <= max {
		return string(out)
	}
	return string(out[:max])
}
