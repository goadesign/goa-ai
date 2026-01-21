package runtime

// session_lifecycle.go defines the public session lifecycle surface of the runtime.
//
// Sessions are first-class: callers must create a session explicitly before
// starting runs under them. This gives the runtime a strong contract boundary
// for session-scoped state and, in later lanes, session-scoped streaming.

import (
	"context"
	"errors"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/session"
)

// CreateSession creates (or returns) an active session with the given ID.
//
// Contract:
// - sessionID must be non-empty and non-whitespace.
// - Creating an already-active session is idempotent.
// - Creating an ended session returns session.ErrSessionEnded.
func (r *Runtime) CreateSession(ctx context.Context, sessionID string) (session.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return session.Session{}, ErrMissingSessionID
	}
	return r.SessionStore.CreateSession(ctx, id, time.Now().UTC())
}

// DeleteSession ends a session with the given ID.
//
// Contract:
// - Ending a session is durable and monotonic.
// - Cancellation of in-flight runs is best-effort and bounded.
func (r *Runtime) DeleteSession(ctx context.Context, sessionID string) (session.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return session.Session{}, ErrMissingSessionID
	}
	ended, err := r.SessionStore.EndSession(ctx, id, time.Now().UTC())
	if err != nil {
		return session.Session{}, err
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.cancelSessionRuns(cancelCtx, id); err != nil {
		r.logWarn(ctx, "cancel session runs failed", err, "session_id", id)
	}
	return ended, nil
}

func (r *Runtime) cancelSessionRuns(ctx context.Context, sessionID string) error {
	runs, err := r.SessionStore.ListRunsBySession(ctx, sessionID, []session.RunStatus{
		session.RunStatusPending,
		session.RunStatusRunning,
		session.RunStatusPaused,
	})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	canceler, ok := r.Engine.(engine.Canceler)
	var errs []error
	for _, run := range runs {
		if ok {
			if err := canceler.CancelByID(ctx, run.RunID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := r.CancelRun(ctx, run.RunID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
