package runtime

import (
	"context"
	"errors"
	"fmt"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	finalizerInvokerContextKey struct{}

	finalizerInvokerFactory struct {
		runtime         *Runtime
		wfCtx           engine.WorkflowContext
		activityName    string
		activityOptions engine.ActivityOptions
		agentID         agent.Ident
	}

	toolInvokerMeta struct {
		RunID            string
		SessionID        string
		TurnID           string
		ParentToolCallID string
		AgentID          agent.Ident
	}

	finalizerToolInvoker struct {
		factory *finalizerInvokerFactory
		meta    toolInvokerMeta
		counter int
	}
)

func withFinalizerInvokerFactory(ctx context.Context, factory *finalizerInvokerFactory) context.Context {
	if factory == nil {
		return ctx
	}
	return context.WithValue(ctx, finalizerInvokerContextKey{}, factory)
}

func finalizerInvokerFactoryFromContext(ctx context.Context) *finalizerInvokerFactory {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(finalizerInvokerContextKey{}); v != nil {
		if factory, ok := v.(*finalizerInvokerFactory); ok {
			return factory
		}
	}
	return nil
}

func finalizerToolInvokerFromContext(ctx context.Context, call *planner.ToolRequest) ToolInvoker {
	factory := finalizerInvokerFactoryFromContext(ctx)
	if factory == nil || call == nil {
		return nil
	}
	meta := toolInvokerMeta{
		RunID:            call.RunID,
		SessionID:        call.SessionID,
		TurnID:           call.TurnID,
		ParentToolCallID: call.ToolCallID,
		AgentID:          call.AgentID,
	}
	return &finalizerToolInvoker{
		factory: factory,
		meta:    meta,
	}
}

func (i *finalizerToolInvoker) Invoke(ctx context.Context, tool tools.Ident, payload any) (*planner.ToolResult, error) {
	if i == nil || i.factory == nil || i.factory.runtime == nil {
		return nil, errors.New("finalizer tool invoker not configured")
	}
	if tool == "" {
		return nil, errors.New("tool identifier is required")
	}
	spec, ok := i.factory.runtime.toolSpec(tool)
	if !ok {
		return nil, fmt.Errorf("finalizer tool invoker: unknown tool %q", tool)
	}
	parent := i.meta
	raw, err := i.factory.runtime.marshalToolValue(ctx, tool, payload, true)
	if err != nil {
		return nil, err
	}
	call := planner.ToolRequest{
		Name:             tool,
		Payload:          raw,
		RunID:            parent.RunID,
		SessionID:        parent.SessionID,
		TurnID:           parent.TurnID,
		ParentToolCallID: parent.ParentToolCallID,
		AgentID:          parent.AgentID,
		ToolCallID:       generateDeterministicToolCallID(parent.RunID, parent.TurnID, tool, i.counter),
	}
	i.counter++

	if spec.IsAgentTool {
		i.factory.runtime.mu.RLock()
		reg, ok := i.factory.runtime.toolsets[spec.Toolset]
		i.factory.runtime.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("finalizer tool invoker: toolset %q not registered", spec.Toolset)
		}
		ctxInline := engine.WithWorkflowContext(ctx, i.factory.wfCtx)
		result, err := reg.Execute(ctxInline, &call)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errors.New("finalizer tool invoker: tool returned nil result")
		}
		return result, nil
	}

	callOpts := i.factory.activityOptions
	if callOpts.Queue == "" {
		i.factory.runtime.mu.RLock()
		reg, ok := i.factory.runtime.toolsets[spec.Toolset]
		i.factory.runtime.mu.RUnlock()
		if ok && reg.TaskQueue != "" {
			callOpts.Queue = reg.TaskQueue
		}
	}

	out, err := i.factory.wfCtx.ExecuteToolActivity(ctx, engine.ToolActivityCall{
		Name: i.factory.activityName,
		Input: &ToolInput{
			AgentID:          parent.AgentID,
			RunID:            parent.RunID,
			ToolsetName:      spec.Toolset,
			ToolName:         tool,
			ToolCallID:       call.ToolCallID,
			Payload:          raw,
			SessionID:        parent.SessionID,
			TurnID:           parent.TurnID,
			ParentToolCallID: parent.ParentToolCallID,
		},
		Options: callOpts,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("finalizer tool invoker: tool activity returned nil output")
	}

	var decoded any
	if len(out.Payload) > 0 {
		v, decErr := i.factory.runtime.unmarshalToolValue(ctx, tool, out.Payload, false)
		if decErr != nil {
			return nil, fmt.Errorf("finalizer tool %q result decode failed (tool_call_id=%s): %w", tool, call.ToolCallID, decErr)
		}
		decoded = v
	}

	result := &planner.ToolResult{
		Name:       tool,
		Result:     decoded,
		ToolCallID: call.ToolCallID,
		Telemetry:  out.Telemetry,
	}
	if out.Error != "" {
		result.Error = planner.NewToolError(out.Error)
	}
	if out.RetryHint != nil {
		result.RetryHint = out.RetryHint
	}
	return result, nil
}
