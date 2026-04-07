package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/runlog"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// recordActivityName is the engine-registered activity that persists durable
// runtime records and fans out hook-backed records on behalf of workflow code.
const recordActivityName = "runtime.record_event"

// recordActivity persists workflow-emitted runtime records outside of
// deterministic workflow execution.
//
// Contract:
//   - The canonical record of runtime events is the run event log. Appending to
//     RunEventStore is a correctness invariant: failures must fail the activity
//     so the workflow run can stop and/or be retried by the engine.
//   - Canonical transcript message records are runtime-owned run-log records.
//     They bypass hook decoding and bus publication. Seed records rebuild run
//     snapshots only; appended records additionally fan out canonical
//     assistant-turn stream events for session-aware consumers.
//   - Streaming is a session contract:
//   - While the session is active, stream emission failures must fail the
//     activity so workflows can retry or stop rather than silently diverge
//     from the stream consumer's view.
//   - After the session is ended, stream emission becomes a no-op to avoid
//     "stream destroyed mid-run" turning into spurious run failures.
//   - One-shot runs (empty SessionID) bypass SessionStore and stream sinks.
//   - Publishing to the hook bus is best-effort. The bus drives derived storage
//     (memory) and local observability, but it must not be allowed to corrupt or
//     block the canonical transcript.
func (r *Runtime) recordActivity(ctx context.Context, input *RecordActivityInput) error {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	if input == nil {
		return errors.New("runtime: record input is nil")
	}

	if input.Type == transcript.RunLogMessagesSeeded || input.Type == transcript.RunLogMessagesAppended {
		return r.appendTranscriptRunLogMessages(ctx, input)
	}

	evt, err := hooks.DecodeFromRecordInput(input)
	if err != nil {
		return err
	}
	payload := append([]byte(nil), input.Payload...)
	if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
		if enriched := r.enrichToolCallScheduledHint(ctx, e); enriched {
			reencoded, err := hooks.EncodeToRecordInput(e, hooks.EncodeOptions{
				TurnID:      input.TurnID,
				EventKey:    input.EventKey,
				TimestampMS: input.TimestampMS,
			})
			if err == nil {
				payload = append([]byte(nil), reencoded.Payload.RawMessage()...)
			}
		}
	}
	// Tool call argument deltas are best-effort UX signals. They are intentionally
	// excluded from the canonical run event log to avoid bloating durable history.
	//
	// Consumers must treat ToolCallArgsDelta as optional; the canonical tool
	// payload is still emitted via tool_start/tool_end and the finalized tool call.
	if input.Type != hooks.ToolCallArgsDelta {
		if _, err := r.RunEventStore.Append(ctx, &runlog.Event{
			EventKey:  input.EventKey,
			RunID:     input.RunID,
			AgentID:   input.AgentID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Type:      input.Type,
			Payload:   payload,
			Timestamp: time.UnixMilli(evt.Timestamp()).UTC(),
		}); err != nil {
			return err
		}

		// Session-derived metadata exists only for sessionful runs. One-shot runs
		// intentionally bypass SessionStore and keep canonical state in RunEventStore.
		if input.SessionID != "" {
			if err := r.updateRunMetaFromHookEvent(ctx, evt); err != nil {
				return err
			}
		}
	}

	// Streaming is explicitly session-scoped. One-shot runs (empty SessionID) are
	// runlog-only and must never publish to stream sinks.
	if input.SessionID != "" && r.streamSubscriber != nil {
		sess, err := r.SessionStore.LoadSession(ctx, input.SessionID)
		if err != nil {
			return err
		}
		if sess.Status != session.StatusEnded {
			if err := r.streamSubscriber.HandleEvent(ctx, evt); err != nil {
				return err
			}
		}
	}

	// Tool call argument deltas are streaming-only; they do not participate in
	// derived stores like memory.
	if input.Type != hooks.ToolCallArgsDelta {
		if err := r.Bus.Publish(ctx, evt); err != nil {
			r.logWarn(ctx, "hook publish failed", err, "event", evt.Type())
		}
	}
	if input.Type == hooks.RunCompleted {
		r.storeWorkflowHandle(input.RunID, nil)
	}
	return nil
}

// appendTranscriptRunLogMessages appends canonical transcript message records to
// the durable run log. Only appended transcript messages fan out canonical
// assistant-turn stream events; seeded transcript messages rebuild snapshots but
// do not represent newly committed conversation output.
func (r *Runtime) appendTranscriptRunLogMessages(ctx context.Context, input *RecordActivityInput) error {
	if input == nil {
		return errors.New("runtime: transcript delta input is nil")
	}
	messages, err := transcript.DecodeRunLogDelta(input.Payload)
	if err != nil {
		return fmt.Errorf("runtime: decode transcript delta: %w", err)
	}
	_, err = r.RunEventStore.Append(ctx, &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   append([]byte(nil), input.Payload...),
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	})
	if err != nil {
		return err
	}
	if input.SessionID == "" || r.streamSubscriber == nil {
		return nil
	}
	sess, err := r.SessionStore.LoadSession(ctx, input.SessionID)
	if err != nil {
		return err
	}
	if sess.Status == session.StatusEnded {
		return nil
	}
	streamCommittedAssistantTurns := input.Type == transcript.RunLogMessagesAppended
	if !streamCommittedAssistantTurns {
		return nil
	}
	for i, msg := range messages {
		if msg == nil || msg.Role != model.ConversationRoleAssistant || agentMessageText(msg) == "" {
			continue
		}
		evt := hooks.NewAssistantTurnCommittedEvent(input.RunID, input.AgentID, input.SessionID, msg)
		evt.SetTurnID(input.TurnID)
		evt.SetTimestampMS(input.TimestampMS)
		evt.SetEventKey(committedAssistantTurnEventKey(input.EventKey, i))
		if err := r.streamSubscriber.HandleEvent(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

// committedAssistantTurnEventKey derives a stable event key for one assistant
// message extracted from a transcript delta record.
func committedAssistantTurnEventKey(base string, index int) string {
	return fmt.Sprintf("%s/assistant/%d", base, index)
}

func (r *Runtime) enrichToolCallScheduledHint(ctx context.Context, evt *hooks.ToolCallScheduledEvent) bool {
	if evt == nil {
		return false
	}
	if evt.DisplayHint != "" {
		return false
	}
	raw := normalizeHintPayloadJSON(evt.Payload.RawMessage())
	typed, err := r.unmarshalToolValue(ctx, evt.ToolName, raw, true)
	if err != nil || typed == nil {
		return false
	}
	if hint := rthints.FormatCallHint(evt.ToolName, typed); hint != "" {
		evt.DisplayHint = hint
		return true
	}
	return false
}

func normalizeHintPayloadJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}")
	}
	return raw
}

func (r *Runtime) updateRunMetaFromHookEvent(ctx context.Context, evt hooks.Event) error {
	if evt == nil {
		return errors.New("runtime: hook event is nil")
	}
	switch e := evt.(type) {
	case *hooks.RunStartedEvent:
		run, err := r.SessionStore.LoadRun(ctx, e.RunID())
		if err != nil {
			if errors.Is(err, session.ErrRunNotFound) {
				startedAt := time.UnixMilli(e.Timestamp()).UTC()
				now := time.Now().UTC()
				return r.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   e.AgentID(),
					RunID:     e.RunID(),
					SessionID: e.SessionID(),
					Status:    session.RunStatusRunning,
					StartedAt: startedAt,
					UpdatedAt: now,
					Labels:    cloneLabels(e.RunContext.Labels),
					Metadata:  nil,
				})
			}
			return err
		}
		run.Status = session.RunStatusRunning
		run.UpdatedAt = time.Now().UTC()
		run.Labels = cloneLabels(e.RunContext.Labels)
		return r.SessionStore.UpsertRun(ctx, run)
	case *hooks.PromptRenderedEvent:
		run, err := r.SessionStore.LoadRun(ctx, e.RunID())
		if err != nil {
			if errors.Is(err, session.ErrRunNotFound) {
				now := time.Now().UTC()
				return r.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   e.AgentID(),
					RunID:     e.RunID(),
					SessionID: e.SessionID(),
					Status:    session.RunStatusRunning,
					StartedAt: time.UnixMilli(e.Timestamp()).UTC(),
					UpdatedAt: now,
					Labels:    nil,
					Metadata:  nil,
					PromptRefs: []prompt.PromptRef{
						{
							ID:      e.PromptID,
							Version: e.Version,
						},
					},
				})
			}
			return err
		}
		run.UpdatedAt = time.Now().UTC()
		run.PromptRefs = appendUniquePromptRef(run.PromptRefs, prompt.PromptRef{
			ID:      e.PromptID,
			Version: e.Version,
		})
		return r.SessionStore.UpsertRun(ctx, run)
	case *hooks.ChildRunLinkedEvent:
		return r.SessionStore.LinkChildRun(ctx, e.RunID(), session.RunMeta{
			AgentID:   string(e.ChildAgentID),
			RunID:     e.ChildRunID,
			SessionID: e.SessionID(),
			Status:    session.RunStatusPending,
		})
	case *hooks.RunPausedEvent:
		return r.updateRunStatus(ctx, e.RunID(), session.RunStatusPaused)
	case *hooks.RunResumedEvent:
		return r.updateRunStatus(ctx, e.RunID(), session.RunStatusRunning)
	case *hooks.RunCompletedEvent:
		switch e.Status {
		case "success":
			return r.updateRunStatus(ctx, e.RunID(), session.RunStatusCompleted)
		case "failed":
			return r.updateRunStatus(ctx, e.RunID(), session.RunStatusFailed)
		case "canceled":
			return r.updateRunStatus(ctx, e.RunID(), session.RunStatusCanceled)
		default:
			return errors.New("runtime: run completed event has unknown status")
		}
	default:
		return nil
	}
}

func (r *Runtime) updateRunStatus(ctx context.Context, runID string, status session.RunStatus) error {
	run, err := r.SessionStore.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	run.Status = status
	run.UpdatedAt = time.Now().UTC()
	return r.SessionStore.UpsertRun(ctx, run)
}

// appendUniquePromptRef appends ref only when it is not already present.
// Uniqueness is defined by (prompt_id, version) and ordering is first-seen.
func appendUniquePromptRef(existing []prompt.PromptRef, ref prompt.PromptRef) []prompt.PromptRef {
	for _, cur := range existing {
		if cur.ID == ref.ID && cur.Version == ref.Version {
			return existing
		}
	}
	return append(existing, ref)
}
