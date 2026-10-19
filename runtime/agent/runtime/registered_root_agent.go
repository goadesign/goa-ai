// Package runtime runs registered native operations on the selected Agent
// worker and retains the original input through preparation and continuation.
package runtime

import (
	"bytes"
	"errors"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/tools"
)

// registeredAgentDeclaration checks the retained native selection against the
// Agent subject and returns its exact generated provider declaration.
func registeredAgentDeclaration(input *api.RunInput) (*genregistry.ToolSchema, error) {
	if _, err := genregistry.EncodeRegisteredInvocation(input.RegisteredInvocation); err != nil {
		return nil, err
	}
	request := input.RegisteredInvocation
	root, ok := request.Invocation.Invocation.AsRoot()
	if !ok || root.RunID != input.RunID {
		return nil, errors.New("registered native root requires its original run identity")
	}
	target, ok := request.Target.Target.AsNative()
	if !ok {
		return nil, errors.New("registered Agent requires native target")
	}
	executor, ok := input.Subject.Executor.AsAgent()
	if !ok {
		return nil, errors.New("registered native root requires Agent subject")
	}
	resolution, err := registrycontract.Read(input.ToolRegistry)
	if err != nil {
		return nil, err
	}
	if resolution.Registered.Toolset.Name != target.Toolset {
		return nil, errors.New("native target differs from retained revision")
	}
	if input.Operation != nil || input.ParentRunID != "" || input.ParentAgentID != "" || input.ParentToolCallID != "" || input.Continuation != nil {
		return nil, errors.New("registered native root cannot include direct operation or parent identity")
	}
	for _, declaration := range resolution.Registered.Toolset.Tools {
		if declaration.Name != target.Tool {
			continue
		}
		spec, err := compileAgentOperation(declaration, agent.Ident(executor))
		if err != nil {
			return nil, err
		}
		spec, err = exactOperationSpec(spec, input.Policy)
		if err != nil {
			return nil, err
		}
		if _, err := spec.ExecutionPayloadCodec.FromJSON(request.Arguments); err != nil {
			return nil, err
		}
		for _, label := range declaration.ConsumerContract.RequiredLabels {
			if input.Labels[label] == "" {
				return nil, fmt.Errorf("registered native operation requires label %q", label)
			}
		}
		if input.SeedEndID == "" && (input.Tool != "" || len(input.ToolArgs) != 0) {
			return nil, errors.New("fresh native root cannot repeat derived operation input")
		}
		return declaration, nil
	}
	return nil, fmt.Errorf("retained native selection contains no tool %q", tools.Ident(target.Tool))
}

// registeredCheckpointDeclaration verifies the saved selection and original input
// before a continuation derives its provider route from the retained selection.
func registeredCheckpointDeclaration(checkpoint *workflowCheckpoint) (*genregistry.ToolSchema, error) {
	request := checkpoint.Context.RegisteredInvocation
	if _, err := genregistry.EncodeRegisteredInvocation(request); err != nil {
		return nil, err
	}
	root, ok := request.Invocation.Invocation.AsRoot()
	if !ok {
		return nil, errors.New("registered checkpoint requires original root invocation")
	}
	input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(checkpoint.AgentID))}, RunID: root.RunID, SessionID: checkpoint.SessionID, RegisteredInvocation: request, Operation: checkpoint.Context.Operation, ToolRegistry: checkpoint.Context.ToolRegistry, Labels: checkpoint.Context.Labels, ParentRunID: checkpoint.Context.ParentRunID, ParentAgentID: checkpoint.Context.ParentAgentID, ParentToolCallID: checkpoint.Context.ParentToolCallID}
	input.Policy = &PolicyOverrides{TextOnly: checkpoint.Context.TextOnly}
	declaration, err := registeredAgentDeclaration(input)
	if err != nil {
		return nil, err
	}
	if declaration.Name != string(checkpoint.Context.Tool) || !bytes.Equal(request.Arguments, checkpoint.Context.ToolArgs) {
		return nil, errors.New("registered checkpoint changed its original operation input")
	}
	return declaration, nil
}
