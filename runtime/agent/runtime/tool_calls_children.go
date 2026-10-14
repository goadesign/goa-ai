// Package runtime collects child workflow results into the active tool batch.
// Cancellation can transfer ready results while an ordinary activity still runs;
// normal collection and finalization publish those same results exactly once.
package runtime

import (
	"context"
	"errors"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/planner"
)

// collectAgentChildResults waits for child completion and publishes each tool
// result. Results already retained during cancellation remain in the same map.
func (e *toolBatchExec) collectAgentChildResults(wfCtx engine.WorkflowContext, children []agentChildFutureInfo, out map[string]*ToolExecutionResult, finalizeTimer engine.Future[time.Time]) (map[string]*ToolExecutionResult, []agentChildFutureInfo, bool, error) {
	ctx := wfCtx.Context()
	if len(children) == 0 {
		return out, nil, false, nil
	}

	pending := append([]agentChildFutureInfo(nil), children...)
	var executionErr error
	for len(pending) > 0 {
		if err := wfCtx.Await(func() bool {
			if finalizeTimer != nil && finalizeTimer.IsReady() {
				return true
			}
			for _, info := range pending {
				if info.handle.IsReady() {
					return true
				}
			}
			return false
		}); err != nil {
			if errors.Is(err, engine.ErrPlannerActivityDeadlineExceeded) {
				return out, pending, true, executionErr
			}
			return out, pending, false, errors.Join(executionErr, err)
		}

		i := 0
		for i < len(pending) {
			info := pending[i]
			if result, collected := out[info.call.ToolCallID]; collected {
				pending[i] = pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				executionErr = errors.Join(executionErr, e.publishChildResult(ctx, info.call, result))
				continue
			}
			if !info.handle.IsReady() {
				i++
				continue
			}
			pending[i] = pending[len(pending)-1]
			pending = pending[:len(pending)-1]

			outPtr, err := info.handle.Get(wfCtx.Context())
			if err != nil {
				if isRunCancellationError(err) || temporalerrors.IsOutputContract(err) || temporalerrors.IsRequestValidation(err) {
					return out, pending, false, errors.Join(executionErr, err)
				}
				if _, ok := temporalerrors.Provider(err); ok {
					return out, pending, false, errors.Join(executionErr, err)
				}
				duration := wfCtx.Now().Sub(info.startTime)
				result, synthErr := e.synthesizeToolError(ctx, info.call, err, "agent tool execution failed", duration)
				if result != nil {
					out[info.call.ToolCallID] = result
				}
				if synthErr != nil {
					executionErr = errors.Join(executionErr, synthErr)
				}
				continue
			}
			result, err := e.adaptCompletedChild(wfCtx, info, outPtr)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			out[info.call.ToolCallID] = result
			executionErr = errors.Join(executionErr, e.publishChildResult(ctx, info.call, result))
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return out, pending, true, executionErr
		}
	}
	return out, nil, false, executionErr
}

// retainReadyChildResults transfers successful child outputs into the batch's
// result map while collection may be waiting on another activity. It publishes
// no hooks; normal collection or step finalization records each result once.
func (e *toolBatchExec) retainReadyChildResults(wfCtx engine.WorkflowContext) error {
	for _, info := range e.batch.childFutures {
		if _, collected := e.batch.inlineByID[info.call.ToolCallID]; collected || !info.handle.IsReady() {
			continue
		}
		output, err := info.handle.Get(wfCtx.Context())
		if err != nil {
			continue
		}
		result, err := e.adaptCompletedChild(wfCtx, info, output)
		if err != nil {
			return err
		}
		e.batch.inlineByID[info.call.ToolCallID] = result
	}
	return nil
}

// adaptCompletedChild validates one completed child and retains either its
// unanswered suspension or its typed tool result without publishing side effects.
func (e *toolBatchExec) adaptCompletedChild(wfCtx engine.WorkflowContext, info agentChildFutureInfo, output *api.RunOutput) (*ToolExecutionResult, error) {
	if err := validateWorkflowOutput(output, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(info.cfg.Definition.route.ID))}, info.nestedRun.RunID); err != nil {
		return nil, err
	}
	if output.Suspension != nil {
		return &ToolExecutionResult{
			ToolResult:      &planner.ToolResult{Name: info.call.Name, ToolCallID: info.call.ToolCallID},
			childSuspension: output.Suspension,
			duration:        wfCtx.Now().Sub(info.startTime),
		}, nil
	}
	toolResult, err := e.r.adaptAgentChildOutput(info.cfg, &info.call, info.nestedRun, output)
	if err != nil {
		return nil, err
	}
	result := Executed(toolResult)
	result.duration = wfCtx.Now().Sub(info.startTime)
	return result, nil
}

// publishChildResult records a completed tool result once. Suspended children
// retain unanswered calls until their continuation supplies a result.
func (e *toolBatchExec) publishChildResult(ctx context.Context, call ToolCall, result *ToolExecutionResult) error {
	if result.childSuspension != nil || result.resultPublished {
		return nil
	}
	data, err := e.r.materializeToolResult(ctx, call, result.ToolResult)
	if err != nil {
		return err
	}
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, result, data, result.duration)
	if err != nil {
		return err
	}
	result.resultPublished = true
	return nil
}
