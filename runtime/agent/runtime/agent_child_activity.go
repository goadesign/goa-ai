// Package runtime prepares child-agent inputs in activities so workflow replay
// reuses recorded prompt text instead of reading prompt storage again.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/google/uuid"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

const agentChildActivityName = "runtime.prepare_agent_child"

// prepareAgentChildActivity decodes the parent tool payload, renders the child
// prompt, and returns the exact values that workflow history must retain.
func (r *Runtime) prepareAgentChildActivity(ctx context.Context, input *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()

	executor, isAgent := input.Call.Subject.Executor.AsAgent()
	if !isAgent {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("child preparation requires an Agent parent subject"))
	}
	if input.Call.RunID != input.ParentRun.RunID || input.Call.SessionID != input.ParentRun.SessionID {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("child call does not belong to the parent run"))
	}
	cfg, err := r.agentChildPreparationConfig(input.Call)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	commandBytes, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(commandBytes)
	operation := storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(string(cfg.Definition.route.ID)))}, RunID: agentChildRunContext(&input.Call).RunID, SessionID: input.ParentRun.SessionID, CommandID: hex.EncodeToString(digest[:])}
	origin := input.Origin
	decision, err := r.Store.BeginRunPreparationOperation(ctx, storage.RunPreparationBegin{Operation: operation, Origin: origin})
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if reason, stopped := decision.State.AsStop(); stopped {
		return &api.AgentChildActivityOutput{Stopped: &api.CancellationRequest{RunID: operation.RunID, Reason: string(reason)}}, nil
	}
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if found {
		return recoverChildPreparation(ctx, r.Store, accepted)
	}
	var request agentChildRequest
	var history AgentHistory
	if input.Call.Registry != nil {
		var config *AgentToolConfiguration
		config, err = r.prepareRegistryAgentChild(ctx, origin, input.Call)
		if err == nil {
			request = agentChildRequest{
				renderedPrompts: config.RenderedPrompts,
				policy:          config.Policy, runContext: agentChildRunContext(&input.Call),
			}
			request.runContext.Labels = config.Labels
			request.runContext.Metadata = config.Metadata
			history = config.History
		}
	} else {
		messages, historyErr := r.loadActivityHistory(ctx, agent.Ident(executor), input.ParentRun.RunID, input.ParentRun.SessionID, input.HistoryEndID)
		if historyErr != nil {
			return nil, historyErr
		}
		request, err = r.buildAgentChildRequest(ctx, cfg, &input.Call, messages, &input.ParentRun)
		history = LiteralAgentHistory(request.messages)
	}
	if err != nil {
		if failure := buildToolFailureFromAgentToolRequestError(err); failure != nil {
			return &api.AgentChildActivityOutput{Failure: failure}, nil
		}
		return nil, err
	}
	if input.Call.TextOnly {
		if request.policy == nil {
			request.policy = &PolicyOverrides{}
		}
		request.policy.TextOnly = true
	}
	// A selected child can require ordinary messages even when its parent allows
	// UI output. Check that child's invocation before saving its model input.
	if request.policy != nil && request.policy.TextOnly {
		spec, exists, err := r.callSpec(input.Call)
		if err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(err)
		}
		if !exists || spec.RequiresUI || spec.Confirmation != nil || spec.TextOnly == nil {
			return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("agent tool %q is incompatible with text-only child execution", input.Call.Name))
		}
		if _, err := spec.TextOnly.ExecutionCodec.FromJSON(input.Call.Payload); err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("agent tool %q text-only child input: %w", input.Call.Name, err))
		}
	}
	if _, err := agentChildRunInput(cfg.Definition, request); err != nil {
		return nil, err
	}
	writer, err := stageAgentHistory(ctx, r.Store, storage.SeedDeclaration{
		Origin:  origin,
		Subject: operation.Subject, RunID: operation.RunID, SessionID: operation.SessionID,
		CommandID: operation.CommandID, AttemptID: uuid.NewString(), RenderedPrompts: request.renderedPrompts,
	}, history)
	if err != nil {
		return r.recoverChildPublication(ctx, operation, err)
	}
	success := &api.AgentChildActivitySuccess{Metadata: request.runContext.Metadata, SeedEndID: writer.endID, Labels: request.runContext.Labels, Policy: request.policy}
	data, err := json.Marshal(success)
	if err != nil {
		return nil, err
	}
	if err := writer.publish(ctx, data); err != nil {
		return r.recoverChildPublication(ctx, operation, err)
	}
	return &api.AgentChildActivityOutput{Success: success}, nil
}

// recoverChildPublication checks for a winner after a candidate loses a write
// or reply. It preserves the original error when no complete value was accepted.
func (r *Runtime) recoverChildPublication(ctx context.Context, operation storage.PreparationOperation, cause error) (*api.AgentChildActivityOutput, error) {
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if found {
		return recoverChildPreparation(ctx, r.Store, accepted)
	}
	return nil, classifyStorageActivityError(cause)
}

// recoverChildPreparation returns the original rendered prompt's control
// result after a lost reply, without loading mutable registry or prompt data.
func recoverChildPreparation(ctx context.Context, store storage.Store, accepted storage.RunPreparation) (*api.AgentChildActivityOutput, error) {
	data, err := readPreparation(ctx, store, accepted)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	var success api.AgentChildActivitySuccess
	if err := json.Unmarshal(data, &success); err != nil {
		return nil, classifyStorageActivityError(storage.NewContractError(err))
	}
	if success.SeedEndID != accepted.Seed.EndID {
		return nil, classifyStorageActivityError(storage.NewContractError(storage.ErrSeedConflict))
	}
	return &api.AgentChildActivityOutput{Success: &success}, nil
}

// prepareAgentChild schedules prompt rendering outside workflow code and
// converts the recorded result into the runtime's private child request.
func (r *Runtime) prepareAgentChild(wfCtx engine.WorkflowContext, call ToolCall, historyEndID string, parentRun run.Context) (agentChildRequest, *api.CancellationRequest, error) {
	origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}
	call.RunID = parentRun.RunID
	var options engine.ActivityOptions
	executor, isAgent := call.Subject.Executor.AsAgent()
	if !isAgent {
		return agentChildRequest{}, nil, errors.New("child preparation requires an Agent parent subject")
	}
	preparer := agent.Ident(executor)
	if call.Registry != nil {
		declaration, err := registryAgentDeclaration(call)
		if err != nil {
			return agentChildRequest{}, nil, err
		}
		options.Queue = declaration.ConsumerContract.Agent.Queue
		preparer = agent.Ident(declaration.ConsumerContract.Agent.Executor)
	}
	output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name:    agentChildPreparationActivityName(preparer),
		Options: options,
		Input: &api.AgentChildActivityInput{
			Origin:       origin,
			Call:         call,
			HistoryEndID: historyEndID,
			ParentRun:    parentRun,
		},
	})
	if err != nil {
		return agentChildRequest{}, nil, err
	}
	if output == nil {
		return agentChildRequest{}, nil, errors.New("agent child activity returned nil output")
	}
	switch {
	case output.Stopped != nil && output.Success == nil && output.Failure == nil:
		if output.Stopped.RunID != agentChildRunContext(&call).RunID {
			return agentChildRequest{}, nil, errors.New("child preparation stop belongs to another run")
		}
		return agentChildRequest{}, output.Stopped, nil
	case output.Success != nil && output.Failure == nil && output.Stopped == nil:
		nested := agentChildRunContext(&call)
		nested.Labels = mergeLabels(nested.Labels, output.Success.Labels)
		nested.Metadata = cloneMetadata(output.Success.Metadata)
		return agentChildRequest{
			policy:     clonePolicyOverrides(output.Success.Policy),
			seedEndID:  output.Success.SeedEndID,
			runContext: nested,
		}, nil, nil
	case output.Success == nil && output.Failure != nil && output.Stopped == nil:
		return agentChildRequest{}, nil, &agentChildRequestFailure{failure: output.Failure}
	default:
		return agentChildRequest{}, nil, errors.New("agent child activity must return exactly one of success, failure or stopped")
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

// agentChildPreparationActivityName gives each Agent a stable preparation
// activity name. The engine registers that name on the Agent's declared queue,
// so static parents and registry providers can prepare on separate workers.
func agentChildPreparationActivityName(executor agent.Ident) string {
	return agentChildActivityName + "." + string(executor)
}
