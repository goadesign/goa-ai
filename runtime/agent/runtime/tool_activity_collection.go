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
			return activityByID, pending, false, err
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
						return activityByID, pending, false, err
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
					return activityByID, pending, false, err
				}
				if info.task != nil {
					if cancelErr := e.cancelAcceptedTask(wfCtx, info); cancelErr != nil {
						return activityByID, pending, false, errors.Join(err, cancelErr)
					}
				}
				duration := wfCtx.Now().Sub(info.startTime)
				result, synthErr := e.synthesizeToolError(ctx, info.call, err, "tool activity failed", duration)
				if result != nil {
					activityByID[info.call.ToolCallID] = result
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

			if info.task != nil && out.Failure != nil {
				if err := e.cancelAcceptedTask(wfCtx, info); err != nil {
					return activityByID, pending, false, err
				}
			}
			execResult, err := e.executionFromActivityOutput(ctx, info, out, wfCtx.Now().Sub(info.startTime))
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
					return activityByID, pending, false, err
				}
				execResult.mcpPending = accepted
				execResult.task = cloneTaskExecution(info.task)
				if info.task != nil {
					e.ownedTasks[info.call.ToolCallID] = info
				}
				if waiting {
					delay := defaultTaskPollIntervalMs
					if hint := info.task.PollIntervalMs; hint != nil {
						delay = *hint
					}
					if err := startTaskPollTimer(wfCtx, &info, delay); err != nil {
						return activityByID, pending, false, err
					}
					pending = append(pending, info)
					continue
				}
			}
			if execResult.mcpPending == nil {
				delete(e.ownedTasks, info.call.ToolCallID)
			}
			activityByID[info.call.ToolCallID] = execResult
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return activityByID, pending, true, executionErr
		}
	}
	return activityByID, nil, false, executionErr
}

// executionFromActivityOutput decodes and validates one activity result, then
// publishes the canonical result event for the tool call.
func (e *toolBatchExec) executionFromActivityOutput(ctx context.Context, info futureInfo, out *ToolOutput, duration time.Duration) (*ToolExecutionResult, error) {
	spec, ok, err := lookupCallSpec(info.call, e.r.toolSpec)
	if err != nil {
		return nil, err
	}
	if !ok {
		return e.synthesizeUnknownToolResult(ctx, info.call, duration)
	}

	if out.PendingExecution != nil {
		if info.call.TextOnly && pendingHostInput(out.PendingExecution) != nil {
			return nil, errors.New("text-only tools cannot request MCP host input")
		}
		if err := validatePendingActivityOutput(out); err != nil {
			return nil, err
		}
		return &ToolExecutionResult{mcpPending: out.PendingExecution, mcpToolCallID: info.call.ToolCallID, duration: duration, task: cloneTaskExecution(info.task), executionSequence: info.call.ExecutionSequence}, nil
	}
	if err := out.Blocks.Validate(); err != nil {
		return nil, fmt.Errorf("tool %q activity content: %w", info.call.Name, err)
	}
	var decoded any
	if out.Failure == nil && hasNonNullJSON(out.Payload.RawMessage()) {
		v, err := spec.Result.Codec.FromJSON(out.Payload)
		if err != nil {
			return nil, fmt.Errorf("tool %q result decode failed (tool_call_id=%s): %w", info.call.Name, info.call.ToolCallID, err)
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
		return nil, err
	}
	if err := validateToolClarificationContract(info.call, toolRes, out.Clarification); err != nil {
		return nil, err
	}
	result := &ToolExecutionResult{
		ToolResult:        toolRes,
		Clarification:     out.Clarification,
		duration:          duration,
		executionSequence: info.call.ExecutionSequence,
	}
	result.resultRecord, err = e.publishToolResultReceived(ctx, info.call, toolRes, out.Payload, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

// scheduleToolActivity selects the configured queue and retry policy for one
// exact execution operation. Original calls, Task reads and cancellation share
// this scheduler, so their payload, credentials and admission route cannot drift.
func (e *toolBatchExec) scheduleToolActivity(wfCtx engine.WorkflowContext, call ToolCall) (engine.Future[*ToolOutput], error) {
	var toolsetName string
	var ts ToolsetRegistration
	var hasTS bool
	if call.Registry == nil {
		toolsetName, ts, hasTS = e.r.toolsetForTool(call.Name)
	}
	input := &ToolInput{
		ExecutionContinuation: call.ExecutionContinuation,
		ExecutionSequence:     call.ExecutionSequence,
		TextOnly:              call.TextOnly,
		Registry:              call.Registry.Clone(),
		AgentID:               e.agentID,
		RunID:                 e.runID,
		ToolsetName:           toolsetName,
		ToolName:              call.Name,
		ToolCallID:            call.ToolCallID,
		Payload:               append(rawjson.Message(nil), call.Payload...),
		SessionID:             call.SessionID,
		Labels:                cloneLabels(call.Labels),
		TurnID:                call.TurnID,
		ParentToolCallID:      call.ParentToolCallID,
	}
	options := computeToolActivityOptions(wfCtx, e.toolActOptions, e.finishBy)
	if hasTS && ts.ActivityRetryPolicy != nil {
		options.RetryPolicy = *ts.ActivityRetryPolicy
	}
	if options.Queue == "" && hasTS && !ts.Inline && ts.TaskQueue != "" {
		options.Queue = ts.TaskQueue
	}
	return wfCtx.ExecuteToolActivityAsync(engine.ToolActivityCall{Name: e.activityName, Input: input, Options: options})
}
