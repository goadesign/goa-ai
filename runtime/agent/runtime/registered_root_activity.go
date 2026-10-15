// Package runtime loads the root workflow's original saved request in
// registered service activities. Provider calls require the exact selected
// registration and use shared transport for overload recovery and typed results.
package runtime

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/internal/registrycall"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/toolregistry"
)

// executeRegisteredRootActivity checks the saved identity and original input
// before sending the operation to its selected provider registration.
func (r *Runtime) executeRegisteredRootActivity(ctx context.Context, input *api.ToolInput) (activityOutput *api.ToolOutput, activityErr error) {
	ctx, span := r.tracer.Start(ctx, "runtime.execute_registered_root")
	defer func() {
		if activityErr != nil {
			span.RecordError(activityErr)
			span.SetStatus(codes.Error, activityErr.Error())
		}
		span.End()
	}()
	if input == nil {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("registered service activity input is required"))
	}
	span.SetAttributes(attribute.String("run.id", input.RunID), attribute.String("tool.name", input.ToolName.String()))
	stop := startActivityHeartbeat(ctx)
	defer stop()
	root, ok := input.Invocation.Invocation.AsRoot()
	if !ok {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("registered service activity requires root invocation"))
	}
	seed, err := r.Store.LoadRunSeed(ctx, root.RunID, storage.EmptySeedEndID)
	if err != nil {
		return nil, err
	}
	declaration := seed.Declaration
	prepared, found, err := r.Store.FindRunPreparation(ctx, storage.PreparationOperation{Subject: declaration.Subject, RunID: declaration.RunID, SessionID: declaration.SessionID, CommandID: declaration.CommandID})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("registered root has no published request")
	}
	accepted, err := recoverOperationPreparation(ctx, r.Store, prepared)
	if err != nil {
		return nil, err
	}
	resolution, err := validateRegisteredRootInput(accepted)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	request := accepted.RegisteredInvocation
	subject, _ := accepted.Subject.Executor.AsServiceTool()
	if input.RunID != root.RunID || input.ToolName.String() != subject.Tool || input.SessionID != accepted.SessionID || !reflect.DeepEqual(input.Subject, accepted.Subject) || !reflect.DeepEqual(input.Invocation, *request.Invocation) || !reflect.DeepEqual(input.Registry, accepted.ToolRegistry) || !reflect.DeepEqual(input.Labels, accepted.Labels) || !bytes.Equal(input.OriginalArguments, request.Arguments) || !bytes.Equal(input.Payload, request.Arguments) {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("registered activity changed original execution identity or input"))
	}
	if err := toolregistry.ValidateExecution(input.ExecutionSequence, input.ExecutionContinuation, input.TextOnly); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	connection, err := r.registryConnection(subject.Registry)
	if err != nil {
		return nil, err
	}
	executor, err := registrycall.New(registrycall.NewResolvedClient(connection.client, resolution.Registered.RegistrationToken, subject.Tool), connection.pulse, resolution.Registered.Toolset.Name, registryExecutionSpecs(resolution.Specs), r.registryCallOptions...)
	if err != nil {
		return nil, err
	}
	call := &ToolCall{Name: input.ToolName, Payload: input.Payload, ModelPayload: input.OriginalArguments, Registry: input.Registry, RunID: root.RunID, Subject: input.Subject, SessionID: input.SessionID, ToolCallID: input.ToolCallID, Labels: input.Labels, ExecutionSequence: input.ExecutionSequence, ExecutionContinuation: input.ExecutionContinuation}
	meta := &toolregistry.ToolCallMeta{RunID: root.RunID, SessionID: input.SessionID, ToolCallID: input.ToolCallID, Labels: cloneLabels(input.Labels)}
	output, err := executor.Execute(ctx, meta, call)
	if err != nil {
		return nil, err
	}
	if output.PendingExecution != nil {
		return output, nil
	}
	// The shared decoder validates final result and server-only data against the
	// retained declaration before the workflow observes a final tool result.
	decoded := executor.DecodeCompletedResult(call, meta, output)
	event, err := encodeToolEvent(decoded, *call, r.callSpecLookup(*call))
	if err != nil {
		return nil, err
	}
	return &api.ToolOutput{Payload: event.Result, ServerData: event.ServerData, Blocks: event.Blocks, Bounds: event.Bounds, Failure: event.Failure, Telemetry: event.Telemetry}, nil
}
