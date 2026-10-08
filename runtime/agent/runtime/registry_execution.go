// Package runtime dispatches saved registry calls through the same admission and
// result-stream implementation used by static registry executors. Both initial
// submission and overload recovery require the originally selected token.
package runtime

import (
	"context"
	"fmt"

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

// CallTool admits only the registration whose definition produced the call.
func (c resolvedRegistryClient) CallTool(ctx context.Context, payload *genregistry.CallToolPayload) (*genregistry.CallToolResult, error) {
	result, err := c.client.CallResolvedTool(ctx, &genregistry.CallResolvedToolPayload{
		ExpectedRegistrationToken: c.token,
		Toolset:                   payload.Toolset,
		Tool:                      payload.Tool,
		PayloadJSON:               payload.PayloadJSON,
		WireProtocolVersion:       payload.WireProtocolVersion,
		Meta:                      payload.Meta,
	})
	if err != nil {
		return nil, err
	}
	if result.RegistrationToken != c.token {
		return nil, fmt.Errorf("registry admitted a different registration for tool %q", payload.Tool)
	}
	return result, nil
}

// RetryTool resumes the original call's registry-owned overload event.
// It cannot replace the selected registration or start a new call identity.
func (c resolvedRegistryClient) RetryTool(ctx context.Context, payload *genregistry.RetryToolPayload) (*genregistry.CallToolResult, error) {
	if payload.ExpectedRegistrationToken != c.token {
		return nil, fmt.Errorf("registry retry token differs from the selected registration")
	}
	return c.CallTool(ctx, &genregistry.CallToolPayload{
		Toolset:             payload.Toolset,
		Tool:                payload.Tool,
		PayloadJSON:         payload.PayloadJSON,
		WireProtocolVersion: payload.WireProtocolVersion,
		Meta:                payload.Meta,
	})
}

// Spec supplies the already-compiled saved contract to result-stream decoding.
func (s registryExecutionSpecs) Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	spec, ok := s[name]
	return &spec, ok
}
