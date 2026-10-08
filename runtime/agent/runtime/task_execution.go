// Package runtime observes accepted MCP Tasks through ordinary tool activities
// and workflow timers. Private state keeps exact Task identity and answered
// request keys across host-input suspensions. No Task read repeats tools/call.
package runtime

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// taskExecution retains only facts owned by one unfinished remote Task.
	// An empty TaskID is valid; the containing pointer records Task presence.
	taskExecution struct {
		// TaskID is the exact server-owned identifier, including an empty string.
		TaskID string
		// PollIntervalMs is the last observed guidance for this Task's next read.
		PollIntervalMs *int64
		// AnsweredRequests holds sorted keys acknowledged by tasks/update.
		AnsweredRequests []string
	}
)

const (
	// defaultTaskPollIntervalMs spaces this Task's reads when the server supplies
	// no guidance. It is not a limit on retention, activity time or run duration.
	defaultTaskPollIntervalMs int64 = 1000
)

// acceptTaskPending retains the current Task and suppresses questions already
// answered by an acknowledged update. Empty outstanding questions keep polling
// rather than publishing another host-input request.
func acceptTaskPending(info *futureInfo, pending *api.PendingExecution) (*api.PendingExecution, bool, error) {
	id, hint, waiting := pending.AsTaskWait()
	var input *mcp.InputRequired
	if !waiting {
		var selected bool
		id, hint, input, selected = pending.AsTaskInput()
		if !selected {
			if info.task != nil {
				return nil, false, errors.New("existing task returned ordinary tool input")
			}
			return pending, false, nil
		}
	}
	if info.task == nil {
		info.task = &taskExecution{TaskID: id}
	} else if info.task.TaskID != id {
		return nil, false, errors.New("existing task changed its identifier")
	}
	if operation := info.call.ExecutionContinuation; operation != nil {
		if taskID, answers, update := operation.AsTaskUpdate(); update {
			if taskID != id {
				return nil, false, errors.New("task acknowledgment changed its identifier")
			}
			for requestID := range answers {
				info.task.AnsweredRequests = append(info.task.AnsweredRequests, requestID)
			}
			slices.Sort(info.task.AnsweredRequests)
			info.task.AnsweredRequests = slices.Compact(info.task.AnsweredRequests)
			// An acknowledgment has no new observation guidance. Keep the last
			// observed hint until a later tasks/get supplies its current value.
			hint = info.task.PollIntervalMs
		}
	}
	info.task.PollIntervalMs = cloneTaskPollInterval(hint)
	if input != nil {
		for _, answered := range info.task.AnsweredRequests {
			delete(input.Requests, answered)
		}
		if len(input.Requests) > 0 {
			filtered, err := tooloperation.NewPendingTaskInput(id, hint, input)
			return filtered, false, err
		}
	}
	retained, err := tooloperation.NewPendingTaskWait(id, hint)
	return retained, true, err
}

// startTaskPollTimer divides an integer-millisecond hint into timers that fit
// Go's duration representation. The remainder is saved in workflow state, so
// an extreme hint cannot overflow or silently shorten the complete wait.
func startTaskPollTimer(wfCtx engine.WorkflowContext, info *futureInfo, millis int64) error {
	segment := min(max(millis, 0), int64(math.MaxInt64)/int64(time.Millisecond))
	info.pollRemainingMs = max(millis, 0) - segment
	timer, err := wfCtx.NewTimer(wfCtx.Context(), time.Duration(segment)*time.Millisecond)
	if err != nil {
		return err
	}
	info.pollTimer = timer
	info.future = nil
	return nil
}

// cloneTaskExecution copies the saved answer keys and hint before an execution
// changes them. A predecessor checkpoint therefore remains immutable.
func cloneTaskExecution(task *taskExecution) *taskExecution {
	if task == nil {
		return nil
	}
	return &taskExecution{TaskID: task.TaskID, PollIntervalMs: cloneTaskPollInterval(task.PollIntervalMs), AnsweredRequests: slices.Clone(task.AnsweredRequests)}
}

// cloneTaskPollInterval lets new observations replace guidance without changing
// a saved predecessor or the server-authored unfinished value.
func cloneTaskPollInterval(hint *int64) *int64 {
	if hint == nil {
		return nil
	}
	value := *hint
	return &value
}

// validateTaskInputState checks exact saved ownership and answer-key order at
// checkpoint admission. Ordinary input cannot inherit another Task's state.
func validateTaskInputState(pending *api.PendingExecution, task *taskExecution) error {
	id, hint, input, selected := pending.AsTaskInput()
	if !selected {
		if task != nil {
			return errors.New("ordinary input cannot contain task state")
		}
		return nil
	}
	if task == nil || task.TaskID != id || !equalTaskPollInterval(task.PollIntervalMs, hint) {
		return errors.New("task input does not match saved task ownership")
	}
	for index, key := range task.AnsweredRequests {
		if index > 0 && key <= task.AnsweredRequests[index-1] {
			return errors.New("saved task answer keys must be unique and ordered")
		}
		if _, repeated := input.Requests[key]; repeated {
			return errors.New("task input repeats an answered question")
		}
	}
	return nil
}

// equalTaskPollInterval compares presence and the exact saved guidance rather
// than giving an omitted hint the meaning of an explicitly zero hint.
func equalTaskPollInterval(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// validatePendingActivityOutput rejects completed fields on an unfinished
// activity reply. The selected native branch and its interaction content were
// checked at construction or decoding before workflow collection.
func validatePendingActivityOutput(output *ToolOutput) error {
	if output.PendingExecution == nil {
		return errors.New("unfinished activity output requires a pending outcome")
	}
	if len(output.Payload) != 0 || len(output.ServerData) != 0 || len(output.Blocks) != 0 || output.Bounds != nil || output.Telemetry != nil || output.Failure != nil || output.Clarification != nil {
		return errors.New("unfinished outcome cannot accompany a completed activity result")
	}
	return output.PendingExecution.Validate()
}

// continueTaskPoll consumes one completed timer and either waits its remaining
// segments or starts the next saved tasks/get operation through the existing
// activity route. Original model arguments and tool-call identity stay exact.
func (e *toolBatchExec) continueTaskPoll(wfCtx engine.WorkflowContext, info *futureInfo) error {
	if _, err := info.pollTimer.Get(wfCtx.Context()); err != nil {
		return err
	}
	if info.pollRemainingMs > 0 {
		return startTaskPollTimer(wfCtx, info, info.pollRemainingMs)
	}
	if info.call.ExecutionSequence == math.MaxUint64 {
		return errors.New("task execution sequence cannot be incremented")
	}
	operation, err := tooloperation.NewTaskGet(info.task.TaskID)
	if err != nil {
		return err
	}
	info.call.ExecutionSequence++
	info.call.ExecutionContinuation = operation
	e.ownedTasks[info.call.ToolCallID] = *info
	future, err := e.scheduleToolActivity(wfCtx, info.call)
	if err != nil {
		return fmt.Errorf("schedule task observation: %w", err)
	}
	info.pollTimer = nil
	info.future = future
	return nil
}

// cancelAcceptedTasks sends cancellation for every Task this batch still owns.
// Sorted invocation IDs make replay ordering stable. A successful acknowledgment
// ends observation without claiming that the server's effects have stopped.
func (e *toolBatchExec) cancelAcceptedTasks(wfCtx engine.WorkflowContext) error {
	var cancellationErr error
	for _, id := range slices.Sorted(maps.Keys(e.ownedTasks)) {
		info := e.ownedTasks[id]
		if operation := info.call.ExecutionContinuation; operation != nil {
			if _, alreadySent := operation.AsTaskCancel(); alreadySent {
				continue
			}
		}
		cancellationErr = errors.Join(cancellationErr, e.cancelAcceptedTask(wfCtx, info))
	}
	return cancellationErr
}

// cancelAcceptedTask schedules cancellation independently of the stopped run's
// context. The workflow waits for the saved acknowledgment before it settles;
// another tasks/get is neither needed nor performed.
func (e *toolBatchExec) cancelAcceptedTask(wfCtx engine.WorkflowContext, info futureInfo) error {
	if info.call.ExecutionSequence == math.MaxUint64 {
		return errors.New("task cancellation sequence cannot be incremented")
	}
	operation, err := tooloperation.NewTaskCancel(info.task.TaskID)
	if err != nil {
		return err
	}
	info.call.ExecutionSequence++
	info.call.ExecutionContinuation = operation
	// Keep the exact cancellation operation if its activity response is lost.
	// A workflow replay schedules the same activity with the same sequence.
	e.ownedTasks[info.call.ToolCallID] = info
	detached := wfCtx.Detached()
	future, err := e.scheduleToolActivity(detached, info.call)
	if err != nil {
		return fmt.Errorf("schedule task cancellation: %w", err)
	}
	output, err := future.Get(detached.Context())
	if err != nil {
		return fmt.Errorf("cancel accepted task: %w", err)
	}
	if output == nil {
		return errors.New("task cancellation returned no output")
	}
	if output.Failure != nil {
		return fmt.Errorf("task cancellation failed: %w", output.Failure.Error)
	}
	if err := validatePendingActivityOutput(output); err != nil {
		return err
	}
	id, _, waiting := output.PendingExecution.AsTaskWait()
	if !waiting || id != info.task.TaskID {
		return errors.New("task cancellation acknowledgment does not match the accepted task")
	}
	delete(e.ownedTasks, info.call.ToolCallID)
	return nil
}
