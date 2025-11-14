package runtime

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	RunInput           = api.RunInput
	PlanActivityInput  = api.PlanActivityInput
	PlanActivityOutput = api.PlanActivityOutput
	ToolInput          = api.ToolInput
	ToolOutput         = api.ToolOutput

	// WorkflowOptions mirrors the subset of engine start options we expose through
	// the runtime. Memo/SearchAttributes follow Temporal semantics but remain generic
	// maps so other engines can interpret them as needed.
	WorkflowOptions = api.WorkflowOptions

	// PolicyOverrides configures per-run policy constraints.
	// All fields are optional; zero values mean no override.
	PolicyOverrides = api.PolicyOverrides

	// RunOutput represents the final outcome returned by a run workflow, including the
	// concluding assistant message plus tool traces and planner notes for callers.
	RunOutput = api.RunOutput

	// ActivityToolExecutor implements ToolActivityExecutor for regular tools that execute via
	// workflow activities. It uses ExecuteActivityAsync for parallel execution with other
	// tools in the same batch.
	ActivityToolExecutor struct {
		// activityName is the registered activity name for tool execution.
		activityName string
		// queue is the task queue where the activity should be scheduled.
		queue string
	}

	// ToolCallMeta carries run-scoped identifiers for executors. It provides explicit
	// access to business context (RunID, SessionID, TurnID, correlation IDs)
	// without relying on context values.
	ToolCallMeta struct {
		// RunID is the durable workflow execution identifier of the run that
		// owns this tool call. It remains stable across retries and is used to
		// correlate runtime records and telemetry.
		RunID string

		// SessionID logically groups related runs (for example a chat
		// conversation). Services typically index memory and search attributes
		// by session.
		SessionID string

		// TurnID identifies the conversational turn that produced this tool
		// call. When set, event streams use it to order and group events.
		TurnID string

		// ToolCallID uniquely identifies this tool invocation. It is used to
		// correlate start/update/end events and parent/child relationships.
		ToolCallID string

		// ParentToolCallID is the identifier of the parent tool call when this
		// invocation is a child (for example a tool launched by an agent-tool).
		// UIs and subscribers use it to reconstruct the call tree.
		ParentToolCallID string
	}

	// ToolCallExecutor executes a tool call and returns a planner.ToolResult. This
	// generic interface enables a uniform execution model across method-backed
	// tools, MCP tools, and agent-tools. Registrations accept a ToolCallExecutor and
	// the runtime delegates execution via this interface.
	ToolCallExecutor interface {
		Execute(ctx context.Context, meta *ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error)
	}

	// ToolCallExecutorFunc adapts a function to the ToolCallExecutor interface.
	ToolCallExecutorFunc func(ctx context.Context, meta *ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error)

	// ToolActivityExecutor handles execution of a single tool via workflow
	// activities. Implementations decide how to schedule and await activity
	// completion while preserving workflow determinism.
	ToolActivityExecutor interface {
		// Execute runs the tool with the given input and returns the result.
		// The workflow context is provided for workflow-level operations (activities,
		// timers, etc.). Input and output use the ToolInput/ToolOutput envelope.
		Execute(ctx context.Context, wfCtx engine.WorkflowContext, input *ToolInput) (*ToolOutput, error)
	}

	// Timing groups per-run timing overrides in a single structure.
	// Zero values mean no override.
	Timing struct {
		// Budget sets the total wall-clock budget for this run.
		Budget time.Duration
		// Plan sets the Plan/Resume activity timeout for this run.
		Plan time.Duration
		// Tools sets the default ExecuteTool activity timeout for this run.
		Tools time.Duration
		// PerToolTimeout allows targeting specific tools with custom timeouts.
		PerToolTimeout map[tools.Ident]time.Duration
	}
)

// Ensure ActivityToolExecutor implements ToolActivityExecutor at compile time.
var _ ToolActivityExecutor = (*ActivityToolExecutor)(nil)

// Execute calls f(ctx, meta, call).
func (f ToolCallExecutorFunc) Execute(ctx context.Context, meta *ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
	return f(ctx, meta, call)
}

// Execute schedules the tool as a workflow activity and waits for its result.
// This maintains workflow determinism while allowing the tool to run out-of-process.
// The input is passed through to the activity as-is (already properly formatted).
func (e *ActivityToolExecutor) Execute(ctx context.Context, wfCtx engine.WorkflowContext, input *ToolInput) (*ToolOutput, error) {
	if input == nil {
		return nil, errors.New("tool input is required")
	}
	req := engine.ActivityRequest{
		Name:  e.activityName,
		Queue: e.queue,
		Input: input,
	}

	future, err := wfCtx.ExecuteActivityAsync(ctx, req)
	if err != nil {
		return nil, err
	}

	var result ToolOutput
	if err := future.Get(ctx, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
