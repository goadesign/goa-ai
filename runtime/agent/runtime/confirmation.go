package runtime

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ToolConfirmationConfig configures runtime-enforced confirmation for specific
	// tools. When enabled, the runtime requires explicit operator approval before
	// executing configured tool calls.
	//
	// This is runtime-owned policy: planners do not need to special-case
	// confirmation flows for configured tools.
	ToolConfirmationConfig struct {
		// Confirm maps tool IDs to confirmation handlers.
		//
		// These handlers are an override mechanism used to:
		// - require confirmation for tools that do not declare design-time Confirmation
		// - override prompt/denied-result rendering for specific tool IDs
		Confirm map[tools.Ident]*ToolConfirmation
	}

	// ToolConfirmation defines how to request confirmation and how to represent
	// a user denial for a given tool.
	ToolConfirmation struct {
		// Prompt returns the deterministic prompt shown to the user for this call.
		Prompt func(ctx context.Context, call *planner.ToolRequest) (string, error)
		// DeniedResult constructs a schema-compatible tool result value representing
		// a user denial. The runtime attaches it to the original tool_call_id with
		// Error unset.
		DeniedResult func(ctx context.Context, call *planner.ToolRequest) (any, error)
	}
)

func (c *ToolConfirmationConfig) validate() error {
	if len(c.Confirm) == 0 {
		return nil
	}
	for id, h := range c.Confirm {
		if id == "" {
			return fmt.Errorf("%w: tool id is required", ErrInvalidConfig)
		}
		if h == nil {
			return fmt.Errorf("%w: confirmation handler for %q is nil", ErrInvalidConfig, id)
		}
		if h.Prompt == nil {
			return fmt.Errorf("%w: confirmation handler for %q missing Prompt", ErrInvalidConfig, id)
		}
		if h.DeniedResult == nil {
			return fmt.Errorf("%w: confirmation handler for %q missing DeniedResult", ErrInvalidConfig, id)
		}
	}
	return nil
}
