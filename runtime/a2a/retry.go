package a2a

import (
	"errors"

	a2aretry "goa.design/goa-ai/runtime/a2a/retry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// ErrorToRetryHint maps an A2A JSON-RPC error to a planner retry hint using
// the schema and example information from the corresponding SkillConfig.
// It focuses on invalid params and method-not-found conditions where retries
// are meaningful.
func ErrorToRetryHint(skill SkillConfig, err error) *planner.RetryHint {
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		return nil
	}

	switch rpcErr.Code {
	case JSONRPCInvalidParams:
		// Use schema and example from SkillConfig to build a structured repair prompt.
		prompt := a2aretry.BuildRepairPrompt(
			"tasks/send:"+skill.ID,
			rpcErr.Message,
			skill.ExampleArgs,
			string(skill.Payload.Schema),
		)
		return &planner.RetryHint{
			Reason:         planner.RetryReasonInvalidArguments,
			Tool:           tools.Ident(skill.ID),
			Message:        prompt,
			RestrictToTool: true,
		}
	case JSONRPCMethodNotFound:
		return &planner.RetryHint{
			Reason:  planner.RetryReasonToolUnavailable,
			Tool:    tools.Ident(skill.ID),
			Message: rpcErr.Message,
		}
	default:
		return nil
	}
}

// DefaultRetryHint is a convenience wrapper that looks up the SkillConfig by
// tool identifier and delegates to ErrorToRetryHint.
func DefaultRetryHint(skillMap map[tools.Ident]SkillConfig, toolName tools.Ident, err error) *planner.RetryHint {
	skill, ok := skillMap[toolName]
	if !ok {
		return nil
	}
	return ErrorToRetryHint(skill, err)
}


