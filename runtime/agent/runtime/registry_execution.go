// Package runtime dispatches saved registry calls through the same admission and
// result-stream implementation used by static registry executors. Both initial
// submission and overload recovery require the originally selected token.
package runtime

import (
	"context"
	"fmt"
	"time"

	"goa.design/goa-ai/internal/registrycall"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	resolvedRegistryClient struct {
		client *genregistry.Client
		token  string
	}
	registryExecutionSpecs map[tools.Ident]tools.ToolSpec
)

// executeRegistryTool uses the call's saved schemas for decoding and sends its
// token to the registry before any provider can execute the request.
func (r *Runtime) executeRegistryTool(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
	resolved, err := r.resolveRegistryExecution(call.AgentID, *call)
	if err != nil {
		return nil, err
	}
	connection, err := r.registryConnection(call.Registry.Registry)
	if err != nil {
		return nil, err
	}
	client := resolvedRegistryClient{client: connection.client, token: resolved.Registered.RegistrationToken}
	executor, err := registrycall.New(client, connection.pulse, resolved.Registered.Toolset.Name,
		registryExecutionSpecs(resolved.Specs), r.registryCallOptions...)
	if err != nil {
		return nil, err
	}
	result, err := executor.Execute(ctx, &toolregistry.ToolCallMeta{
		RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID,
		ToolCallID: call.ToolCallID, ParentToolCallID: call.ParentToolCallID,
		Labels: cloneLabels(call.Labels),
	}, call)
	if err != nil {
		return nil, err
	}
	return Executed(result), nil
}

// CallTool admits only the registration whose definition produced the call.
func (c resolvedRegistryClient) CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (toolregistry.ToolCallRef, error) {
	result, err := c.client.CallResolvedTool(ctx, &genregistry.CallResolvedToolPayload{
		ExpectedRegistrationToken: c.token,
		Toolset:                   toolset, Tool: tool.String(), PayloadJSON: payload,
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID: meta.RunID, SessionID: meta.SessionID, ToolCallID: meta.ToolCallID,
			TurnID: &meta.TurnID, ParentToolCallID: &meta.ParentToolCallID,
			Labels: cloneLabels(meta.Labels),
		},
	})
	if err != nil {
		return toolregistry.ToolCallRef{}, err
	}
	if result.RegistrationToken != c.token {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry admitted a different registration for tool %q", tool)
	}
	deadline, err := time.Parse(time.RFC3339Nano, result.ExecutionDeadline)
	if err != nil {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry execution deadline: %w", err)
	}
	expiration, err := time.Parse(time.RFC3339Nano, result.ResultStreamExpiresAt)
	if err != nil {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry result expiration: %w", err)
	}
	return toolregistry.ToolCallRef{
		ToolUseID: result.ToolUseID, RegistrationToken: result.RegistrationToken,
		ExecutionDeadline: deadline, ResultStreamExpiresAt: expiration,
	}, nil
}

// RetryTool resumes the original call's registry-owned overload event.
// It cannot replace the selected registration or start a new call identity.
func (c resolvedRegistryClient) RetryTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta, token string) (toolregistry.ToolCallRef, error) {
	if token != c.token {
		return toolregistry.ToolCallRef{}, fmt.Errorf("registry retry token differs from the selected registration")
	}
	return c.CallTool(ctx, toolset, tool, payload, meta)
}

// Spec supplies the already-compiled saved contract to result-stream decoding.
func (s registryExecutionSpecs) Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	spec, ok := s[name]
	return &spec, ok
}
