package runtime

import (
	"context"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
)

// handleRunStoreEvent updates the configured RunStore in response to hook bus
// events.
//
// The Runtime registers this subscriber only when RunStore is configured. The
// handler is intentionally idempotent: it uses load+upsert so duplicate event
// delivery (for example due to activity retries) converges to the same record.
func (r *Runtime) handleRunStoreEvent(ctx context.Context, evt hooks.Event) error {
	switch e := evt.(type) {
	case *hooks.RunStartedEvent:
		return r.upsertRunRecord(ctx, evt, run.StatusRunning, cloneLabels(e.RunContext.Labels), nil, true)
	case *hooks.RunPausedEvent:
		meta := cloneMetadata(e.Metadata)
		meta = mergeMetadata(meta, map[string]any{
			"pause_reason":       e.Reason,
			"pause_requested_by": e.RequestedBy,
		})
		return r.upsertRunRecord(ctx, evt, run.StatusPaused, cloneLabels(e.Labels), meta, false)
	case *hooks.RunResumedEvent:
		meta := map[string]any{
			"resume_requested_by": e.RequestedBy,
			"resume_notes":        e.Notes,
		}
		return r.upsertRunRecord(ctx, evt, run.StatusRunning, cloneLabels(e.Labels), meta, false)
	case *hooks.RunCompletedEvent:
		status, err := runStatusFromCompletion(e.Status)
		if err != nil {
			return err
		}
		var meta map[string]any
		if e.Error != nil {
			meta = map[string]any{
				"error": e.Error.Error(),
			}
		}
		return r.upsertRunRecord(ctx, evt, status, nil, meta, false)
	case *hooks.PolicyDecisionEvent:
		return r.applyPolicyDecisionToRunRecord(ctx, e)
	default:
		return nil
	}
}

// upsertRunRecord loads the current record (if any), applies updates derived from
// the given event, and upserts it back into the store.
func (r *Runtime) upsertRunRecord(ctx context.Context, evt hooks.Event, status run.Status, labels map[string]string, meta map[string]any, overwriteStart bool) error {
	rec, err := r.RunStore.Load(ctx, evt.RunID())
	if err != nil {
		return err
	}

	eventTime := time.UnixMilli(evt.Timestamp())
	if rec.RunID == "" {
		rec.RunID = evt.RunID()
		rec.StartedAt = eventTime
	}
	rec.AgentID = agent.Ident(evt.AgentID())
	rec.SessionID = evt.SessionID()
	rec.TurnID = evt.TurnID()
	rec.Status = status
	rec.UpdatedAt = eventTime
	if overwriteStart {
		rec.StartedAt = eventTime
	} else if rec.StartedAt.IsZero() {
		rec.StartedAt = eventTime
	}

	rec.Labels = mergeLabels(cloneLabels(rec.Labels), labels)
	rec.Metadata = mergeMetadata(cloneMetadata(rec.Metadata), meta)
	return r.RunStore.Upsert(ctx, rec)
}

// applyPolicyDecisionToRunRecord records the policy decision (caps, allowed
// tools, metadata) into the run record for observability.
func (r *Runtime) applyPolicyDecisionToRunRecord(ctx context.Context, evt *hooks.PolicyDecisionEvent) error {
	rec, err := r.RunStore.Load(ctx, evt.RunID())
	if err != nil {
		return err
	}
	eventTime := time.UnixMilli(evt.Timestamp())
	if rec.RunID == "" {
		rec.RunID = evt.RunID()
		rec.StartedAt = eventTime
	}
	rec.AgentID = agent.Ident(evt.AgentID())
	rec.SessionID = evt.SessionID()
	rec.TurnID = evt.TurnID()
	rec.Status = run.StatusRunning
	rec.UpdatedAt = eventTime
	if rec.StartedAt.IsZero() {
		rec.StartedAt = eventTime
	}

	rec.Labels = mergeLabels(cloneLabels(rec.Labels), cloneLabels(evt.Labels))

	entry := map[string]any{
		"caps":      evt.Caps,
		"timestamp": eventTime.UTC(),
	}
	if len(evt.AllowedTools) > 0 {
		entry["allowed_tools"] = evt.AllowedTools
	}
	if len(evt.Metadata) > 0 {
		entry["metadata"] = cloneMetadata(evt.Metadata)
	}
	meta := appendPolicyDecisionMetadata(cloneMetadata(rec.Metadata), entry)
	rec.Metadata = meta

	return r.RunStore.Upsert(ctx, rec)
}

// runStatusFromCompletion maps a completion payload string to a durable run.Status.
func runStatusFromCompletion(status string) (run.Status, error) {
	switch status {
	case "success":
		return run.StatusCompleted, nil
	case "failed":
		return run.StatusFailed, nil
	case "canceled":
		return run.StatusCanceled, nil
	default:
		return "", fmt.Errorf("unsupported run completion status %q", status)
	}
}

// mergeMetadata merges src into dst and returns dst.
func mergeMetadata(dst map[string]any, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
