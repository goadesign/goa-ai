package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/engine"
)

var errInvalidToolActivityInput = errors.New("invalid tool activity input")

// WorkflowHandler returns a generic workflow handler that type-asserts the
// input to *RunInput and delegates to Runtime.ExecuteWorkflow. Use this to avoid
// generating per-agent boilerplate handlers.
func WorkflowHandler(rt *Runtime) engine.WorkflowFunc {
	return func(wfctx engine.WorkflowContext, input any) (any, error) {
		var in *RunInput
		switch v := input.(type) {
		case *RunInput:
			in = v
		case RunInput:
			in = &v
		default:
			// Best-effort decode: JSON round-trip into RunInput to survive generic decoders
			// used by workflow engines (e.g., Temporal JSON payloads).
			if v == nil {
				return nil, errors.New("invalid run input")
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, errors.New("invalid run input")
			}
			var tmp RunInput
			if err := json.Unmarshal(b, &tmp); err != nil {
				return nil, errors.New("invalid run input")
			}
			in = &tmp
		}
		return rt.ExecuteWorkflow(wfctx, in)
	}
}

// WorkflowHandlerTyped returns a typed workflow handler that Temporal can decode into directly.
// (Typed workflow handler removed; use WorkflowHandler with coercion.)

// PlanStartActivityHandler returns a generic activity handler for the plan-start
// activity. It type-asserts the input to PlanActivityInput and delegates to
// Runtime.PlanStartActivity.
func PlanStartActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		var in PlanActivityInput
		switch v := input.(type) {
		case PlanActivityInput:
			in = v
		case *PlanActivityInput:
			if v == nil {
				return nil, errors.New("invalid plan activity input")
			}
			in = *v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, errors.New("invalid plan activity input")
			}
			if err := json.Unmarshal(b, &in); err != nil {
				return nil, errors.New("invalid plan activity input")
			}
		}
		return rt.PlanStartActivity(ctx, in)
	}
}

// PlanResumeActivityHandler returns a generic activity handler for the plan-resume
// activity. It type-asserts the input to PlanActivityInput and delegates to
// Runtime.PlanResumeActivity.
func PlanResumeActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		var in PlanActivityInput
		switch v := input.(type) {
		case PlanActivityInput:
			in = v
		case *PlanActivityInput:
			if v == nil {
				return nil, errors.New("invalid plan activity input")
			}
			in = *v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("invalid plan activity input: failed to marshal input (type %T): %w", v, err)
			}
			if err := json.Unmarshal(b, &in); err != nil {
				return nil, fmt.Errorf("invalid plan activity input: failed to unmarshal input (type %T, json: %s): %w", v, string(b), err)
			}
		}
		return rt.PlanResumeActivity(ctx, in)
	}
}

// ExecuteToolActivityHandler returns a generic activity handler for the execute-tool
// activity. It type-asserts the input to ToolInput and delegates to
// Runtime.ExecuteToolActivity.
func ExecuteToolActivityHandler(rt *Runtime) func(context.Context, any) (any, error) {
	return func(ctx context.Context, input any) (any, error) {
		var in *ToolInput
		switch v := input.(type) {
		case ToolInput:
			in = &v
		case *ToolInput:
			if v == nil {
				return nil, fmt.Errorf("%w: nil *ToolInput", errInvalidToolActivityInput)
			}
			in = v
		default:
			// Best-effort decode: JSON round-trip into ToolInput to survive generic
			// decoders used by workflow engines. Temporal's default JSON codec
			// deserializes into map[string]any when handlers accept `any`, so we
			// coerce that generic value back into the strong ToolInput envelope.
			if v == nil {
				return nil, fmt.Errorf("%w: nil input", errInvalidToolActivityInput)
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf(
					"%w: failed to marshal input (type %T): %w",
					errInvalidToolActivityInput, v, err,
				)
			}
			var tmp ToolInput
			if err := json.Unmarshal(b, &tmp); err != nil {
				return nil, fmt.Errorf(
					"%w: failed to unmarshal input (type %T, json: %s): %w",
					errInvalidToolActivityInput, v, string(b), err,
				)
			}
			in = &tmp
		}
		return rt.ExecuteToolActivity(ctx, in)
	}
}
