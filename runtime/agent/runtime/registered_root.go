// Package runtime executes a retained Registry selection on its declared
// worker. Service roots require no Agent, and the engine owns each request
// before provider work begins.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	registeredRootWorkflow = "runtime.registered_tool"
	registeredRootActivity = "runtime.execute_registered_root"
)

// WithToolWorkflowQueue selects the worker queue that owns service-tool roots.
// Submitters and workers must use the same queue; callers never choose a route.
func WithToolWorkflowQueue(queue string) RuntimeOption {
	return func(options *Options) { options.toolWorkflowQueue = queue }
}

// StartToolOperation durably submits one exact registered operation in
// an existing Session. The engine owns the request before provider work starts.
// Only WithTextOnly is accepted in opts; other options fail before submission.
func (r *Runtime) StartToolOperation(ctx context.Context, sessionID, registry string, resolved *genregistry.ResolvedToolset, invocation *genregistry.RegisteredInvocation, labels map[string]string, memo map[string]any, opts ...RunOption) (engine.WorkflowHandle, error) {
	if sessionID == "" {
		return nil, ErrMissingSessionID
	}
	request, err := r.prepareRegisteredRootRequest(sessionID, registry, resolved, invocation, labels, memo, opts...)
	if err != nil {
		return nil, err
	}
	return r.startWorkflow(ctx, request)
}

// StartOneShotToolOperation submits one registered operation without a
// Session. Exact retries attach to the original engine request. Only
// WithTextOnly is accepted in opts.
func (r *Runtime) StartOneShotToolOperation(ctx context.Context, registry string, resolved *genregistry.ResolvedToolset, invocation *genregistry.RegisteredInvocation, labels map[string]string, memo map[string]any, opts ...RunOption) (engine.WorkflowHandle, error) {
	request, err := r.prepareRegisteredRootRequest("", registry, resolved, invocation, labels, memo, opts...)
	if err != nil {
		return nil, err
	}
	return r.startWorkflow(ctx, request)
}

// EnsureToolOperationCompletion delivers the engine's outcome for the exact
// sessionful input, including failure before the workflow recorded its start.
// Pass the same WithTextOnly option used at start; other options are rejected.
func (r *Runtime) EnsureToolOperationCompletion(ctx context.Context, sessionID, registry string, resolved *genregistry.ResolvedToolset, invocation *genregistry.RegisteredInvocation, labels map[string]string, memo map[string]any, opts ...RunOption) error {
	if sessionID == "" {
		return ErrMissingSessionID
	}
	request, err := r.prepareRegisteredRootRequest(sessionID, registry, resolved, invocation, labels, memo, opts...)
	if err != nil {
		return err
	}
	return r.ensureOperationCompletion(ctx, request)
}

// EnsureOneShotToolOperationCompletion repairs the exact sessionless execution;
// it never starts work or treats missing engine history as successful completion.
// Pass the same WithTextOnly option used at start; other options are rejected.
func (r *Runtime) EnsureOneShotToolOperationCompletion(ctx context.Context, registry string, resolved *genregistry.ResolvedToolset, invocation *genregistry.RegisteredInvocation, labels map[string]string, memo map[string]any, opts ...RunOption) error {
	request, err := r.prepareRegisteredRootRequest("", registry, resolved, invocation, labels, memo, opts...)
	if err != nil {
		return err
	}
	return r.ensureOperationCompletion(ctx, request)
}

// prepareRegisteredRootRequest copies the generated operation request and binds
// its exact service registration to the schema used to validate original input.
func (r *Runtime) prepareRegisteredRootRequest(sessionID, registry string, resolved *genregistry.ResolvedToolset, invocation *genregistry.RegisteredInvocation, labels map[string]string, memo map[string]any, opts ...RunOption) (engine.WorkflowStartRequest, error) {
	policy, err := exactOperationPolicy(opts)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	encoded, err := genregistry.EncodeRegisteredInvocation(invocation)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	retained, err := genregistry.DecodeRegisteredInvocation(encoded)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	root, ok := retained.Invocation.Invocation.AsRoot()
	if !ok {
		return engine.WorkflowStartRequest{}, errors.New("registered root requires root invocation")
	}
	if native, ok := retained.Target.Target.AsNative(); ok {
		resolution, err := registrycontract.Resolve(resolved)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		if native.Toolset != resolved.Toolset.Name {
			return engine.WorkflowStartRequest{}, errors.New("native target differs from selected revision")
		}
		binding, err := resolution.Select(registry, tools.Ident(native.Tool))
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		for _, declaration := range resolved.Toolset.Tools {
			if declaration.Name != native.Tool {
				continue
			}
			if declaration.ConsumerContract.Agent == nil {
				return engine.WorkflowStartRequest{}, errors.New("native target requires Agent declaration")
			}
			route := declaration.ConsumerContract.Agent
			input := &api.RunInput{Policy: policy, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(route.Executor))}, RunID: root.RunID, SessionID: sessionID, RegisteredInvocation: retained, ToolRegistry: binding, Labels: cloneLabels(labels)}
			input.TurnID, err = lifecycle.RegisteredRunTurnID(input.Subject, input.RunID, "")
			if err != nil {
				return engine.WorkflowStartRequest{}, err
			}
			if _, err := registeredAgentDeclaration(input); err != nil {
				return engine.WorkflowStartRequest{}, err
			}
			launch, err := encodeWorkflowLaunch(WorkflowOptions{Memo: memo})
			if err != nil {
				return engine.WorkflowStartRequest{}, err
			}
			return engine.WorkflowStartRequest{ID: root.RunID, Workflow: route.Workflow, TaskQueue: route.Queue, Input: input, Memo: launch.memo}, nil
		}
		return engine.WorkflowStartRequest{}, errors.New("native operation absent from retained revision")
	}
	if r.toolWorkflowQueue == "" {
		return engine.WorkflowStartRequest{}, errors.New("service-tool workflow queue is required")
	}
	source, ok := retained.Target.Target.AsService()
	if !ok {
		return engine.WorkflowStartRequest{}, errors.New("registered service root requires service target")
	}
	resolution, err := registrycontract.Resolve(resolved)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if source.Toolset != resolved.Toolset.Name || source.RegistrationToken != resolved.RegistrationToken {
		return engine.WorkflowStartRequest{}, errors.New("registered target differs from selected registration")
	}
	binding, err := resolution.Select(registry, tools.Ident(source.Tool))
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	input := &api.RunInput{Policy: policy, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: registry, Tool: source.Tool})}, RunID: root.RunID, SessionID: sessionID, RegisteredInvocation: retained, ToolRegistry: binding, Labels: cloneLabels(labels)}
	input.TurnID, err = lifecycle.RegisteredRunTurnID(input.Subject, input.RunID, "")
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if _, err := validateRegisteredRootInput(input); err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	launch, err := encodeWorkflowLaunch(WorkflowOptions{Memo: memo})
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	return engine.WorkflowStartRequest{ID: root.RunID, Workflow: registeredRootWorkflow, TaskQueue: r.toolWorkflowQueue, Input: input, Memo: launch.memo}, nil
}

// validateRegisteredRootInput rejects mixed Agent state and checks the exact
// complete service arguments against the retained execution contract before the
// worker sends the selected operation to its provider. Provider-injected fields
// remain excluded from caller input.
func validateRegisteredRootInput(input *api.RunInput) (*registrycontract.Resolution, error) {
	if input == nil {
		return nil, errors.New("registered input is required")
	}
	if err := validateExactOperationPolicy(input.Policy); err != nil {
		return nil, err
	}
	subject, ok := input.Subject.Executor.AsServiceTool()
	if !ok {
		return nil, errors.New("registered service root requires service subject")
	}
	if input.Operation != nil || input.Continuation != nil || input.TurnID != "" || input.ParentRunID != "" || input.ParentAgentID != "" || input.ParentToolCallID != "" || input.Tool != "" || len(input.ToolArgs) != 0 || len(input.Metadata) != 0 {
		return nil, errors.New("registered service root cannot include Agent or duplicate operation state")
	}
	if _, err := genregistry.EncodeRegisteredInvocation(input.RegisteredInvocation); err != nil {
		return nil, err
	}
	request := input.RegisteredInvocation
	root, ok := request.Invocation.Invocation.AsRoot()
	if !ok || root.RunID == "" {
		return nil, errors.New("registered service operation requires original root identity")
	}
	selected, ok := request.Target.Target.AsService()
	if !ok {
		return nil, errors.New("registered service operation requires service target")
	}
	resolution, err := registrycontract.Read(input.ToolRegistry, nil)
	if err != nil {
		return nil, err
	}
	if subject.Registry != input.ToolRegistry.Registry || subject.Tool != selected.Tool || selected.Toolset != resolution.Registered.Toolset.Name || selected.RegistrationToken != resolution.Registered.RegistrationToken {
		return nil, errors.New("registered service subject and retained target differ")
	}
	spec, ok := resolution.Specs[tools.Ident(subject.Tool)]
	if !ok || spec.IsAgentTool {
		return nil, errors.New("registered selection is not a service operation")
	}
	spec, err = exactOperationSpec(spec, input.Policy)
	if err != nil {
		return nil, err
	}
	if _, err := spec.ExecutionPayloadCodec.FromJSON(request.Arguments); err != nil {
		return nil, fmt.Errorf("registered arguments: %w", err)
	}
	for _, declaration := range resolution.Registered.Toolset.Tools {
		if declaration.Name != subject.Tool {
			continue
		}
		for _, label := range declaration.ConsumerContract.RequiredLabels {
			if input.Labels[label] == "" {
				return nil, fmt.Errorf("registered operation requires label %q", label)
			}
		}
	}
	return resolution, nil
}
