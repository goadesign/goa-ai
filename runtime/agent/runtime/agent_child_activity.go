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
	"github.com/google/uuid"

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

	if input.Call.RunID != input.ParentRun.RunID || input.Call.SessionID != input.ParentRun.SessionID {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("child call does not belong to the parent run"))
	}
	cfg, err := r.selectedAgentToolConfig(input.Call)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	commandBytes, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(commandBytes)
	operation := storage.PreparationOperation{AgentID: string(cfg.Definition.route.ID), RunID: agentChildRunContext(&input.Call).RunID, SessionID: input.ParentRun.SessionID, CommandID: hex.EncodeToString(digest[:])}
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if found {
		return recoverChildPreparation(ctx, r.Store, accepted)
	}
	messages, err := r.loadActivityHistory(ctx, input.Call.AgentID, input.ParentRun.RunID, input.ParentRun.SessionID, input.HistoryEndID)
	if err != nil {
		return nil, err
	}
	var request agentChildRequest
	if input.Call.Registry != nil {
		var config *AgentToolConfiguration
		config, err = r.prepareRegistryAgentChild(ctx, input.Call)
		if err == nil {
			request = agentChildRequest{
				messages: config.Messages, renderedPrompts: config.RenderedPrompts,
				policy: config.Policy, runContext: agentChildRunContext(&input.Call),
			}
			request.runContext.Labels = config.Labels
		}
	} else {
		request, err = r.buildAgentChildRequest(ctx, cfg, &input.Call, messages, &input.ParentRun)
	}
	if err != nil {
		if failure := buildToolFailureFromAgentToolRequestError(err); failure != nil {
			return &api.AgentChildActivityOutput{Failure: failure}, nil
		}
		return nil, err
	}
	if _, err := agentChildRunInput(cfg.Definition, request); err != nil {
		return nil, err
	}
	writer, err := stageLiteralHistory(ctx, r.Store, storage.SeedDeclaration{
		AgentID: operation.AgentID, RunID: operation.RunID, SessionID: operation.SessionID,
		CommandID: operation.CommandID, AttemptID: uuid.NewString(), Kind: storage.SeedLiteral, RenderedPrompts: request.renderedPrompts,
	}, request.messages)
	if err != nil {
		return r.recoverChildPublication(ctx, operation, err)
	}
	success := &api.AgentChildActivitySuccess{SeedEndID: writer.endID, Labels: request.runContext.Labels, Policy: request.policy}
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
func (r *Runtime) prepareAgentChild(wfCtx engine.WorkflowContext, call ToolCall, historyEndID string, parentRun run.Context) (agentChildRequest, error) {
	output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name: agentChildActivityName,
		Input: &api.AgentChildActivityInput{
			Call:         call,
			HistoryEndID: historyEndID,
			ParentRun:    parentRun,
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
		nested := agentChildRunContext(&call)
		nested.Labels = mergeLabels(nested.Labels, output.Success.Labels)
		return agentChildRequest{
			policy:     clonePolicyOverrides(output.Success.Policy),
			seedEndID:  output.Success.SeedEndID,
			runContext: nested,
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
