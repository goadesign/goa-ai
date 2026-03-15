package engine

import (
	"context"
	"time"
)

// wfCtxKey is the private context key used to stash a WorkflowContext inside a
// Go context passed to activities, enabling code to retrieve the originating
// workflow context when needed (e.g., nested agent execution).
type wfCtxKey struct{}

// activityCtxKey marks contexts that originate from an activity invocation.
// The temporal engine uses this to distinguish true workflow contexts from
// activity contexts that carry the originating WorkflowContext for reference.
type activityCtxKey struct{}

// activityHeartbeatKey stores an engine-specific heartbeat recorder for activity
// contexts that support cooperative cancellation delivery.
type activityHeartbeatKey struct{}

// activityHeartbeatTimeoutKey stores the effective heartbeat timeout attached to
// an activity context by the engine adapter.
type activityHeartbeatTimeoutKey struct{}

// ActivityHeartbeatRecorder records liveness heartbeats for a running activity.
// Engine adapters attach implementations to activity contexts when the backend
// requires heartbeats to deliver cancellation or timeout updates promptly.
type ActivityHeartbeatRecorder interface {
	RecordHeartbeat(details ...any)
}

// WithWorkflowContext returns a child context that carries the provided
// WorkflowContext. Engine adapters should use this when invoking activity
// handlers so downstream code can retrieve the workflow context if needed.
func WithWorkflowContext(ctx context.Context, wf WorkflowContext) context.Context {
	return context.WithValue(ctx, wfCtxKey{}, wf)
}

// WithActivityContext returns a child context that is marked as an activity
// invocation context.
func WithActivityContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, activityCtxKey{}, true)
}

// WithActivityHeartbeatRecorder returns a child context carrying the given
// heartbeat recorder.
func WithActivityHeartbeatRecorder(ctx context.Context, recorder ActivityHeartbeatRecorder) context.Context {
	return context.WithValue(ctx, activityHeartbeatKey{}, recorder)
}

// WithActivityHeartbeatTimeout returns a child context carrying the effective
// heartbeat timeout for the current activity attempt. Zero means the engine is
// not using heartbeat-based liveness detection for this activity.
func WithActivityHeartbeatTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, activityHeartbeatTimeoutKey{}, timeout)
}

// IsActivityContext reports whether ctx is marked as originating from an
// activity invocation.
func IsActivityContext(ctx context.Context) bool {
	v := ctx.Value(activityCtxKey{})
	b, ok := v.(bool)
	return ok && b
}

// WorkflowContextFromContext extracts a WorkflowContext from ctx if present.
// Returns nil if the context does not carry a workflow context. Engine adapters
// are responsible for attaching the workflow context via WithWorkflowContext.
func WorkflowContextFromContext(ctx context.Context) WorkflowContext {
	if v := ctx.Value(wfCtxKey{}); v != nil {
		if wf, ok := v.(WorkflowContext); ok {
			return wf
		}
	}
	return nil
}

// RecordActivityHeartbeat emits a heartbeat on ctx when an engine-specific
// recorder is attached. It returns false when ctx is not an activity context or
// when the engine does not require heartbeats.
func RecordActivityHeartbeat(ctx context.Context, details ...any) bool {
	recorder, ok := ctx.Value(activityHeartbeatKey{}).(ActivityHeartbeatRecorder)
	if !ok || recorder == nil {
		return false
	}
	recorder.RecordHeartbeat(details...)
	return true
}

// ActivityHeartbeatTimeout returns the effective heartbeat timeout carried on
// ctx. Zero means the current activity does not use heartbeat-based failure
// detection.
func ActivityHeartbeatTimeout(ctx context.Context) time.Duration {
	timeout, ok := ctx.Value(activityHeartbeatTimeoutKey{}).(time.Duration)
	if !ok {
		return 0
	}
	return timeout
}
