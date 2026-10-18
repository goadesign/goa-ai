// Package runtime stores failures and cancellations decided before workflow
// code observes a successful Run start. The storage activity loads the original
// invocation from the exact published seed and preserves earlier terminal results.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

type terminalFailure struct {
	failure *run.Failure
}

// Error returns the caller-safe summary retained in the canonical failure.
func (e terminalFailure) Error() string {
	return e.failure.Message
}

// RunFailure lets the existing hook encoder retain the exact failure fields.
func (e terminalFailure) RunFailure() *run.Failure {
	return e.failure
}

// persistUnstartedExecution freezes the workflow's failure or cancellation and
// decision time before retrying storage. A detached context keeps that activity
// running after cancellation, so workflow completion cannot discard the outcome.
func (r *Runtime) persistUnstartedExecution(wfCtx engine.WorkflowContext, command *api.UnstartedTerminalCommand, workflowErr error, cancellationReason string) error {
	command.Time = genrunterminal.NewExecutionTerminalTimeWorkflowDecision(genrunterminal.ExecutionTerminalTimeBranchWorkflowDecision(wfCtx.Now().UTC().Format(time.RFC3339Nano)))
	if terminalRunStatusForError(workflowErr) == runStatusCanceled {
		command.Outcome = genrunterminal.NewUnstartedExecutionOutcomeCanceled(genrunterminal.UnstartedExecutionOutcomeBranchCanceled(cancellationReason))
	} else {
		failure := &genrunterminal.ExecutionFailure{}
		failure.CreateFromFailure(hooks.RunFailureFromError(workflowErr))
		command.Outcome = genrunterminal.NewUnstartedExecutionOutcomeFailed(failure)
	}
	_, err := r.executeStorageWithRetry(wfCtx.Detached().Context(), &api.StorageActivityCommand{UnstartedTerminal: command})
	return err
}

// storeUnstartedTerminal resolves the original invocation outside workflow code.
// A missing dependency returns an activity error, so the workflow retains the
// obligation until the same command can be stored or a prior result is delivered.
func (r *Runtime) storeUnstartedTerminal(ctx context.Context, input *api.UnstartedTerminalCommand) (*api.StorageActivityResult, error) {
	if (input.Root == nil) == (input.PublishedSeed == nil) {
		return nil, malformedStorageCommand(errors.New("early terminal requires exactly one original invocation source"))
	}
	command := genrunterminal.UnstartedExecutionTerminal{
		RunID:         input.RunID,
		Subject:       &input.Subject,
		RequestDigest: input.RequestDigest,
		Time:          input.Time,
		Outcome:       input.Outcome,
	}
	if input.SessionID != "" {
		command.SessionID = &input.SessionID
	}
	if input.Root != nil {
		command.Origin = &genregistry.ExecutionInvocation{
			Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: input.RunID}),
		}
	} else {
		if input.PublishedSeed.EndID == "" {
			return nil, malformedStorageCommand(errors.New("early terminal requires an exact published seed position"))
		}
		seed, err := r.Store.LoadRunSeed(ctx, input.RunID, input.PublishedSeed.EndID)
		if err != nil {
			return nil, fmt.Errorf("load early terminal original invocation: %w", err)
		}
		if seed.Declaration.RunID != input.RunID || seed.Declaration.SessionID != input.SessionID || !reflect.DeepEqual(seed.Declaration.Subject, input.Subject) {
			return nil, malformedStorageCommand(errors.New("early terminal seed belongs to another execution"))
		}
		command.Origin = &seed.Origin
	}
	result, err := r.Store.RecordUnstartedExecutionTerminal(ctx, command)
	if err != nil {
		return nil, err
	}
	if _, started := result.Result.AsStarted(); started {
		if err := r.completeStartedExecution(ctx, command); err != nil {
			return nil, err
		}
	}
	return &api.StorageActivityResult{UnstartedTerminal: &result}, nil
}

// completeStartedExecution handles a start that committed before its response
// was lost. The store has verified the accepted digest and original invocation;
// an existing terminal result wins, otherwise this exact decision completes it.
func (r *Runtime) completeStartedExecution(ctx context.Context, command genrunterminal.UnstartedExecutionTerminal) error {
	meta, err := r.Store.LoadRun(ctx, command.RunID)
	if err != nil {
		return err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		return r.ensureStoredRunCompletion(ctx, meta)
	}
	turnID, err := r.loadRunTurnID(ctx, meta)
	if err != nil {
		return err
	}
	var (
		failure      error
		cancellation *run.Cancellation
	)
	status := runStatusFailed
	if value, failed := command.Outcome.AsFailed(); failed {
		failure = terminalFailure{failure: value.ConvertToFailure()}
	} else if reason, canceled := command.Outcome.AsCanceled(); canceled {
		status = runStatusCanceled
		cancellation = &run.Cancellation{Reason: string(reason)}
		// A previously accepted cancellation keeps its original reason even
		// when external engine closure reports only a generic cancellation.
		if meta.CancellationReason != "" {
			cancellation.Reason = meta.CancellationReason
		}
	} else {
		return malformedStorageCommand(errors.New("early terminal outcome is missing"))
	}
	var timestamp string
	if value, selected := command.Time.AsWorkflowDecision(); selected {
		timestamp = string(value)
	} else if value, selected := command.Time.AsEngineClosure(); selected {
		timestamp = string(value)
	} else {
		return malformedStorageCommand(errors.New("early terminal time source is missing"))
	}
	decidedAt, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return malformedStorageCommand(err)
	}
	event := hooks.NewRunCompletedEvent(meta.RunID, meta.Subject, meta.SessionID, status, terminalRunPhaseForStatus(status), meta.Labels, failure, cancellation)
	record, err := prepareHookRecordInputWithMetadata(event, turnID, recordDispatchMetadata{TimestampMS: decidedAt.UnixMilli()})
	if err != nil {
		return malformedStorageCommand(err)
	}
	terminal, storedEvent, err := runTerminalStorageCommand(&api.RunTerminalCommand{Record: record}, meta)
	if err != nil {
		return malformedStorageCommand(err)
	}
	result, err := r.repairRunTerminalUntilApplied(ctx, terminal)
	if err != nil {
		return err
	}
	return r.finalizeEnsuredRunCompletion(ctx, meta, storedEvent, result)
}
