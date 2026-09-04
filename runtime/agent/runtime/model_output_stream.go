package runtime

// This file sends live model text and thinking directly from the planner
// activity to the configured session stream. These updates never enter the
// durable run log or hook bus. Emitted assistant text is append-only. The
// workflow later persists either the complete accepted transcript or, when the
// response is rejected or fails, the exact text that already reached clients.

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
) (bool, error) {
	if sessionID == "" || r.streamSubscriber == nil {
		return false, nil
	}
	return r.streamSubscriber.HandleModelOutputEvent(ctx, event)
}
