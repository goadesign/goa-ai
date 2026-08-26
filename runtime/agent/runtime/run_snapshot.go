// Package runtime rebuilds the compact run state returned by Runtime from
// canonical run-log events supplied in oldest-first order.
package runtime

import (
	"fmt"
	"sort"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// newRunSnapshot derives a compact run state snapshot by replaying canonical
// run log events in order. The caller must supply events ordered oldest-first.
func newRunSnapshot(events []*runlog.Event) (*run.Snapshot, error) {
	if len(events) == 0 {
		return nil, run.ErrNotFound
	}
	for index, event := range events {
		if event == nil {
			return nil, fmt.Errorf("snapshot run log contains nil event at index %d", index)
		}
	}

	s := &run.Snapshot{
		RunID:     events[0].RunID,
		AgentID:   events[0].AgentID,
		SessionID: events[0].SessionID,
		TurnID:    events[0].TurnID,
		Status:    run.StatusRunning,
		Phase:     run.PhasePrompted,
		StartedAt: events[0].Timestamp,
		UpdatedAt: events[0].Timestamp,
	}
	toolCalls := make(map[string]*run.ToolCallSnapshot)
	activeToolCalls := make(map[string]*run.ToolCallSnapshot)
	scheduledToolEvents := make(map[string]*hooks.ToolCallScheduledEvent)
	completedToolCallIDs := make(map[string]struct{})

	for _, e := range events {
		if e.RunID != s.RunID {
			return nil, fmt.Errorf("snapshot events contain multiple run IDs (%q, %q)", s.RunID, e.RunID)
		}
		if s.AgentID == "" && e.AgentID != "" {
			s.AgentID = e.AgentID
		}
		if s.SessionID == "" && e.SessionID != "" {
			s.SessionID = e.SessionID
		}
		if s.TurnID == "" && e.TurnID != "" {
			s.TurnID = e.TurnID
		}
		if e.Timestamp.Before(s.StartedAt) {
			s.StartedAt = e.Timestamp
		}
		if e.Timestamp.After(s.UpdatedAt) {
			s.UpdatedAt = e.Timestamp
		}

		switch e.Type {
		case hooks.RunStarted,
			hooks.ChildRunLinked,
			hooks.AwaitClarification,
			hooks.AwaitConfirmation,
			hooks.AwaitExternalTools,
			hooks.RunPhaseChanged,
			hooks.AssistantMessage,
			hooks.ToolCallScheduled,
			hooks.ToolCallUpdated,
			hooks.ToolResultReceived,
			hooks.RunCompleted,
			hooks.RunSuspended:
		default:
			// Most event types do not affect the snapshot; transcript events are
			// replayed by transcript.ReplayRunLogEvents below.
			continue
		}

		decoded, err := hooks.DecodeRunlogEvent(e)
		if err != nil {
			return nil, err
		}
		switch p := decoded.(type) {
		case *hooks.RunStartedEvent:
			s.Labels = p.RunContext.Labels

		case *hooks.ChildRunLinkedEvent:
			s.ChildRuns = append(s.ChildRuns, &run.ChildRunLink{
				ToolName:     p.ToolName,
				ToolCallID:   p.ToolCallID,
				ChildRunID:   p.ChildRunID,
				ChildAgentID: p.ChildAgentID,
			})

		case *hooks.AwaitClarificationEvent:
			s.Await = &run.AwaitSnapshot{
				Kind:     string(hooks.AwaitClarification),
				ID:       p.ID,
				ToolName: p.RestrictToTool,
				Question: p.Question,
			}

		case *hooks.AwaitConfirmationEvent:
			s.Await = &run.AwaitSnapshot{
				Kind:       string(hooks.AwaitConfirmation),
				ID:         p.ID,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Title:      p.Title,
				Prompt:     p.Prompt,
			}

		case *hooks.AwaitExternalToolsEvent:
			s.Await = &run.AwaitSnapshot{
				Kind:      string(hooks.AwaitExternalTools),
				ID:        p.ID,
				ItemCount: len(p.Items),
			}

		case *hooks.RunPhaseChangedEvent:
			s.Phase = p.Phase

		case *hooks.AssistantMessageEvent:
			s.LastAssistantMessage = p.Message

		case *hooks.ToolCallScheduledEvent:
			if _, ok := toolCalls[p.ToolCallID]; ok {
				return nil, fmt.Errorf("duplicate tool schedule for call %q", p.ToolCallID)
			}
			tc := &run.ToolCallSnapshot{
				ToolCallID:            p.ToolCallID,
				ToolName:              p.ToolName,
				ParentToolCallID:      p.ParentToolCallID,
				ScheduledAt:           e.Timestamp,
				ExpectedChildrenTotal: p.ExpectedChildrenTotal,
			}
			toolCalls[p.ToolCallID] = tc
			activeToolCalls[p.ToolCallID] = tc
			scheduledToolEvents[p.ToolCallID] = p

			if p.ParentToolCallID != "" {
				parent, ok := activeToolCalls[p.ParentToolCallID]
				if !ok {
					return nil, fmt.Errorf(
						"tool schedule for call %q requires active parent schedule %q",
						p.ToolCallID,
						p.ParentToolCallID,
					)
				}
				parent.ObservedChildrenTotal++
			}

		case *hooks.ToolCallUpdatedEvent:
			if _, ok := completedToolCallIDs[p.ToolCallID]; ok {
				return nil, fmt.Errorf("tool update follows completion for call %q", p.ToolCallID)
			}
			tc, ok := activeToolCalls[p.ToolCallID]
			if !ok {
				return nil, fmt.Errorf("tool update requires active schedule for call %q", p.ToolCallID)
			}
			tc.ExpectedChildrenTotal = p.ExpectedChildrenTotal

		case *hooks.ToolResultReceivedEvent:
			if _, ok := completedToolCallIDs[p.ToolCallID]; ok {
				return nil, fmt.Errorf("tool result follows completion for call %q", p.ToolCallID)
			}
			tc := activeToolCalls[p.ToolCallID]
			if err := hooks.ValidateToolResultPlacement(
				s.RunID,
				scheduledToolEvents[p.ToolCallID],
				p,
			); err != nil {
				return nil, fmt.Errorf(
					"tool result placement is invalid for call %q: %w",
					p.ToolCallID,
					err,
				)
			}
			if p.CallRunID == s.RunID {
				delete(activeToolCalls, p.ToolCallID)
			} else {
				// The matching schedule belongs to CallRunID. This run records
				// only the supplied result and must not count a child locally.
				tc = &run.ToolCallSnapshot{
					ToolCallID: p.ToolCallID,
				}
				toolCalls[p.ToolCallID] = tc
			}
			tc.ToolName = p.ToolName
			tc.ParentToolCallID = p.ParentToolCallID
			tc.CompletedAt = e.Timestamp
			tc.Duration = p.Duration
			if p.Failure != nil {
				tc.ErrorSummary = p.Failure.Error.Message
			}
			completedToolCallIDs[p.ToolCallID] = struct{}{}

		case *hooks.RunCompletedEvent:
			s.Phase = p.Phase
			s.Await = nil
			switch p.Status {
			case runStatusSuccess:
				s.Status = run.StatusCompleted
			case runStatusFailed:
				s.Status = run.StatusFailed
			case runStatusCanceled:
				s.Status = run.StatusCanceled
			default:
				return nil, fmt.Errorf("unsupported run completion status %q", p.Status)
			}

		case *hooks.RunSuspendedEvent:
			s.Status = run.StatusSuspended
			s.Phase = run.PhaseSuspended

		default:
			return nil, fmt.Errorf("snapshot hook event %q decoded as unexpected type %T", e.Type, decoded)
		}
	}

	if len(toolCalls) > 0 {
		s.ToolCalls = make([]*run.ToolCallSnapshot, 0, len(toolCalls))
		for _, v := range toolCalls {
			s.ToolCalls = append(s.ToolCalls, v)
		}
		sort.Slice(s.ToolCalls, func(i, j int) bool {
			a := s.ToolCalls[i]
			b := s.ToolCalls[j]
			if !a.ScheduledAt.Equal(b.ScheduledAt) {
				return a.ScheduledAt.Before(b.ScheduledAt)
			}
			return a.ToolCallID < b.ToolCallID
		})
	}

	transcriptMessages, foundTranscript, err := transcript.ReplayRunLogEvents(events)
	if err != nil {
		return nil, err
	}
	if foundTranscript {
		s.Transcript = transcriptMessages
		for i := len(transcriptMessages) - 1; i >= 0; i-- {
			msg := transcriptMessages[i]
			if msg == nil || msg.Role != model.ConversationRoleAssistant {
				continue
			}
			if text := agentMessageText(msg); text != "" {
				s.LastAssistantMessage = text
				break
			}
		}
	}

	return s, nil
}
