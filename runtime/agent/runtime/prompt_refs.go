package runtime

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
)

// ResolvePromptRefs returns prompt refs for the run and all linked child runs.
//
// This is a convenience wrapper around session.ResolvePromptRefs that uses the
// runtime's configured SessionStore.
func (r *Runtime) ResolvePromptRefs(ctx context.Context, runID string) ([]prompt.PromptRef, error) {
	if r.SessionStore == nil {
		return nil, errors.New("session store is not configured")
	}
	return session.ResolvePromptRefs(ctx, r.SessionStore, runID)
}
