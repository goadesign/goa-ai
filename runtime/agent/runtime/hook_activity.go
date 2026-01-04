package runtime

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
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
//   - Publishing to the hook bus is best-effort. The bus drives live UX and
//     observability, but it must not be allowed to corrupt or block the
//     canonical transcript. Failures are logged and the activity succeeds.
func (r *Runtime) hookActivity(ctx context.Context, input *HookActivityInput) error {
	evt, err := hooks.DecodeFromHookInput(input)
	if err != nil {
		return err
	}
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
	if err := r.Bus.Publish(ctx, evt); err != nil {
		r.logWarn(ctx, "hook publish failed", err, "event", evt.Type())
	}
	return nil
}
