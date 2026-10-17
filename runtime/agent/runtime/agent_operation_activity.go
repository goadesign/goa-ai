// Package runtime prepares accepted direct operations on their provider worker.
// Preparation stores the exact messages through the runtime store; a lost reply
// recovers that same prepared input instead of rereading mutable configuration.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
)

// agentOperationActivityName selects the preparer registered for one provider
// Agent. Other Agents on the same queue cannot consume the wrong preparation.
func agentOperationActivityName(executor agent.Ident) string {
	return "runtime.prepare_agent_operation." + string(executor)
}

// prepareAgentOperationActivity validates the retained operation before reading
// its configuration. It publishes prepared messages and control values together.
func (r *Runtime) prepareAgentOperationActivity(ctx context.Context, input *api.RunInput) (*api.AgentOperationActivityResult, error) {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	if input == nil {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("operation input is required"))
	}
	if input.SeedEndID != "" || input.Policy != nil {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("direct operation preparation cannot include caller-prepared history or policy"))
	}
	if err := validateDirectOperationInput(input); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	executor, _ := input.Subject.Executor.AsAgent()
	registration, exists := r.agentByID(agent.Ident(executor))
	if !exists {
		return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("agent provider %q is not registered", executor))
	}
	declaration := input.Operation
	if input.RegisteredInvocation != nil {
		var err error
		declaration, err = registeredAgentDeclaration(input)
		if err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(err)
		}
	}
	target := declaration.ConsumerContract.Agent
	selected, err := providerOperationSpec(registration.Definition, declaration)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	command, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(command)
	operation := storage.PreparationOperation{Subject: input.Subject, RunID: input.RunID, SessionID: input.SessionID, CommandID: hex.EncodeToString(digest[:])}
	origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: input.RunID})}
	decision, err := r.Store.BeginRunPreparationOperation(ctx, storage.RunPreparationBegin{Operation: operation, Origin: origin})
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if reason, stopped := decision.State.AsStop(); stopped {
		return &api.AgentOperationActivityResult{Stopped: &api.CancellationRequest{RunID: operation.RunID, Reason: string(reason)}}, nil
	}
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if found {
		prepared, err := recoverOperationPreparation(ctx, r.Store, accepted)
		if err != nil {
			return nil, err
		}
		return &api.AgentOperationActivityResult{Prepared: prepared}, nil
	}
	arguments := input.ToolArgs
	if input.RegisteredInvocation != nil {
		arguments = input.RegisteredInvocation.Arguments
	}
	// The provider receives a root call with no parent or tool-call identity.
	// Its existing resolver owns prompt rendering, labels and planner policy.
	call := ToolCall{
		Name: selected.Name, Payload: append(rawjson.Message(nil), arguments...),
		RunID: input.RunID, Subject: input.Subject, SessionID: input.SessionID,
		TurnID: input.TurnID, Labels: cloneLabels(input.Labels),
	}
	config, err := r.resolveAgentConfiguration(ctx, origin, registration.Definition, target.Configuration, &call)
	if err != nil {
		return nil, err
	}
	prepared := *input
	if input.RegisteredInvocation != nil {
		prepared.Tool = selected.Name
		prepared.ToolArgs = append(rawjson.Message(nil), arguments...)
	}
	prepared.Metadata = config.Metadata
	prepared.Labels = config.Labels
	prepared.Policy = config.Policy
	if err := r.validateCompletionToolPolicy(registration, prepared.Policy); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	if prepared.Policy != nil {
		if err := r.validateLimitTerminalPlans(registration, prepared.Policy.LimitTerminalPlans); err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(err)
		}
	}
	writer, err := stageAgentHistory(ctx, r.Store, storage.SeedDeclaration{
		Subject: operation.Subject, RunID: operation.RunID, SessionID: operation.SessionID,
		CommandID: operation.CommandID, AttemptID: uuid.NewString(),
		RenderedPrompts: config.RenderedPrompts,
	}, config.History)
	if err != nil {
		recovered, recoverErr := r.recoverOperationPublication(ctx, operation, err)
		if recoverErr != nil {
			return nil, recoverErr
		}
		return &api.AgentOperationActivityResult{Prepared: recovered}, nil
	}
	prepared.SeedEndID = writer.endID
	data, err := json.Marshal(&prepared)
	if err != nil {
		return nil, err
	}
	if err := writer.publish(ctx, data); err != nil {
		recovered, recoverErr := r.recoverOperationPublication(ctx, operation, err)
		if recoverErr != nil {
			return nil, recoverErr
		}
		return &api.AgentOperationActivityResult{Prepared: recovered}, nil
	}
	return &api.AgentOperationActivityResult{Prepared: &prepared}, nil
}

// recoverOperationPreparation reads the original complete input after a lost
// reply and verifies that it points at the messages accepted with that input.
func recoverOperationPreparation(ctx context.Context, store storage.Store, accepted storage.RunPreparation) (*api.RunInput, error) {
	data, err := readPreparation(ctx, store, accepted)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	var input api.RunInput
	if err := workflowcodec.DecodeJSON(data, &input); err != nil {
		return nil, classifyStorageActivityError(storage.NewContractError(err))
	}
	if !reflect.DeepEqual(input.Subject, accepted.Seed.Declaration.Subject) ||
		input.RunID != accepted.Seed.Declaration.RunID || input.SessionID != accepted.Seed.Declaration.SessionID ||
		input.SeedEndID != accepted.Seed.EndID {
		return nil, classifyStorageActivityError(storage.NewContractError(storage.ErrSeedConflict))
	}
	return &input, nil
}

// prepareAgentOperation records provider preparation and checks the returned
// immutable request facts before entering the common execution loop.
func (r *Runtime) prepareAgentOperation(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunInput, *api.CancellationRequest, error) {
	if err := validateDirectOperationInput(input); err != nil {
		return nil, nil, err
	}
	executor, _ := input.Subject.Executor.AsAgent()
	declaration := input.Operation
	if input.RegisteredInvocation != nil {
		var err error
		declaration, err = registeredAgentDeclaration(input)
		if err != nil {
			return nil, nil, err
		}
	}
	target := declaration.ConsumerContract.Agent
	result, err := wfCtx.ExecuteAgentOperationActivity(engine.AgentOperationActivityCall{
		Name: agentOperationActivityName(agent.Ident(executor)), Input: input,
		Options: engine.ActivityOptions{Queue: target.Queue},
	})
	if err != nil {
		return nil, nil, err
	}
	if result.Stopped != nil {
		if result.Stopped.RunID != input.RunID {
			return nil, nil, errors.New("operation preparation stop belongs to another run")
		}
		return nil, result.Stopped, nil
	}
	prepared := result.Prepared
	if prepared == nil || prepared.SeedEndID == "" {
		return nil, nil, errors.New("operation preparation returned no published history")
	}
	// Preparation may add only history, provider labels, metadata and policy.
	// The accepted operation, input and execution relationships cannot change.
	restored := *prepared
	if input.RegisteredInvocation != nil {
		target, _ := input.RegisteredInvocation.Target.Target.AsNative()
		if prepared.Tool.String() != target.Tool || !bytes.Equal(prepared.ToolArgs, input.RegisteredInvocation.Arguments) {
			return nil, nil, errors.New("native preparation changed original operation")
		}
		restored.Tool = input.Tool
		restored.ToolArgs = input.ToolArgs
	}
	restored.SeedEndID = input.SeedEndID
	restored.Metadata = input.Metadata
	restored.Labels = input.Labels
	restored.Policy = input.Policy
	if !reflect.DeepEqual(input, &restored) {
		return nil, nil, errors.New("operation preparation changed the accepted request")
	}
	return prepared, nil, nil
}

// recoverOperationPublication returns an accepted winner after a write loses
// its reply. Without a winner, the original storage failure remains observable.
func (r *Runtime) recoverOperationPublication(ctx context.Context, operation storage.PreparationOperation, cause error) (*api.RunInput, error) {
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if found {
		return recoverOperationPreparation(ctx, r.Store, accepted)
	}
	return nil, classifyStorageActivityError(cause)
}
