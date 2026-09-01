// Package runtime prepares child-agent inputs in activities so workflow replay
// reuses recorded prompt text instead of reading prompt storage again.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

const agentChildActivityName = "runtime.prepare_agent_child"

// prepareAgentChildActivity decodes the parent tool payload, renders the child
// prompt, and returns the exact values that workflow history must retain.
func (r *Runtime) prepareAgentChildActivity(ctx context.Context, input *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
	cfg, err := r.agentToolConfig(input.Call.Name)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	request, err := r.buildAgentChildRequest(
		ctx,
		cfg,
		&input.Call,
		input.Messages,
		&input.ParentRun,
	)
	if err != nil {
		if failure := buildToolFailureFromAgentToolRequestError(err); failure != nil {
			return &api.AgentChildActivityOutput{Failure: failure}, nil
		}
		return nil, err
	}
	return &api.AgentChildActivityOutput{
		Success: &api.AgentChildActivitySuccess{
			Messages:        request.messages,
			RenderedPrompts: request.renderedPrompts,
		},
	}, nil
}

// prepareAgentChild schedules prompt rendering outside workflow code and
// converts the recorded result into the runtime's private child request.
func (r *Runtime) prepareAgentChild(wfCtx engine.WorkflowContext, call ToolCall, messages []*model.Message, parentRun run.Context) (agentChildRequest, error) {
	output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name: agentChildActivityName,
		Input: &api.AgentChildActivityInput{
			Call:      call,
			Messages:  messages,
			ParentRun: parentRun,
		},
	})
	if err != nil {
		return agentChildRequest{}, err
	}
	if output == nil {
		return agentChildRequest{}, errors.New("agent child activity returned nil output")
	}
	switch {
	case output.Success != nil && output.Failure == nil:
		return agentChildRequest{
			messages:        output.Success.Messages,
			runContext:      agentChildRunContext(&call),
			renderedPrompts: output.Success.RenderedPrompts,
		}, nil
	case output.Success == nil && output.Failure != nil:
		return agentChildRequest{}, &agentChildRequestFailure{failure: output.Failure}
	default:
		return agentChildRequest{}, errors.New("agent child activity must return exactly one of success or failure")
	}
}

// agentToolConfig returns the immutable child-agent configuration owned by
// the registered toolset for name.
func (r *Runtime) agentToolConfig(name tools.Ident) (*AgentToolConfig, error) {
	spec, ok := r.toolSpec(name)
	if !ok {
		return nil, fmt.Errorf("agent tool %q is not registered", name)
	}
	if !spec.IsAgentTool {
		return nil, fmt.Errorf("tool %q is not an agent tool", name)
	}
	_, toolset, ok := r.toolsetForTool(name)
	if !ok {
		return nil, fmt.Errorf("agent tool %q has no registered toolset", name)
	}
	if toolset.AgentTool == nil {
		return nil, fmt.Errorf("agent tool %q has no child-agent configuration", name)
	}
	return toolset.AgentTool, nil
}
