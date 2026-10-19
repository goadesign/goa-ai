// Package runtime runs service-tool workflows that own start, execution,
// suspension for input, and terminal storage. These workflows use the common
// lifecycle records without running a model planner.
package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/contract"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RegisterToolWorkflow installs the service-tool workflow and shared storage and
// cancellation activities on the configured worker. Submit-only runtimes omit it.
func (r *Runtime) RegisterToolWorkflow(ctx context.Context) error {
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()
	if r.registrationClosed.Load() {
		return ErrRegistrationClosed
	}
	if r.Engine == nil {
		return ErrEngineNotConfigured
	}
	if r.toolWorkflowQueue == "" {
		return errors.New("service-tool workflow queue is required")
	}
	if err := r.ensureStorageActivityRegistered(ctx); err != nil {
		return err
	}
	options := engine.ActivityOptions{Queue: r.toolWorkflowQueue, StartToCloseTimeout: defaultExecuteToolActivityTimeout, RetryPolicy: defaultRetriedActivityPolicy()}
	if err := r.Engine.RegisterExecuteToolActivity(ctx, registeredRootActivity, options, r.executeRegisteredRootActivity); err != nil {
		return err
	}
	cancelOptions := engine.ActivityOptions{Queue: r.toolWorkflowQueue, StartToCloseTimeout: defaultStorageActivityTimeout, RetryPolicy: defaultRetriedActivityPolicy()}
	cancelOptions.RetryPolicy.MaxAttempts = 0
	cancelOptions.RetryPolicy.UnlimitedAttempts = true
	if err := r.Engine.RegisterCancellationWorkflow(ctx, registeredRootWorkflow+".cancel", cancelOptions, r.cancellationActivity); err != nil {
		return err
	}
	return r.Engine.RegisterWorkflow(ctx, engine.WorkflowDefinition{Name: registeredRootWorkflow, TaskQueue: r.toolWorkflowQueue, Handler: r.executeRegisteredRootWorkflow})
}

// executeRegisteredRootWorkflow records the engine-accepted request before its
// first provider call. Detached finalization settles storage despite cancellation.
func (r *Runtime) executeRegisteredRootWorkflow(wf engine.WorkflowContext, submitted *api.RunInput) (output *api.RunOutput, workflowErr error) {
	if err := wf.RequireSingleAttempt(); err != nil {
		return nil, err
	}
	digest, err := wf.StartRequestDigest()
	if err != nil {
		return nil, err
	}
	input, state, err := restoreRegisteredRoot(submitted)
	if err != nil {
		return nil, err
	}
	early := &api.UnstartedTerminalCommand{RunID: input.RunID, Subject: input.Subject, SessionID: input.SessionID, RequestDigest: digest[:], Root: &struct{}{}}
	if submitted.Continuation != nil {
		early.Root = nil
		early.PublishedSeed = &api.SeedPositionResult{EndID: submitted.SeedEndID}
	} else {
		state.queue = r.toolWorkflowQueue
	}
	finalization := &workflowFinalizationState{}
	var startCommand *api.StorageActivityCommand
	started := false
	cancellationRecorded := false
	cancelReason := run.CancellationReasonEngineCanceled
	defer func() {
		canceled, closeErr := finalization.beginFinalization(wf.Detached())
		if closeErr != nil {
			workflowErr = errors.Join(workflowErr, closeErr)
			return
		}
		defer finalization.finishFinalization()
		if canceled {
			workflowErr = context.Canceled
			output = nil
		}
		// A resumed service cancellation waits for any engine callback already
		// running, then keeps the cancellation record if that callback wrote it.
		if started && !canceled && !cancellationRecorded && submitted.Continuation != nil && submitted.Continuation.Cancellation != nil {
			workflowErr = errors.Join(workflowErr, r.publishRunCancellation(wf, input, *submitted.Continuation.Cancellation))
		}
		if workflowErr != nil && state.pending != nil {
			workflowErr = errors.Join(workflowErr, r.cancelRegisteredTask(wf.Detached(), state))
		}
		if !started {
			if workflowErr != nil && !errors.Is(workflowErr, engine.ErrWorkflowCompleted) {
				workflowErr = errors.Join(workflowErr, r.persistUnstartedExecution(wf, early, workflowErr, cancelReason))
			}
			return
		}
		if workflowErr == nil {
			output, workflowErr = contract.CopyRunOutput(output)
		}
		if workflowErr == nil && output.Suspension == nil && output.FinalToolResult == nil {
			workflowErr = errors.New("selected operation completed without its result")
		}
		if workflowErr == nil && output.Suspension != nil {
			workflowErr = r.persistRunSuspension(wf.Detached(), input, output.Suspension)
			if workflowErr == nil {
				return
			}
			output = nil
		}
		status := runStatusSuccess
		if workflowErr != nil {
			status = terminalRunStatusForError(workflowErr)
		}
		terminalCtx := wf.Detached().Context()
		command, commandErr := buildRunTerminalCommand(input, "", status, workflowErr, finalization.operationResult, recordDispatchMetadataForContext(terminalCtx))
		if commandErr != nil {
			workflowErr = errors.Join(workflowErr, commandErr)
			return
		}
		_, terminalErr := r.executeStorage(terminalCtx, &api.StorageActivityCommand{Terminal: command}, engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}})
		workflowErr = errors.Join(workflowErr, terminalErr)
	}()
	if err := wf.SetCancellationHandler(func(cancelCtx engine.WorkflowContext, request engine.CancellationRequest) error {
		if request.RunID != input.RunID {
			return engine.ErrCancellationRunNotOwned
		}
		if startCommand == nil {
			if err := finalization.beginCancellation(); err != nil {
				return err
			}
			cancelReason = request.Reason
			finalization.finishCancellation(true)
			return nil
		}
		if err := r.handleWorkflowCancellation(finalization, cancelCtx, input, startCommand, request); err != nil {
			return err
		}
		cancelReason = request.Reason
		return nil
	}); err != nil {
		return nil, err
	}
	// A literal publication with zero messages records ownership and exact saved
	// input. No conversational record or Agent identity is manufactured.
	if submitted.Continuation == nil {
		commandID := hex.EncodeToString(digest[:])
		declaration := storage.SeedDeclaration{Subject: input.Subject, RunID: input.RunID, SessionID: input.SessionID, CommandID: commandID, AttemptID: commandID, Kind: storage.SeedLiteral, PredecessorRunID: state.predecessor}
		decision, err := bindHistoryOperation(wf.Context(), workflowSeedWriter{r: r}, declaration, *input.RegisteredInvocation.Invocation)
		if err != nil {
			return nil, err
		}
		if reason, stopped := decision.State.AsStop(); stopped {
			cancelReason = string(reason)
			return nil, context.Canceled
		}
		writer, err := stageLiteralHistory(wf.Context(), workflowSeedWriter{r: r}, declaration, nil)
		if err != nil {
			return nil, err
		}
		input.SeedEndID = writer.endID
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		if err := writer.publish(wf.Context(), encoded); err != nil {
			return nil, err
		}
		early.Root = nil
		early.PublishedSeed = &api.SeedPositionResult{EndID: input.SeedEndID}
	}
	records, err := prepareRunStartRecords(wf.Context(), []hooks.Event{hooks.NewRunStartedEvent(input.RunID, input.Subject, input.SessionID, "", state.predecessor, input.Labels)}, "")
	if err != nil {
		return nil, err
	}
	startCommand, err = runStartStorageCommand(input, digest, records)
	if err != nil {
		return nil, err
	}
	result, err := r.executeStorageWithRetry(wf.Detached().Context(), startCommand)
	if err != nil {
		return nil, err
	}
	start := runStartStorageResult(input, result)
	if session.IsTerminalRunStatus(start.RunStatus) {
		return nil, engine.ErrWorkflowCompleted
	}
	started = true
	if start.Outcome == session.RunStartStop {
		cancellationRecorded = true
		cancelReason = start.CancellationReason
		return nil, context.Canceled
	}
	if submitted.Continuation != nil && submitted.Continuation.Cancellation != nil {
		cancelReason = submitted.Continuation.Cancellation.Reason
		return nil, context.Canceled
	}
	if state.predecessor == "" {
		scheduled := newToolCallScheduledEvent(input.RunID, input.Subject, input.SessionID, ToolCall{TextOnly: state.call.TextOnly, Name: state.call.ToolName, ToolCallID: state.call.ToolCallID, Payload: state.call.Payload, Registry: state.call.Registry}, r.toolWorkflowQueue, "", 0)
		if err := r.publishHook(wf.Context(), scheduled, ""); err != nil {
			return nil, err
		}
	}
	if state.predecessor == "" {
		call := ToolCall{TextOnly: state.call.TextOnly, Name: state.call.ToolName, ToolCallID: state.call.ToolCallID, Payload: state.call.Payload, Registry: state.call.Registry}
		plan, required, err := r.confirmationPlan(wf.Context(), &call)
		if err != nil {
			return nil, err
		}
		if required {
			spec, _, err := r.callSpec(call)
			if err != nil {
				return nil, err
			}
			denied, err := EncodeCanonicalToolResult(spec, plan.DeniedResult, nil)
			if err != nil {
				return nil, err
			}
			state.denied = denied
			state.confirmation = &api.PendingConfirmation{ID: state.call.ToolCallID + "/confirmation", Title: plan.Title, Prompt: plan.Prompt, ToolName: state.call.ToolName, ToolCallID: state.call.ToolCallID, Payload: state.call.Payload}
		}
	}
	if state.decision != nil {
		event := hooks.NewToolAuthorizationEvent(input.RunID, input.Subject, input.SessionID, state.call.ToolName, state.call.ToolCallID, state.decision.Approved, "", state.decision.RequestedBy)
		if err := r.publishHook(wf.Context(), event, ""); err != nil {
			return nil, err
		}
	}
	return r.runRegisteredRoot(wf, input, state, finalization)
}

// runRegisteredRoot advances only this invocation. A pending provider Task is
// polled with its existing guidance; an input request ends the workflow durably.
func (r *Runtime) runRegisteredRoot(wf engine.WorkflowContext, input *api.RunInput, state *registeredRootState, finalization *workflowFinalizationState) (*api.RunOutput, error) {
	if state.confirmation != nil {
		suspension, err := registeredRootSuspension(input, state)
		if err != nil {
			return nil, err
		}
		return &api.RunOutput{Subject: input.Subject, RunID: input.RunID, Suspension: suspension}, nil
	}
	for {
		var result *api.ToolOutput
		var err error
		if state.decision != nil && !state.decision.Approved {
			result = &api.ToolOutput{Payload: state.denied}
		} else {
			result, err = wf.ExecuteToolActivity(engine.ToolActivityCall{Name: registeredRootActivity, Input: &state.call, Options: engine.ActivityOptions{Queue: state.queue}})
		}
		if err != nil {
			return nil, err
		}
		if state.call.TextOnly {
			if err := validateTextOnlyOperationOutput(result); err != nil {
				return nil, err
			}
		}
		if result.PendingExecution == nil {
			state.pending = nil
			event := &api.ToolEvent{Name: state.call.ToolName, ToolCallID: state.call.ToolCallID, Result: result.Payload, ServerData: result.ServerData, Blocks: result.Blocks, Bounds: result.Bounds, Failure: result.Failure, Telemetry: result.Telemetry}
			finalization.operationResult, err = operationResultFromToolEvent(event)
			if err != nil {
				return nil, err
			}
			record := hooks.NewToolResultReceivedEvent(input.RunID, input.Subject, input.SessionID, state.call.RunID, state.call.ToolName, state.call.ToolCallID, "", result.Payload, result.ServerData, result.Blocks, "", result.Bounds, 0, result.Telemetry, result.Failure)
			if err := r.publishHookWithOptions(wf.Detached().Context(), record, "", engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}}); err != nil {
				return nil, err
			}
			output := &api.RunOutput{Subject: input.Subject, RunID: input.RunID, FinalToolResult: event, ToolCount: 1, ToolTelemetry: result.Telemetry}
			if result.Failure != nil {
				return output, &selectedOperationError{cause: result.Failure.Error}
			}
			return output, nil
		}
		if err := validatePendingActivityOutput(result); err != nil {
			return nil, err
		}
		observed := futureInfo{call: ToolCall{ExecutionContinuation: state.call.ExecutionContinuation}, task: state.task}
		pending, _, err := acceptTaskPending(&observed, result.PendingExecution)
		if err != nil {
			return nil, err
		}
		state.pending = pending
		state.task = observed.task
		if id, hint, waiting := state.pending.AsTaskWait(); waiting {
			info := futureInfo{task: &taskExecution{TaskID: id, PollIntervalMs: hint}}
			if err := startTaskObservationTimer(wf, &info); err != nil {
				return nil, err
			}
			for {
				if _, err := info.pollTimer.Get(wf.Context()); err != nil {
					return nil, err
				}
				if info.pollRemainingMs == 0 {
					break
				}
				if err := startTaskPollTimer(wf, &info, info.pollRemainingMs); err != nil {
					return nil, err
				}
			}
			next, err := tooloperation.NewTaskGet(id)
			if err != nil {
				return nil, err
			}
			if err := state.advance(next); err != nil {
				return nil, err
			}
			continue
		}
		suspension, err := registeredRootSuspension(input, state)
		if err != nil {
			return nil, err
		}
		return &api.RunOutput{Subject: input.Subject, RunID: input.RunID, Suspension: suspension}, nil
	}
}

// cancelRegisteredTask delivers cancellation for the exact provider Task. The
// existing provider contract acknowledges the request without claiming its
// effects have already stopped; no additional task observation is required.
func (r *Runtime) cancelRegisteredTask(wf engine.WorkflowContext, state *registeredRootState) error {
	if state.task == nil {
		return nil
	}
	next, err := tooloperation.NewTaskCancel(state.task.TaskID)
	if err != nil {
		return errors.Join(errWorkCleanupFailure, err)
	}
	if err := state.advance(next); err != nil {
		return errors.Join(errWorkCleanupFailure, err)
	}
	result, err := wf.ExecuteToolActivity(engine.ToolActivityCall{Name: registeredRootActivity, Input: &state.call, Options: engine.ActivityOptions{Queue: state.queue, RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}}})
	if err != nil {
		return errors.Join(errWorkCleanupFailure, err)
	}
	if result.Failure != nil {
		return errors.Join(errWorkCleanupFailure, result.Failure.Error)
	}
	if err := validatePendingActivityOutput(result); err != nil {
		return errors.Join(errWorkCleanupFailure, err)
	}
	id, _, waiting := result.PendingExecution.AsTaskWait()
	if !waiting || id != state.task.TaskID {
		return errors.Join(errWorkCleanupFailure, errors.New("task cancellation acknowledgment changed the accepted task"))
	}
	state.pending = nil
	state.task = nil
	return nil
}
