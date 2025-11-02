package runtime

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/engine"
)

// WorkflowHandler returns a generic workflow handler that type-asserts the
// input to *RunInput and delegates to Runtime.ExecuteWorkflow. Use this to avoid
// generating per-agent boilerplate handlers.
func WorkflowHandler(rt *Runtime) engine.WorkflowFunc {
	return func(wfctx engine.WorkflowContext, input any) (any, error) {
		in, ok := input.(*RunInput)
		if !ok {
			return nil, errors.New("invalid run input")
		}
		return rt.ExecuteWorkflow(wfctx, in)
	}
}

// PlanStartActivityHandler returns a generic activity handler for the plan-start
// activity. It type-asserts the input to PlanActivityInput and delegates to
// Runtime.PlanStartActivity.
func PlanStartActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(PlanActivityInput)
		if !ok {
			return nil, errors.New("invalid plan activity input")
		}
		return rt.PlanStartActivity(ctx, in)
	}
}

// PlanResumeActivityHandler returns a generic activity handler for the plan-resume
// activity. It type-asserts the input to PlanActivityInput and delegates to
// Runtime.PlanResumeActivity.
func PlanResumeActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(PlanActivityInput)
		if !ok {
			return nil, errors.New("invalid plan activity input")
		}
		return rt.PlanResumeActivity(ctx, in)
	}
}

// ExecuteToolActivityHandler returns a generic activity handler for the execute-tool
// activity. It type-asserts the input to ToolInput and delegates to
// Runtime.ExecuteToolActivity.
func ExecuteToolActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		in, ok := input.(ToolInput)
		if !ok {
			return nil, errors.New("invalid tool activity input")
		}
		return rt.ExecuteToolActivity(ctx, in)
	}
}
