// Package runtime repairs executions that the engine closes before workflow
// code delivers completion. Start and repair use the same private request
// builder; repair does not prepare provider data or start another workflow.
package runtime

import (
	"context"
	"fmt"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

// EnsureOperationCompletion delivers authoritative completion for the exact
// sessionful operation originally passed to StartOperation. The host retains
// the original declaration, arguments, labels and memo until completion. An
// open engine returns ErrRunCompletionNotReady; unavailable history is an error.
func (r *Runtime) EnsureOperationCompletion(ctx context.Context, sessionID, runID string, operation *genregistry.ToolSchema, arguments rawjson.Message, labels map[string]string, memo map[string]any) error {
	if sessionID == "" {
		return ErrMissingSessionID
	}
	request, err := prepareOperationRequest(sessionID, runID, operation, arguments, labels, memo)
	if err != nil {
		return err
	}
	return r.ensureOperationCompletion(ctx, request)
}

// EnsureOneShotOperationCompletion delivers authoritative completion for the
// exact sessionless operation originally passed to StartOneShotOperation. It
// creates no Session and never resubmits a workflow whose history is missing.
func (r *Runtime) EnsureOneShotOperationCompletion(ctx context.Context, runID string, operation *genregistry.ToolSchema, arguments rawjson.Message, labels map[string]string, memo map[string]any) error {
	request, err := prepareOperationRequest("", runID, operation, arguments, labels, memo)
	if err != nil {
		return err
	}
	return r.ensureOperationCompletion(ctx, request)
}

// ensureOperationCompletion proves the retained request against the engine's
// actual accepted input before an engine result can mutate runtime storage.
func (r *Runtime) ensureOperationCompletion(ctx context.Context, request engine.WorkflowStartRequest) error {
	expected, err := startrecipe.SnapshotRequest(request)
	if err != nil {
		return err
	}
	completion, err := r.Engine.QueryRunCompletion(ctx, request.ID)
	if err != nil {
		return fmt.Errorf("query operation completion: %w", err)
	}
	if err := validateOperationCompletion(expected, completion); err != nil {
		return err
	}
	return r.completeObservedOperation(ctx, expected, completion)
}

// validateOperationCompletion checks one observed engine result against the
// original accepted request. Open work remains pending and mismatched input
// cannot authorize a runtime storage change.
func validateOperationCompletion(expected startrecipe.RequestSnapshot, completion engine.RunCompletion) error {
	if expected.Digest != completion.StartRequestDigest {
		return engine.ErrWorkflowStartConflict
	}
	if !isTerminalEngineRunStatus(completion.Status) {
		return ErrRunCompletionNotReady
	}
	return validateEngineCompletion(completion, expected.Request.Input)
}

// completeObservedOperation stores a validated failure or cancellation without
// querying again. Successful work uses the ordinary stored-run completion path.
func (r *Runtime) completeObservedOperation(ctx context.Context, expected startrecipe.RequestSnapshot, completion engine.RunCompletion) error {
	if completion.Status == engine.RunStatusCompleted {
		return r.EnsureRunCompletion(ctx, expected.Request.ID)
	}
	input := expected.Request.Input
	command := &api.UnstartedTerminalCommand{
		RunID: input.RunID, Subject: input.Subject, SessionID: input.SessionID,
		RequestDigest: completion.StartRequestDigest[:],
		Time:          genrunterminal.NewExecutionTerminalTimeEngineClosure(genrunterminal.ExecutionTerminalTimeBranchEngineClosure(completion.CompletedAt.UTC().Format(time.RFC3339Nano))),
	}
	if input.SeedEndID != "" {
		command.PublishedSeed = &api.SeedPositionResult{EndID: input.SeedEndID}
	} else {
		command.Root = &struct{}{}
	}
	if completion.Status == engine.RunStatusCanceled {
		command.Outcome = genrunterminal.NewUnstartedExecutionOutcomeCanceled(run.CancellationReasonEngineCanceled)
	} else {
		failure := &genrunterminal.ExecutionFailure{}
		failure.CreateFromFailure(hooks.RunFailureFromError(completion.WorkflowError))
		command.Outcome = genrunterminal.NewUnstartedExecutionOutcomeFailed(failure)
	}
	_, err := r.executeStorageCommand(ctx, &api.StorageActivityCommand{UnstartedTerminal: command})
	return err
}
