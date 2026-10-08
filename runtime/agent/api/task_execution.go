// Package api constructs unfinished job observations for generated native and
// MCP-backed tools. The workflow owns subsequent reads and host-answer operations;
// each immutable value retains only the facts required for its next operation.
package api

import (
	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/mcp"
)

// NewPendingTaskWait copies an already created job's identifier and polling
// guidance into an unfinished outcome. Callers receive no completed result; the
// workflow schedules a read of that job without invoking its creator again.
func NewPendingTaskWait(taskID string, pollIntervalMs *int64) (*PendingExecution, error) {
	return tooloperation.NewPendingTaskWait(taskID, pollIntervalMs)
}

// NewPendingTaskInput validates and copies a job's outstanding host questions.
// Ordinary request state is rejected because the job identifier owns these
// answers. The workflow submits answers to that job's update operation before
// scheduling another read; no completed tool result enters model history yet.
func NewPendingTaskInput(taskID string, pollIntervalMs *int64, input *mcp.InputRequired) (*PendingExecution, error) {
	return tooloperation.NewPendingTaskInput(taskID, pollIntervalMs, input)
}
