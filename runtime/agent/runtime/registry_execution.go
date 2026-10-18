// Package runtime dispatches saved registry calls through the same admission and
// result-stream implementation used by static registry executors. Both initial
// submission and overload recovery require the originally selected token.
package runtime

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent"

	"goa.design/goa-ai/internal/registrycall"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	registryExecutionSpecs map[tools.Ident]tools.ToolSpec
)

// executeRegistryTool uses the call's saved schemas for decoding and sends its
// token to the registry before any provider can execute the request.
func (r *Runtime) executeRegistryTool(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
	agentExecutor, isAgent := call.Subject.Executor.AsAgent()
	if !isAgent {
		return nil, fmt.Errorf("registry call requires an Agent workflow subject")
	}
	resolved, err := r.resolveRegistryExecution(agent.Ident(agentExecutor), *call)
	if err != nil {
		return nil, err
	}
	connection, err := r.registryConnection(call.Registry.Registry)
	if err != nil {
		return nil, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return nil, err
	}
	client := registrycall.NewResolvedClient(connection.client, resolved.Registered.RegistrationToken, declaration.Name)
	executor, err := registrycall.New(client, connection.pulse, resolved.Registered.Toolset.Name,
		registryExecutionSpecs(resolved.Specs), r.registryCallOptions...)
	if err != nil {
		return nil, err
	}
	meta := &toolregistry.ToolCallMeta{
		RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID,
		ToolCallID: call.ToolCallID, ParentToolCallID: call.ParentToolCallID,
		Labels: cloneLabels(call.Labels), TextOnly: call.TextOnly,
	}
	result, err := executor.Execute(ctx, meta, call)
	if err != nil {
		return nil, err
	}
	if result.PendingExecution != nil {
		return Unfinished(result.PendingExecution)
	}
	return Executed(executor.DecodeCompletedResult(call, meta, result)), nil
}

// Spec supplies the already-compiled saved contract to result-stream decoding.
func (s registryExecutionSpecs) Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	spec, ok := s[name]
	return &spec, ok
}
