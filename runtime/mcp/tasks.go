// Package mcp reads server-owned Tasks through the existing HTTP and stdio
// transports. Each operation observes, answers, or cancels one existing task;
// it never repeats the tool call that created the work or starts a polling loop.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

type (
	// TaskStatus names the server's current state for one asynchronous tool call.
	TaskStatus string

	// TaskInfo contains the server's metadata for one task observation. Retention
	// and polling guidance may change on later observations of the same task.
	TaskInfo struct {
		// TaskID is the server-owned identifier used by every later task operation.
		TaskID string `json:"taskId"` //nolint:tagliatelle // MCP wire name.
		// Status is the state reported by the server for this observation.
		Status TaskStatus `json:"status"`
		// StatusMessage is the optional explanation, including an explicit empty string.
		StatusMessage *string `json:"statusMessage,omitempty"` //nolint:tagliatelle // MCP wire name.
		// CreatedAt is the server's creation timestamp, retained exactly as returned.
		CreatedAt string `json:"createdAt"` //nolint:tagliatelle // MCP wire name.
		// LastUpdatedAt is the server's update timestamp, retained exactly as returned.
		LastUpdatedAt string `json:"lastUpdatedAt"` //nolint:tagliatelle // MCP wire name.
		// TTLMs is retention from creation in integer milliseconds; nil means unlimited.
		// Its JSON member is always present, including when its value is null.
		TTLMs *int64 `json:"ttlMs"` //nolint:tagliatelle // MCP wire name.
		// PollIntervalMs is optional guidance for the next poll in integer milliseconds.
		PollIntervalMs *int64 `json:"pollIntervalMs,omitempty"` //nolint:tagliatelle // MCP wire name.
	}

	// Task is a validated observation returned by tasks/get. Its accessors expose
	// only the data associated with the status in Info; callers cannot set another
	// status's fields on the observation.
	Task struct {
		info    TaskInfo
		input   *InputRequired
		result  *CallResponse
		failure *Error
	}

	taskSupportKey     struct{}
	taskGetResult      struct{ task Task }
	taskAcknowledgment struct {
		ResultType string          `json:"resultType"` //nolint:tagliatelle // MCP wire name.
		Meta       json.RawMessage `json:"_meta"`      //nolint:tagliatelle // MCP wire name.
	}
)

const (
	// TaskWorking means the server still owns ongoing execution.
	TaskWorking TaskStatus = "working"
	// TaskInputRequired means the server is waiting for host answers.
	TaskInputRequired TaskStatus = "input_required"
	// TaskCompleted means a tool result is available, including a tool-level error.
	TaskCompleted TaskStatus = "completed"
	// TaskFailed means execution ended with a JSON-RPC error.
	TaskFailed TaskStatus = "failed"
	// TaskCancelled means the server reports execution was cancelled.
	TaskCancelled TaskStatus = "cancelled"

	tasksExtension    = "io.modelcontextprotocol/tasks"
	resultTask        = "task"
	methodTasksGet    = "tasks/get"
	methodTasksUpdate = "tasks/update"
	methodTasksCancel = "tasks/cancel"
)

// WithTaskSupport declares that the host can retain a Task handle and use task
// operations after this tool call. The server still chooses whether to return a
// Task or an ordinary result. This declaration starts no work or polling loop.
func WithTaskSupport(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskSupportKey{}, struct{}{})
}

// Info returns the metadata reported with this observation.
func (t Task) Info() TaskInfo {
	return t.info
}

// AsInputRequired returns the outstanding host requests only for input_required.
// These requests are answered with UpdateTask, without replaying tools/call.
func (t Task) AsInputRequired() (*InputRequired, bool) {
	return t.input, t.input != nil
}

// AsCompleted returns the final tool result only for completed. Its IsError
// flag represents a tool-level error, which is still a completed task.
func (t Task) AsCompleted() (CallResponse, bool) {
	if t.result == nil {
		return CallResponse{}, false
	}
	return *t.result, true
}

// AsFailed returns the JSON-RPC execution error only for failed.
func (t Task) AsFailed() (*Error, bool) {
	return t.failure, t.failure != nil
}

// GetTask reads the complete current state of one server-owned task.
func (c *HTTPCaller) GetTask(ctx context.Context, taskID string) (Task, error) {
	return c.transport.GetTask(ctx, c.endpoint, taskID)
}

// UpdateTask submits host answers and returns when the server acknowledges them.
// The task may still report the previous state until its owner applies the answers.
func (c *HTTPCaller) UpdateTask(ctx context.Context, taskID string, responses map[string]json.RawMessage) error {
	return c.transport.UpdateTask(ctx, c.endpoint, taskID, responses)
}

// CancelTask requests cancellation and returns its acknowledgement. GetTask
// determines whether work ultimately completes or is cancelled.
func (c *HTTPCaller) CancelTask(ctx context.Context, taskID string) error {
	return c.transport.CancelTask(ctx, c.endpoint, taskID)
}

// GetTask reads one task through the same endpoint and authorization transport
// used by the original tool call. It returns no result for another task ID.
func (t *HTTPTransport) GetTask(ctx context.Context, endpoint, taskID string) (Task, error) {
	var result taskGetResult
	if err := t.call(WithTaskSupport(ctx), endpoint, methodTasksGet, map[string]any{"taskId": taskID}, &result); err != nil {
		return Task{}, err
	}
	return normalizeTask(ctx, taskID, result.task, t.inputSupport)
}

// UpdateTask submits a possibly partial answer object through the existing
// transport. The server owns which keys remain outstanding and applies answers.
func (t *HTTPTransport) UpdateTask(ctx context.Context, endpoint, taskID string, responses map[string]json.RawMessage) error {
	params, err := taskUpdateParams(taskID, responses)
	if err != nil {
		return err
	}
	var result taskAcknowledgment
	if err := t.call(WithTaskSupport(ctx), endpoint, methodTasksUpdate, params, &result); err != nil {
		return err
	}
	return result.validate()
}

// CancelTask sends cooperative cancellation without treating its acknowledgement
// as proof that execution stopped.
func (t *HTTPTransport) CancelTask(ctx context.Context, endpoint, taskID string) error {
	var result taskAcknowledgment
	if err := t.call(WithTaskSupport(ctx), endpoint, methodTasksCancel, map[string]any{"taskId": taskID}, &result); err != nil {
		return err
	}
	return result.validate()
}

// GetTask reads an existing task through the running stdio peer.
func (c *StdioCaller) GetTask(ctx context.Context, taskID string) (Task, error) {
	var result taskGetResult
	if err := c.call(WithTaskSupport(ctx), methodTasksGet, map[string]any{"taskId": taskID}, &result); err != nil {
		return Task{}, err
	}
	return normalizeTask(ctx, taskID, result.task, c.inputSupport)
}

// UpdateTask submits host answers to an existing task through the stdio peer.
func (c *StdioCaller) UpdateTask(ctx context.Context, taskID string, responses map[string]json.RawMessage) error {
	params, err := taskUpdateParams(taskID, responses)
	if err != nil {
		return err
	}
	var result taskAcknowledgment
	if err := c.call(WithTaskSupport(ctx), methodTasksUpdate, params, &result); err != nil {
		return err
	}
	return result.validate()
}

// CancelTask requests cooperative cancellation from the running stdio peer.
func (c *StdioCaller) CancelTask(ctx context.Context, taskID string) error {
	var result taskAcknowledgment
	if err := c.call(WithTaskSupport(ctx), methodTasksCancel, map[string]any{"taskId": taskID}, &result); err != nil {
		return err
	}
	return result.validate()
}

// taskUpdateParams checks the external answer object before it enters either
// transport. An empty object is valid; a missing object or non-object answer fails.
func taskUpdateParams(taskID string, responses map[string]json.RawMessage) (map[string]any, error) {
	if responses == nil {
		return nil, &Error{Code: JSONRPCInvalidParams, Message: "task inputResponses must be an object"}
	}
	for _, raw := range responses {
		if err := validateMeta(raw); err != nil || len(raw) == 0 {
			return nil, &Error{Code: JSONRPCInvalidParams, Message: "task input responses must be JSON objects"}
		}
	}
	return map[string]any{"taskId": taskID, "inputResponses": responses}, nil
}

// normalizeTask requires the requested task's state and checks host input before
// an interaction reaches application code. Completed content was already decoded.
func normalizeTask(ctx context.Context, taskID string, task Task, support InputSupport) (Task, error) {
	if task.info.TaskID != taskID {
		return Task{}, NewMalformedResponseError(errors.New("tasks/get returned another task ID"))
	}
	if task.input != nil {
		if ctx.Value(hostInputDisabledKey{}) != nil {
			return Task{}, NewMalformedResponseError(errors.New("host input is disabled for this operation"))
		}
		if err := task.input.Validate(support); err != nil {
			return Task{}, NewMalformedResponseError(err)
		}
	}
	return task, nil
}

// validate requires a complete acknowledgement while retaining open MCP metadata.
func (r taskAcknowledgment) validate() error {
	if r.ResultType != resultComplete {
		return NewMalformedResponseError(errors.New("task acknowledgement must have resultType complete"))
	}
	if err := validateMeta(r.Meta); err != nil {
		return NewMalformedResponseError(err)
	}
	return nil
}

// completedTaskResult uses the ordinary tool result decoder, including its typed
// content checks. A tool-level error retains its content and completes the task.
func completedTaskResult(result toolsCallResult) (CallResponse, error) {
	response, err := normalizeToolResult(result)
	if err == nil {
		return response, nil
	}
	var toolErr *ToolExecutionError
	if errors.As(err, &toolErr) {
		return toolErr.Response, nil
	}
	return CallResponse{}, err
}
