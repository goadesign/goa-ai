// Package runtime starts direct Agent operations without publishing them as
// tools or registering provider code in the caller. The engine owns the accepted
// request before the provider prepares messages and runs the existing Agent loop.
package runtime

import (
	"context"
	"errors"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	registrycontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

// StartOperation accepts one sessionful call to a generated Agent operation.
// The host must already own the session and authorize the selected declaration.
// Provider preparation and effects belong to the accepted workflow; callers only
// observe its handle. The selected configuration owns planner policy.
func (r *Runtime) StartOperation(
	ctx context.Context, sessionID, runID string, operation *genregistry.ToolSchema,
	arguments rawjson.Message, labels map[string]string, memo map[string]any,
) (engine.WorkflowHandle, error) {
	if sessionID == "" {
		return nil, ErrMissingSessionID
	}
	return r.startOperation(ctx, sessionID, runID, operation, arguments, labels, memo)
}

// StartOneShotOperation accepts one sessionless call without creating a hidden
// conversation. Exact retries observe the same workflow; changed input conflicts.
func (r *Runtime) StartOneShotOperation(
	ctx context.Context, runID string, operation *genregistry.ToolSchema,
	arguments rawjson.Message, labels map[string]string, memo map[string]any,
) (engine.WorkflowHandle, error) {
	return r.startOperation(ctx, "", runID, operation, arguments, labels, memo)
}

// selectedRunTool returns the retained direct operation or delegated tool
// contract. Both use this result to validate typed planner output.
func selectedRunTool(context run.Context, executor agent.Ident, lookup toolSpecLookup) (*tools.ToolSpec, error) {
	if context.Operation == nil {
		return selectedParentTool(context.Tool, context.ToolRegistry, executor, lookup)
	}
	if context.ToolRegistry != nil {
		return nil, errors.New("direct operation cannot include a Registry publication")
	}
	spec, err := compileAgentOperation(context.Operation, executor)
	if err != nil {
		return nil, err
	}
	if spec.Name != context.Tool {
		return nil, errors.New("direct operation name differs from the selected contract")
	}
	return &spec, nil
}

// compileAgentOperation uses generated declaration validation and requires the
// selected executor to own the operation. It does not grant application access.
func compileAgentOperation(operation *genregistry.ToolSchema, executor agent.Ident) (tools.ToolSpec, error) {
	spec, err := registrycontract.Compile(operation)
	if err != nil {
		return tools.ToolSpec{}, err
	}
	if !spec.IsAgentTool || spec.AgentID != string(executor) {
		return tools.ToolSpec{}, fmt.Errorf("operation %q does not target Agent %q", spec.Name, executor)
	}
	return spec, nil
}

// providerOperationSpec requires the selected declaration to match this worker's
// generated export and route before either direct or delegated preparation.
func providerOperationSpec(definition AgentDefinition, declaration *genregistry.ToolSchema) (tools.ToolSpec, error) {
	selected, err := compileAgentOperation(declaration, definition.route.ID)
	if err != nil {
		return tools.ToolSpec{}, err
	}
	target := declaration.ConsumerContract.Agent
	if target.Workflow != definition.route.WorkflowName || target.Queue != definition.route.DefaultTaskQueue {
		return tools.ToolSpec{}, errors.New("agent operation does not match the provider's registered route")
	}
	owned, exists := definition.spec(selected.Name)
	if !exists || !owned.IsAgentTool || owned.AgentID != string(definition.route.ID) || !equivalentToolSpec(owned, selected) {
		return tools.ToolSpec{}, fmt.Errorf("operation %q differs from the provider's generated export", selected.Name)
	}
	return selected, nil
}

// startOperation checks the public input and submits its exact operation
// declaration. It neither publishes messages nor calls a provider in the caller.
func (r *Runtime) startOperation(
	ctx context.Context, sessionID, runID string, operation *genregistry.ToolSchema,
	arguments rawjson.Message, labels map[string]string, memo map[string]any,
) (engine.WorkflowHandle, error) {
	request, err := prepareOperationRequest(sessionID, runID, operation, arguments, labels, memo)
	if err != nil {
		return nil, err
	}
	return r.startWorkflow(ctx, request)
}

// prepareOperationRequest builds the complete immutable engine request for
// both initial submission and exact completion repair. It performs no I/O.
func prepareOperationRequest(sessionID, runID string, operation *genregistry.ToolSchema, arguments rawjson.Message, labels map[string]string, memo map[string]any) (engine.WorkflowStartRequest, error) {
	if runID == "" {
		return engine.WorkflowStartRequest{}, errors.New("operation run id is required")
	}
	if operation == nil || operation.ConsumerContract == nil || operation.ConsumerContract.Agent == nil {
		return engine.WorkflowStartRequest{}, errors.New("generated Agent operation declaration is required")
	}
	target := operation.ConsumerContract.Agent
	executor := agent.Ident(target.Executor)
	spec, err := compileAgentOperation(operation, executor)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if _, err := spec.Payload.Codec.FromJSON(arguments); err != nil {
		return engine.WorkflowStartRequest{}, fmt.Errorf("operation %q input: %w", spec.Name, err)
	}
	definition := NewAgentDefinition(AgentRoute{
		ID: executor, WorkflowName: target.Workflow, DefaultTaskQueue: target.Queue,
	}, nil, nil, nil, nil, nil, nil)
	start := runStart{input: RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(executor))}, SessionID: sessionID, RunID: runID, Labels: cloneLabels(labels)}}
	start.input.Tool = spec.Name
	start.input.ToolArgs = append(rawjson.Message(nil), arguments...)
	start.input.Operation = operation
	launch, err := encodeWorkflowLaunch(WorkflowOptions{Memo: memo})
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	request, err := prepareRunWithDefinition(&start.input, launch, definition, sessionID != "")
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	return request, nil
}

// validateDirectOperationInput rejects parent identity and mixed declarations
// before an accepted root can schedule preparation or execute business work.
func validateDirectOperationInput(input *api.RunInput) error {
	if input.RegisteredInvocation != nil {
		_, err := registeredAgentDeclaration(input)
		return err
	}
	if input.Operation == nil {
		return errors.New("direct Agent operation is required")
	}
	if input.Continuation != nil || input.ToolRegistry != nil || input.ParentRunID != "" || input.ParentAgentID != "" || input.ParentToolCallID != "" {
		return errors.New("direct Agent operation cannot include continuation, publication or parent identity")
	}
	executor, isAgent := input.Subject.Executor.AsAgent()
	if !isAgent {
		return errors.New("direct operation requires an Agent subject")
	}
	spec, err := compileAgentOperation(input.Operation, agent.Ident(executor))
	if err != nil {
		return err
	}
	if spec.Name != input.Tool {
		return errors.New("direct operation name differs from the selected contract")
	}
	if _, err := spec.Payload.Codec.FromJSON(input.ToolArgs); err != nil {
		return fmt.Errorf("operation %q input: %w", spec.Name, err)
	}
	return nil
}
