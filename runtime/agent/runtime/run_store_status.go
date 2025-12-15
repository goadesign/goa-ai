package runtime

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/agent/run"
)

// recordRunStatus writes a best-effort initial run record to the configured
// RunStore.
//
// This is intentionally outside the workflow path: it allows callers to
// discover that a run was submitted even if the workflow fails to start and
// no hooks are emitted. Runtime hook subscribers keep records up to date once
// the workflow begins execution.
func (r *Runtime) recordRunStatus(ctx context.Context, input *RunInput, status run.Status, meta map[string]any) {
	now := time.Now()
	rec := run.Record{
		AgentID:   input.AgentID,
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Status:    status,
		StartedAt: now,
		UpdatedAt: now,
		Labels:    cloneLabels(input.Labels),
		Metadata:  meta,
	}
	if err := r.RunStore.Upsert(ctx, rec); err != nil {
		r.logWarn(ctx, "run record upsert failed", err)
	}
}
