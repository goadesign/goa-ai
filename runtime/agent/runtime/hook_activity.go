package runtime

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
)

// hookActivityName is the engine-registered activity that publishes hook events
// on behalf of workflow code.
const hookActivityName = "runtime.publish_hook"

// hookActivity publishes workflow-emitted hook events outside of deterministic
// workflow execution.
//
// Contract:
//   - The canonical record of runtime events is the run event log. Appending to
//     RunEventStore is a correctness invariant: failures must fail the activity
//     so the workflow run can stop and/or be retried by the engine.
//   - Streaming is a session contract:
//   - While the session is active, stream emission failures must fail the
//     activity so workflows can retry or stop rather than silently diverge
//     from the stream consumer's view.
//   - After the session is ended, stream emission becomes a no-op to avoid
//     "stream destroyed mid-run" turning into spurious run failures.
//   - Publishing to the hook bus is best-effort. The bus drives derived storage
//     (memory) and local observability, but it must not be allowed to corrupt or
//     block the canonical transcript.
func (r *Runtime) hookActivity(ctx context.Context, input *HookActivityInput) error {
	evt, err := hooks.DecodeFromHookInput(input)
	if err != nil {
		return err
	}
	// Tool call argument deltas are best-effort UX signals. They are intentionally
	// excluded from the canonical run event log to avoid bloating durable history.
	//
	// Consumers must treat ToolCallArgsDelta as optional; the canonical tool
	// payload is still emitted via tool_start/tool_end and the finalized tool call.
	if input.Type != hooks.ToolCallArgsDelta {
		if err := r.RunEventStore.Append(ctx, &runlog.Event{
			RunID:     input.RunID,
			AgentID:   input.AgentID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Type:      input.Type,
			Payload:   append([]byte(nil), input.Payload...),
			Timestamp: time.UnixMilli(evt.Timestamp()).UTC(),
		}); err != nil {
			return err
		}

		if err := r.updateRunMetaFromHookEvent(ctx, evt); err != nil {
			return err
		}
	}

	if r.streamSubscriber != nil {
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
	return nil
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
	case *hooks.ChildRunLinkedEvent:
		now := time.Now().UTC()
		return r.SessionStore.UpsertRun(ctx, session.RunMeta{
			AgentID:   string(e.ChildAgentID),
			RunID:     e.ChildRunID,
			SessionID: e.SessionID(),
			Status:    session.RunStatusPending,
			StartedAt: now,
			UpdatedAt: now,
			Labels:    nil,
			Metadata:  nil,
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
