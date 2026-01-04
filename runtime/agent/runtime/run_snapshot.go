package runtime

import (
	"encoding/json"
	"fmt"
	"sort"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
)

// newRunSnapshot derives a compact run state snapshot by replaying canonical
// run log events in order. The caller must supply events ordered oldest-first.
func newRunSnapshot(events []*runlog.Event) (*run.Snapshot, error) {
	if len(events) == 0 {
		return nil, run.ErrNotFound
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

		//nolint:exhaustive // Snapshot intentionally derives state from a small subset of events.
		switch e.Type {
		case hooks.AgentRunStarted:
			var p hooks.AgentRunStartedEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.AgentRunStarted, err)
			}
			s.ChildRuns = append(s.ChildRuns, &run.ChildRunLink{
				ToolName:     p.ToolName,
				ToolCallID:   p.ToolCallID,
				ChildRunID:   p.ChildRunID,
				ChildAgentID: p.ChildAgentID,
			})

		case hooks.AwaitClarification:
			var p hooks.AwaitClarificationEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitClarification, err)
			}
			s.Await = &run.AwaitSnapshot{
				Kind:     string(hooks.AwaitClarification),
				ID:       p.ID,
				ToolName: p.RestrictToTool,
				Question: p.Question,
			}

		case hooks.AwaitConfirmation:
			var p hooks.AwaitConfirmationEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitConfirmation, err)
			}
			s.Await = &run.AwaitSnapshot{
				Kind:       string(hooks.AwaitConfirmation),
				ID:         p.ID,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Title:      p.Title,
				Prompt:     p.Prompt,
			}

		case hooks.AwaitExternalTools:
			var p hooks.AwaitExternalToolsEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.AwaitExternalTools, err)
			}
			s.Await = &run.AwaitSnapshot{
				Kind:      string(hooks.AwaitExternalTools),
				ID:        p.ID,
				ItemCount: len(p.Items),
			}

		case hooks.RunPhaseChanged:
			var p hooks.RunPhaseChangedEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.RunPhaseChanged, err)
			}
			s.Phase = p.Phase

		case hooks.RunResumed:
			s.Await = nil

		case hooks.AssistantMessage:
			var p hooks.AssistantMessageEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.AssistantMessage, err)
			}
			s.LastAssistantMessage = p.Message

		case hooks.ToolCallScheduled:
			var p hooks.ToolCallScheduledEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolCallScheduled, err)
			}
			tc, ok := toolCalls[p.ToolCallID]
			if !ok {
				tc = &run.ToolCallSnapshot{
					ToolCallID: p.ToolCallID,
				}
				toolCalls[p.ToolCallID] = tc
			}
			tc.ToolName = p.ToolName
			tc.ParentToolCallID = p.ParentToolCallID
			if tc.ScheduledAt.IsZero() {
				tc.ScheduledAt = e.Timestamp
			}
			tc.ExpectedChildrenTotal = p.ExpectedChildrenTotal

			if p.ParentToolCallID != "" {
				parent, ok := toolCalls[p.ParentToolCallID]
				if !ok {
					parent = &run.ToolCallSnapshot{
						ToolCallID: p.ParentToolCallID,
					}
					toolCalls[p.ParentToolCallID] = parent
				}
				parent.ObservedChildrenTotal++
			}

		case hooks.ToolCallUpdated:
			var p hooks.ToolCallUpdatedEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolCallUpdated, err)
			}
			tc, ok := toolCalls[p.ToolCallID]
			if !ok {
				tc = &run.ToolCallSnapshot{
					ToolCallID: p.ToolCallID,
				}
				toolCalls[p.ToolCallID] = tc
			}
			tc.ExpectedChildrenTotal = p.ExpectedChildrenTotal

		case hooks.ToolResultReceived:
			var p hooks.ToolResultReceivedEvent
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.ToolResultReceived, err)
			}
			tc, ok := toolCalls[p.ToolCallID]
			if !ok {
				tc = &run.ToolCallSnapshot{
					ToolCallID: p.ToolCallID,
				}
				toolCalls[p.ToolCallID] = tc
			}
			tc.ToolName = p.ToolName
			tc.ParentToolCallID = p.ParentToolCallID
			tc.CompletedAt = e.Timestamp
			tc.Duration = p.Duration
			if p.Error != nil {
				tc.ErrorSummary = p.Error.Message
			}
		case hooks.RunCompleted:
			var p struct {
				Status string    `json:"status"`
				Phase  run.Phase `json:"phase"`
				Error  string    `json:"error,omitempty"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decode %s payload: %w", hooks.RunCompleted, err)
			}
			s.Phase = p.Phase
			s.Await = nil
			switch p.Status {
			case "success":
				s.Status = run.StatusCompleted
			case "failed":
				s.Status = run.StatusFailed
			case "canceled":
				s.Status = run.StatusCanceled
			default:
				return nil, fmt.Errorf("unsupported run completion status %q", p.Status)
			}
		default:
			// Most event types do not affect the snapshot; they remain available via ListRunEvents.
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

	return s, nil
}
