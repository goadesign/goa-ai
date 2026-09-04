package stream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// Subscriber receives persisted hook events and live model output events. It
	// maps supported hook events to stream events and sends the event kinds
	// selected by its profile to a trusted host through a Sink.
	//
	// Only the sink actually "sends" messages; the subscriber listens for
	// incoming events, translates those of interest, and hands them off to
	// the sink using its Send method.
	//
	// Supported events include assistant output, exact assistant turns, planner
	// thoughts, prompts, tool activity, usage, requests that pause a run,
	// authorization decisions, workflow state, and child-run links. Hook events
	// with no stream representation are ignored.
	Subscriber struct {
		sink    Sink
		profile StreamProfile
	}
)

// NewSubscriber constructs a subscriber that forwards the events selected by
// profile to sink. The sink delivers private runtime data to a trusted host;
// these events are not safe to forward unchanged to an end-user client.
//
// NewSubscriber returns an error when sink is nil or profile does not enable
// any events.
//
// Example:
//
//	sink := myStreamImplementation
//	sub, err := NewSubscriber(sink, RuntimeHostProfile())
//	if err != nil {
//	    return err
//	}
//	subscription, err := bus.Register(sub)
//	if err != nil {
//	    return err
//	}
//	defer func() {
//	    if err := subscription.Close(); err != nil {
//	        panic(err)
//	    }
//	}()
func NewSubscriber(sink Sink, profile StreamProfile) (*Subscriber, error) {
	if sink == nil {
		return nil, errors.New("stream sink is required")
	}
	if profile == (StreamProfile{}) {
		return nil, errors.New("stream profile must enable at least one event")
	}
	return &Subscriber{
		sink:    sink,
		profile: profile,
	}, nil
}

// HandleModelOutputEvent applies the configured profile to one live
// model text or thinking event and sends it to the same sink used for
// hook-derived events. The boolean reports whether the sink accepted the event.
func (s *Subscriber) HandleModelOutputEvent(ctx context.Context, event Event) (bool, error) {
	eventType := event.Type()
	if eventType != EventAssistantReply &&
		eventType != EventPlannerThought {
		return false, fmt.Errorf("unsupported live model output event %q", eventType)
	}
	if eventType == EventAssistantReply && !s.profile.Assistant {
		return false, nil
	}
	if eventType == EventPlannerThought && !s.profile.Thoughts {
		return false, nil
	}
	if err := s.sink.Send(ctx, event); err != nil {
		return false, err
	}
	return true, nil
}

// HandleEvent implements the Subscriber interface by translating hook events
// into stream events and forwarding them to the configured sink.
//
// It translates supported assistant, planner, prompt, tool, usage, pause,
// authorization, workflow, and child-run hook events. Unsupported hook event
// types are ignored.
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
		payload := UsagePayload{TokenUsage: evt.TokenUsage}
		return s.sink.Send(ctx, Usage{
			Base: newBaseFromHook(evt, EventUsage, payload),
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
			ExampleJSON:    evt.ExampleJSON,
		}
		return s.sink.Send(ctx, AwaitClarification{
			Base: newBaseFromHook(evt, EventAwaitClarification, payload),
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
			Base: newBaseFromHook(evt, EventAwaitConfirmation, payload),
			Data: payload,
		})
	case *hooks.AwaitQuestionsEvent:
		if !s.profile.AwaitQuestions {
			return nil
		}
		qs := make([]AwaitQuestionPayload, 0, len(evt.Questions))
		for _, q := range evt.Questions {
			opts := make([]AwaitQuestionOptionPayload, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, AwaitQuestionOptionPayload{
					ID:    o.ID,
					Label: o.Label,
				})
			}
			qs = append(qs, AwaitQuestionPayload{
				ID:            q.ID,
				Prompt:        q.Prompt,
				AllowMultiple: q.AllowMultiple,
				Options:       opts,
			})
		}
		payload := AwaitQuestionsPayload{
			ID:         evt.ID,
			ToolName:   string(evt.ToolName),
			ToolCallID: evt.ToolCallID,
			Title:      evt.Title,
			Questions:  qs,
		}
		return s.sink.Send(ctx, AwaitQuestions{
			Base: newBaseFromHook(evt, EventAwaitQuestions, payload),
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
			Base: newBaseFromHook(evt, EventAwaitExternalTools, payload),
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
			Base: newBaseFromHook(evt, EventToolAuthorization, payload),
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
			DisplayHint:           evt.DisplayHint,
		}
		return s.sink.Send(ctx, ToolStart{
			Base: newBaseFromHook(evt, EventToolStart, payload),
			Data: payload,
		})
	case *hooks.AssistantTurnCommittedEvent:
		if !s.profile.AssistantTurns {
			return nil
		}
		if evt.ResponseID == "" || len(evt.Messages) == 0 {
			return fmt.Errorf("assistant_turn_committed missing response id or messages for run %s", evt.RunID())
		}
		if evt.EventKey() != evt.ResponseID {
			return fmt.Errorf("assistant_turn_committed response id does not match event key for run %s", evt.RunID())
		}
		payload := AssistantTurnPayload{ResponseID: evt.ResponseID, Messages: evt.Messages}
		return s.sink.Send(ctx, AssistantTurn{
			Base: newBaseFromHook(evt, EventAssistantTurn, payload),
			Data: payload,
		})
	case *hooks.PlannerNoteEvent:
		if !s.profile.Thoughts {
			return nil
		}
		// Preserve the structured payload instead of wrapping it in a string.
		payload := PlannerThoughtPayload{
			Note: evt.Note,
		}
		return s.sink.Send(ctx, PlannerThought{
			Base: newBaseFromHook(evt, EventPlannerThought, payload),
			Data: payload,
		})
	case *hooks.PromptRenderedEvent:
		if !s.profile.PromptRendered {
			return nil
		}
		payload := PromptRenderedPayload{
			PromptID: evt.PromptID.String(),
			Version:  evt.Version,
			Scope:    evt.Scope,
		}
		return s.sink.Send(ctx, PromptRendered{
			Base: newBaseFromHook(evt, EventPromptRendered, payload),
			Data: payload,
		})
	case *hooks.ThinkingBlockEvent:
		if !s.profile.Thoughts {
			return nil
		}
		// Keep the complete provider-issued block for trusted hosts and exact
		// replay. Note is reserved for streaming deltas only.
		payload := PlannerThoughtPayload{
			Text:         evt.Text,
			Signature:    evt.Signature,
			Redacted:     evt.Redacted,
			ContentIndex: evt.ContentIndex,
			Final:        evt.Final,
		}
		// Emit Note only for non-final deltas so stream consumers do not
		// append the complete text again.
		if !evt.Final && evt.Text != "" {
			payload.Note = evt.Text
		}
		return s.sink.Send(ctx, PlannerThought{
			Base: newBaseFromHook(evt, EventPlannerThought, payload),
			Data: payload,
		})
	case *hooks.ToolResultReceivedEvent:
		if !s.profile.ToolEnd {
			return nil
		}
		if evt.CallRunID == "" {
			return errors.New("stream: tool_end missing call_run_id")
		}
		if evt.ToolCallID == "" {
			return errors.New("stream: tool_end missing tool_call_id")
		}
		if evt.ToolName == "" {
			return errors.New("stream: tool_end missing tool_name")
		}
		payload := ToolEndPayload{
			CallRunID:        evt.CallRunID,
			ToolCallID:       evt.ToolCallID,
			ParentToolCallID: evt.ParentToolCallID,
			ToolName:         string(evt.ToolName),
			Result:           evt.ResultJSON,
			Bounds:           evt.Bounds,
			Duration:         evt.Duration,
			Telemetry:        evt.Telemetry,
			Failure:          evt.Failure,
		}
		if preview := clampPreview(evt.ResultPreview); preview != "" {
			payload.ResultPreview = preview
		}
		return s.sink.Send(ctx, ToolEnd{
			Base:       newBaseFromHook(evt, EventToolEnd, payload),
			ServerData: append(rawjson.Message(nil), evt.ServerData...),
			Data:       payload,
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
			Base: newBaseFromHook(evt, EventToolUpdate, up),
			Data: up,
		})
	case *hooks.ChildRunLinkedEvent:
		if !s.profile.ChildRuns {
			return nil
		}
		payload := ChildRunLinkedPayload{
			ToolName:     string(evt.ToolName),
			ToolCallID:   evt.ToolCallID,
			ChildRunID:   evt.ChildRunID,
			ChildAgentID: evt.ChildAgentID,
		}
		return s.sink.Send(ctx, ChildRunLinked{
			Base: newBaseFromHook(evt, EventChildRunLinked, payload),
			Data: payload,
		})
	case *hooks.RunCompletedEvent:
		if !s.profile.Workflow {
			return nil
		}
		phase := string(evt.Phase)
		if phase == "" {
			return fmt.Errorf("run_completed event missing phase for run %s", evt.RunID())
		}
		payload := WorkflowPayload{
			Phase:        phase,
			Status:       evt.Status,
			Failure:      evt.Failure,
			Cancellation: evt.Cancellation,
		}
		return s.sendTerminalWorkflow(ctx, evt, payload)
	case *hooks.RunSuspendedEvent:
		if !s.profile.Workflow {
			return nil
		}
		return s.sendTerminalWorkflow(ctx, evt, WorkflowPayload{
			Phase:  string(run.PhaseSuspended),
			Status: string(run.StatusSuspended),
		})
	case *hooks.RunPhaseChangedEvent:
		if !s.profile.Workflow {
			return nil
		}
		// Terminal lifecycle is streamed via RunCompletedEvent or RunSuspendedEvent.
		// Avoid emitting a second terminal workflow event for the same run.
		if evt.Phase == run.PhaseCompleted || evt.Phase == run.PhaseFailed || evt.Phase == run.PhaseCanceled || evt.Phase == run.PhaseSuspended {
			return nil
		}
		payload := WorkflowPayload{
			Phase: string(evt.Phase),
		}
		return s.sink.Send(ctx, Workflow{
			Base: newBaseFromHook(evt, EventWorkflow, payload),
			Data: payload,
		})
	default:
		return nil
	}
}

// sendTerminalWorkflow emits the final workflow state followed by the marker
// that tells a run-scoped stream consumer to close.
func (s *Subscriber) sendTerminalWorkflow(ctx context.Context, event hooks.Event, payload WorkflowPayload) error {
	if err := s.sink.Send(ctx, Workflow{
		Base: newBaseFromHook(event, EventWorkflow, payload),
		Data: payload,
	}); err != nil {
		return err
	}
	// One terminal hook event produces both the final workflow state and the
	// marker that closes this run's live stream. Give the marker its own stable
	// key so exact publication can retry both entries without treating their
	// different types and payloads as conflicting content.
	endPayload := RunStreamEndPayload{}
	return s.sink.Send(ctx, RunStreamEnd{
		Base: NewBaseWithEventKey(
			EventRunStreamEnd,
			event.RunID(),
			event.SessionID(),
			endPayload,
			fmt.Sprintf("%s/%s", event.EventKey(), EventRunStreamEnd),
			time.UnixMilli(event.Timestamp()).UTC(),
		),
		Data: endPayload,
	})
}

func newBaseFromHook(evt hooks.Event, eventType EventType, payload any) Base {
	return NewBaseWithEventKey(
		eventType,
		evt.RunID(),
		evt.SessionID(),
		payload,
		evt.EventKey(),
		time.UnixMilli(evt.Timestamp()).UTC(),
	)
}

// clampPreview normalizes whitespace and limits result previews to a concise
// size for a host-created public summary.
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
