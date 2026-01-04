package engine

import "context"

// wfCtxKey is the private context key used to stash a WorkflowContext inside a
// Go context passed to activities, enabling code to retrieve the originating
// workflow context when needed (e.g., nested agent execution).
type wfCtxKey struct{}

// activityCtxKey marks contexts that originate from an activity invocation.
// The temporal engine uses this to distinguish true workflow contexts from
// activity contexts that carry the originating WorkflowContext for reference.
type activityCtxKey struct{}

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
