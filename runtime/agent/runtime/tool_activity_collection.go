// Package runtime schedules ordinary tool activities and collects their saved
// outputs. Accepted Tasks remain inside this collection through workflow timers;
// only completed results enter tool history. Cancellation and later Task reads
// use the same configured activity route as the original invocation.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// collectActivityResultsAsComplete waits for scheduled activity outputs and
// retains Task handles through later reads. It returns completed results, host
// input, remaining work at the deadline, or the observed execution error.
func (e *toolBatchExec) collectActivityResultsAsComplete(wfCtx engine.WorkflowContext, futures []futureInfo, finalizeTimer engine.Future[time.Time]) (map[string]*ToolExecutionResult, []futureInfo, bool, error) {
	ctx := wfCtx.Context()
	activityByID := make(map[string]*ToolExecutionResult, len(futures))
	pending := append([]futureInfo(nil), futures...)
	var executionErr error
	for len(pending) > 0 {
		if err := wfCtx.Await(func() bool {
			if finalizeTimer != nil && finalizeTimer.IsReady() {
				return true
			}
			for _, info := range pending {
				if info.pollTimer != nil && info.pollTimer.IsReady() || info.future != nil && info.future.IsReady() {
					return true
				}
			}
			return false
		}); err != nil {
			if errors.Is(err, engine.ErrPlannerActivityDeadlineExceeded) {
				return activityByID, pending, true, executionErr
			}
			return activityByID, pending, false, errors.Join(executionErr, err)
		}

		var resultPubs []pendingHookPublication
		flushResults := func() {
			if len(resultPubs) > 0 {
				executionErr = errors.Join(executionErr, e.r.publishHookPublications(ctx, resultPubs))
				resultPubs = nil
			}
		}

		i := 0
		for i < len(pending) {
			info := pending[i]
			if info.pollTimer != nil {
				if finalizeTimer != nil && finalizeTimer.IsReady() {
					i++
					continue
				}
				if info.pollTimer.IsReady() {
					if err := e.continueTaskPoll(wfCtx, &info); err != nil {
						flushResults()
						return activityByID, pending, false, errors.Join(executionErr, err)
					}
					pending[i] = info
					e.ownedTasks[info.call.ToolCallID] = info
				}
				i++
				continue
			}
			if !info.future.IsReady() {
				i++
				continue
			}
			pending[i] = pending[len(pending)-1]
			pending = pending[:len(pending)-1]

			out, err := info.future.Get(ctx)
			if err != nil {
				if isRunCancellationError(err) || temporalerrors.IsRequestValidation(err) {
					flushResults()
					return activityByID, pending, false, errors.Join(executionErr, err)
				}
				if info.task != nil {
					if _, reading := info.call.ExecutionContinuation.AsTaskGet(); reading && temporalerrors.Retryable(err) {
						if err := startTaskObservationTimer(wfCtx, &info); err != nil {
							flushResults()
							return activityByID, pending, false, errors.Join(executionErr, err)
						}
						pending = append(pending, info)
						e.ownedTasks[info.call.ToolCallID] = info
						continue
					}
					if cancelErr := e.cancelAcceptedTask(wfCtx, info); cancelErr != nil {
						flushResults()
						return activityByID, pending, false, errors.Join(executionErr, err, cancelErr)
					}
				}
				duration := wfCtx.Now().Sub(info.startTime)
				result, pub, synthErr := e.prepareSynthesizedToolError(ctx, info.call, err, "tool activity failed", duration)
				if result != nil {
					activityByID[info.call.ToolCallID] = result
				}
				if pub != nil {
					resultPubs = append(resultPubs, *pub)
				}
				if synthErr != nil {
					executionErr = errors.Join(executionErr, synthErr)
				}
				continue
			}
			if out == nil {
				executionErr = errors.Join(
					executionErr,
					fmt.Errorf("tool %q returned nil output", info.call.Name),
				)
				continue
			}

			execResult, pub, err := e.prepareExecutionFromActivityOutput(ctx, info, out, wfCtx.Now().Sub(info.startTime))
			if pub != nil {
				resultPubs = append(resultPubs, *pub)
			}
			if err != nil {
				if execResult != nil {
					activityByID[info.call.ToolCallID] = execResult
				}
				executionErr = errors.Join(executionErr, err)
				continue
			}
			if execResult.mcpPending != nil {
				accepted, waiting, err := acceptTaskPending(&info, execResult.mcpPending)
				if err != nil {
					flushResults()
					return activityByID, pending, false, errors.Join(executionErr, err)
				}
				execResult.mcpPending = accepted
				execResult.task = cloneTaskExecution(info.task)
				if info.task != nil {
					e.ownedTasks[info.call.ToolCallID] = info
				}
				if waiting {
					if err := startTaskObservationTimer(wfCtx, &info); err != nil {
						flushResults()
						return activityByID, pending, false, errors.Join(executionErr, err)
					}
					pending = append(pending, info)
					continue
				}
			}
			// A returned input request transfers its Task to the workflow record.
			// Waiting Tasks stayed above for polling; completed Tasks need no cleanup.
			activityByID[info.call.ToolCallID] = execResult
			delete(e.ownedTasks, info.call.ToolCallID)
		}
		flushResults()
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return activityByID, pending, true, executionErr
		}
	}
	return activityByID, nil, false, executionErr
}

// executionFromActivityOutput decodes and validates one activity result, then
// publishes the canonical result event for the tool call.
func (e *toolBatchExec) executionFromActivityOutput(ctx context.Context, info futureInfo, out *ToolOutput, duration time.Duration) (*ToolExecutionResult, error) {
	result, pub, err := e.prepareExecutionFromActivityOutput(ctx, info, out, duration)
	if err != nil {
		return result, err
	}
	if pub != nil {
		if pubErr := e.r.publishHookPublications(ctx, []pendingHookPublication{*pub}); pubErr != nil {
			return result, pubErr
		}
	}
	return result, nil
}

// prepareExecutionFromActivityOutput decodes and validates one activity result,
// then prepares the canonical result event publication for the tool call.
func (e *toolBatchExec) prepareExecutionFromActivityOutput(ctx context.Context, info futureInfo, out *ToolOutput, duration time.Duration) (*ToolExecutionResult, *pendingHookPublication, error) {
	spec, ok, err := e.r.callSpec(info.call)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return e.prepareSynthesizedUnknownToolResult(ctx, info.call, duration)
	}

	if out.PendingExecution != nil {
		if info.call.TextOnly && pendingHostInput(out.PendingExecution) != nil {
			return nil, nil, errors.New("text-only tools cannot request MCP host input")
		}
		if err := validatePendingActivityOutput(out); err != nil {
			return nil, nil, err
		}
		return &ToolExecutionResult{mcpPending: out.PendingExecution, mcpToolCallID: info.call.ToolCallID, duration: duration, task: cloneTaskExecution(info.task), executionSequence: info.call.ExecutionSequence}, nil, nil
	}
	if err := out.Blocks.Validate(); err != nil {
		return nil, nil, fmt.Errorf("tool %q activity content: %w", info.call.Name, err)
	}
	var decoded any
	if out.Failure == nil && hasNonNullJSON(out.Payload.RawMessage()) {
		v, err := spec.Result.Codec.FromJSON(out.Payload)
		if err != nil {
			return nil, nil, fmt.Errorf("tool %q result decode failed (tool_call_id=%s): %w", info.call.Name, info.call.ToolCallID, err)
		}
		decoded = v
	}

	toolRes := &planner.ToolResult{
		Name:       info.call.Name,
		Result:     decoded,
		Bounds:     out.Bounds,
		ServerData: out.ServerData,
		Blocks:     out.Blocks.Clone(),
		ToolCallID: info.call.ToolCallID,
		Telemetry:  out.Telemetry,
	}
	toolRes.Failure = out.Failure
	if err := canonicalizeAndValidateWorkflowToolResult(spec, info.call, toolRes); err != nil {
		return nil, nil, err
	}
	if err := validateToolClarificationContract(info.call, toolRes, out.Clarification); err != nil {
		return nil, nil, err
	}
	result := &ToolExecutionResult{
		ToolResult:        toolRes,
		Clarification:     out.Clarification,
		duration:          duration,
		executionSequence: info.call.ExecutionSequence,
	}
	pub, err := e.prepareToolResultReceived(ctx, info.call, result, out.Payload, duration)
	if err != nil {
		return result, nil, err
	}
	return result, &pub, nil
}

// scheduleToolActivity selects the configured queue and retry policy for one
// exact execution operation. Original calls, Task reads and cancellation share
// this scheduler, so their payload, credentials and admission route cannot drift.
func (e *toolBatchExec) scheduleToolActivity(wfCtx engine.WorkflowContext, call ToolCall) (engine.Future[*ToolOutput], error) {
	var toolsetName string
	var ts ToolsetRegistration
	var hasTS bool
	if !e.r.isDynamicRegistryCall(call) {
		toolsetName, ts, hasTS = e.r.toolsetForTool(call.Name)
	}
	input := &ToolInput{
		Invocation:            genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})},
		OriginalArguments:     append(rawjson.Message(nil), call.TranscriptPayload()...),
		ExecutionContinuation: call.ExecutionContinuation,
		ExecutionSequence:     call.ExecutionSequence,
		TextOnly:              call.TextOnly,
		Registry:              call.Registry.Clone(),
		Subject:               genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(e.agentID))},

		RunID:            call.RunID,
		ToolsetName:      toolsetName,
		ToolName:         call.Name,
		ToolCallID:       call.ToolCallID,
		Payload:          append(rawjson.Message(nil), call.Payload...),
		SessionID:        call.SessionID,
		Labels:           cloneLabels(call.Labels),
		TurnID:           call.TurnID,
		ParentToolCallID: call.ParentToolCallID,
	}
	finishBy := e.finishBy
	cancelling := false
	if operation := call.ExecutionContinuation; operation != nil {
		_, cancelling = operation.AsTaskCancel()
		if cancelling {
			// Cleanup owns a separate obligation after the run deadline. Its
			// attempts use the configured activity timeout, not the expired run.
			finishBy = time.Time{}
		}
	}
	options := computeToolActivityOptions(wfCtx, e.toolActOptions, finishBy)
	if hasTS && ts.ActivityRetryPolicy != nil {
		options.RetryPolicy = *ts.ActivityRetryPolicy
	}
	if operation := call.ExecutionContinuation; operation != nil {
		_, reading := operation.AsTaskGet()
		_, _, answering := operation.AsTaskUpdate()
		if reading || answering || cancelling {
			// Workflow timers own repeated reads and cancellation delivery. An
			// uncertain answer must never be repeated by an activity policy.
			options.RetryPolicy.MaxAttempts = 1
			options.RetryPolicy.UnlimitedAttempts = false
		}
	}
	if options.Queue == "" && hasTS && !ts.Inline && ts.TaskQueue != "" {
		options.Queue = ts.TaskQueue
	}
	return wfCtx.ExecuteToolActivityAsync(engine.ToolActivityCall{Name: e.activityName, Input: input, Options: options})
}
