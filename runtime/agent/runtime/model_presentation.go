package runtime

// This file sends provisional model text and thinking directly from the
// planner activity to the configured session stream. These updates never enter
// the durable run log or hook bus. The workflow later persists only validated
// transcript messages and complete tool calls.

import (
	"context"

	"goa.design/goa-ai/runtime/agent/stream"
)

// publishModelPresentation sends one provisional model update through the
// configured stream profile. One-shot runs have no session ID, and profiles
// that hide both assistant text and thinking also hide their lifecycle.
func (r *Runtime) publishModelPresentation(
	ctx context.Context,
	sessionID string,
	event stream.Event,
) error {
	if sessionID == "" || r.streamSubscriber == nil {
		return nil
	}
	return r.streamSubscriber.HandleProvisionalEvent(ctx, event)
}
