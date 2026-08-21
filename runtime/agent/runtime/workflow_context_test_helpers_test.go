// workflow_context_test_helpers_test.go routes workflow activity calls to
// focused test handlers without starting an engine worker.
package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

// routeWorkflowContext routes activity execution through registered handlers so
// tests can call runtime helpers without standing up a workflow engine.
type routeWorkflowContext struct {
	ctx   context.Context
	runID string
	now   func() time.Time

	plannerRoutes map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error)
	limitRoutes   map[string]func(context.Context, *LimitFinalizationActivityInput) (*LimitFinalizationActivityOutput, error)
	toolRoutes    map[string]func(context.Context, *ToolInput) (*ToolOutput, error)

	lastHookCall    engine.RecordActivityCall
	lastPlannerCall engine.PlannerActivityCall
	lastLimitCall   engine.LimitFinalizationActivityCall
	lastToolCall    engine.ToolActivityCall
	sequenceMu      sync.Mutex
	nextSequence    uint64

	hookRuntime  *Runtime
	childRuntime *Runtime

	parent *routeWorkflowContext
}

func (r *routeWorkflowContext) root() *routeWorkflowContext {
	if r.parent != nil {
		return r.parent
	}
	return r
}

func (r *routeWorkflowContext) Context() context.Context {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	return engine.WithWorkflowContext(r.ctx, r)
}

func (r *routeWorkflowContext) WorkflowID() string {
	return "wf"
}

func (r *routeWorkflowContext) RunID() string {
	return r.runID
}

func (r *routeWorkflowContext) Detached() engine.WorkflowContext {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	return r.withContext(context.WithoutCancel(r.ctx))
}

func (r *routeWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	ctx, cancel := context.WithCancel(r.ctx)
	return r.withContext(ctx), cancel
}

// withContext preserves the activity routes and points derived workflow
// contexts at the root sequence counter used by assertions.
func (r *routeWorkflowContext) withContext(ctx context.Context) *routeWorkflowContext {
	return &routeWorkflowContext{
		ctx:              ctx,
		runID:            r.runID,
		now:              r.now,
		plannerRoutes:    r.plannerRoutes,
		limitRoutes:      r.limitRoutes,
		toolRoutes:       r.toolRoutes,
		lastHookCall:     r.lastHookCall,
		lastPlannerCall:  r.lastPlannerCall,
		lastLimitCall:    r.lastLimitCall,
		lastToolCall:     r.lastToolCall,
		hookRuntime:      r.hookRuntime,
		childRuntime:     r.childRuntime,
		parent:           r.root(),
	}
}

func (r *routeWorkflowContext) Now() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Unix(0, 0)
}

func (r *routeWorkflowContext) NextSequence() uint64 {
	root := r.root()
	root.sequenceMu.Lock()
	defer root.sequenceMu.Unlock()
	root.nextSequence++
	return root.nextSequence
}

func (r *routeWorkflowContext) NewTimer(ctx context.Context, duration time.Duration) (engine.Future[time.Time], error) {
	now := time.Now()
	if duration <= 0 {
		future := &controlledTimeFuture{ready: make(chan struct{}), v: now}
		close(future.ready)
		return future, nil
	}
	future := &controlledTimeFuture{ready: make(chan struct{}), v: now.Add(duration)}
	go func() {
		defer close(future.ready)
		select {
		case <-ctx.Done():
			future.err = ctx.Err()
		case <-time.After(duration):
		}
	}()
	return future, nil
}

func (r *routeWorkflowContext) Await(condition func() bool) error {
	if condition == nil {
		return fmt.Errorf("await condition is required")
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return nil
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *routeWorkflowContext) SetQueryHandler(string, any) error {
	return nil
}

func (r *routeWorkflowContext) StartChildWorkflow(
	_ context.Context,
	request engine.ChildWorkflowRequest,
) (engine.ChildWorkflowHandle, error) {
	return &testChildHandle{
		runtime: r.childRuntime,
		request: request,
		wfCtx:   r,
	}, nil
}

func (r *routeWorkflowContext) PublishRecord(call engine.RecordActivityCall) error {
	r.lastHookCall = call
	if call.Name != recordActivityName {
		return fmt.Errorf("unexpected record activity name %q", call.Name)
	}
	if r.hookRuntime == nil {
		return nil
	}
	return r.hookRuntime.recordActivity(r.Context(), call.Input)
}

func (r *routeWorkflowContext) ExecutePlannerActivity(
	call engine.PlannerActivityCall,
) (*api.PlanActivityOutput, error) {
	r.lastPlannerCall = call
	handler, ok := r.plannerRoutes[call.Name]
	if !ok {
		return nil, fmt.Errorf("no planner route for activity %q", call.Name)
	}
	return handler(r.Context(), call.Input)
}

func (r *routeWorkflowContext) ExecuteLimitFinalizationActivity(
	call engine.LimitFinalizationActivityCall,
) (*api.LimitFinalizationActivityOutput, error) {
	r.lastLimitCall = call
	handler, ok := r.limitRoutes[call.Name]
	if !ok {
		return nil, fmt.Errorf("no limit finalization route for activity %q", call.Name)
	}
	return handler(r.Context(), call.Input)
}

func (r *routeWorkflowContext) ExecuteToolActivity(
	call engine.ToolActivityCall,
) (*api.ToolOutput, error) {
	future, err := r.ExecuteToolActivityAsync(call)
	if err != nil {
		return nil, err
	}
	return future.Get(r.Context())
}

func (r *routeWorkflowContext) ExecuteToolActivityAsync(
	call engine.ToolActivityCall,
) (engine.Future[*api.ToolOutput], error) {
	r.lastToolCall = call
	handler, ok := r.toolRoutes[call.Name]
	if !ok {
		return nil, fmt.Errorf("no tool route for activity %q", call.Name)
	}
	future := &testToolFuture{}
	future.result, future.err = handler(r.Context(), call.Input)
	return future, nil
}
