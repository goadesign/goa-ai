package runtime

// This file sends live model text and thinking directly from the planner
// activity to the configured session stream. These updates never enter the
// durable run log or hook bus. Emitted assistant text is append-only; the
// workflow later persists complete accepted transcript messages and tool calls.

import (
	"context"

	"goa.design/goa-ai/runtime/agent/stream"
)

// publishModelOutput sends one live model update through the configured stream
// profile. One-shot runs have no session ID.
func (r *Runtime) publishModelOutput(
	ctx context.Context,
	sessionID string,
	event stream.Event,
) error {
	if sessionID == "" || r.streamSubscriber == nil {
		return nil
	}
	return r.streamSubscriber.HandleModelOutputEvent(ctx, event)
}
