// Package runtime rebuilds the compact run state returned by Runtime from
// canonical run-log events supplied in oldest-first order.
package runtime

import (
	"fmt"
	"reflect"
	"sort"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// newRunSnapshot derives a compact run state snapshot by replaying canonical
// run log events in order. The caller supplies oldest-first events and the
// exact parent call verified from the parent run, or an empty ID for a root.
// Inherited events come only from the exact suspended predecessors; they
// initialize unfinished tool calls without replacing this run's identity.
func newRunSnapshot(events []*runlog.Event, parentToolCallID string, inherited []*runlog.Event) (*run.Snapshot, error) {
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
		Subject:   events[0].Subject,
		SessionID: events[0].SessionID,
		TurnID:    events[0].TurnID,
		Status:    run.StatusRunning,
		Phase:     run.PhasePrompted,
		StartedAt: events[0].Timestamp,
		UpdatedAt: events[0].Timestamp,
	}
	if _, err := genrunidentity.EncodeRunSubject(&s.Subject); err != nil {
		return nil, fmt.Errorf("snapshot run subject: %w", err)
	}
	if service, ok := s.Subject.Executor.AsServiceTool(); ok {
		owner := *service
		s.Subject.Executor = genrunidentity.NewRunExecutorServiceTool(&owner)
	}
	state := snapshotToolState{
		parentToolCallID: parentToolCallID,
		calls:            make(map[string]*run.ToolCallSnapshot),
		active:           make(map[string]*run.ToolCallSnapshot),
		schedules:        make(map[string]*hooks.ToolCallScheduledEvent),
		completed:        make(map[string]struct{}),
	}
	for _, event := range inherited {
		if event == nil || !reflect.DeepEqual(event.Subject, s.Subject) || event.SessionID != s.SessionID {
			return nil, fmt.Errorf("snapshot inherited event owner does not match run %q", s.RunID)
		}
		switch event.Type {
		case hooks.ToolCallScheduled, hooks.ToolCallUpdated, hooks.ToolResultReceived:
			decoded, err := hooks.DecodeRunlogEvent(event)
			if err != nil {
				return nil, err
			}
			if err := state.apply(event, decoded); err != nil {
				return nil, err
			}
		}
	}
	// Earlier completions remain invalid update targets, but only unfinished
	// calls belong in the continued run's visible tool state.
	for id := range state.completed {
		delete(state.calls, id)
	}

	for _, e := range events {
		if e.RunID != s.RunID {
			return nil, fmt.Errorf("snapshot events contain multiple run IDs (%q, %q)", s.RunID, e.RunID)
		}
		if !reflect.DeepEqual(e.Subject, s.Subject) {
			return nil, fmt.Errorf("snapshot event subject does not match run %q", s.RunID)
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
			hooks.AwaitMCPInput,
			hooks.AwaitConfirmation,
			hooks.AwaitExternalTools,
			hooks.RunPhaseChanged,
			hooks.AssistantMessage,
			hooks.ToolCallScheduled,
			hooks.ToolCallUpdated,
			hooks.ToolResultReceived,
			hooks.RunCompleted,
			hooks.RunSuspended:
		case hooks.PromptRendered, hooks.PolicyDecision, hooks.Usage,
			hooks.ModelOutputRejected, hooks.PlannerOutputRejected, hooks.PlannerNote,
			hooks.ThinkingBlock, hooks.AssistantTurnCommitted, hooks.MemoryAppended:
			if _, ok := s.Subject.Executor.AsAgent(); !ok {
				return nil, fmt.Errorf("snapshot event %q requires an Agent subject", e.Type)
			}
			continue
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
			s.Labels = p.Labels

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

		case *hooks.AwaitMCPInputEvent:
			s.Await = &run.AwaitSnapshot{Kind: string(hooks.AwaitMCPInput), ToolName: p.Input.ToolName, ToolCallID: p.Input.ToolCallID, ItemCount: len(p.Input.Requests)}

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

		case *hooks.ToolCallScheduledEvent, *hooks.ToolCallUpdatedEvent, *hooks.ToolResultReceivedEvent:
			if err := state.apply(e, decoded); err != nil {
				return nil, err
			}

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

	if len(state.calls) > 0 {
		s.ToolCalls = make([]*run.ToolCallSnapshot, 0, len(state.calls))
		for _, v := range state.calls {
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
		setSnapshotTranscript(s, transcriptMessages)
	}

	return s, nil
}

// setSnapshotTranscript keeps the full saved conversation and its last
// assistant text together, including messages reached through initial history.
func setSnapshotTranscript(snapshot *run.Snapshot, messages []*model.Message) {
	snapshot.Transcript = messages
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == model.ConversationRoleAssistant && message.Text() != "" {
			snapshot.LastAssistantMessage = message.Text()
			break
		}
	}
}
