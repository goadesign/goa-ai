package runtime

import (
	"context"
	"encoding/json"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RunInput captures everything a generated workflow needs to start or resume a run.
	// It ensures planners receive full conversational context plus caller-provided labels
	// and metadata.
	RunInput struct {
		// AgentID identifies which agent should process the run.
		AgentID string
		// RunID is the durable workflow execution identifier.
		RunID string
		// SessionID groups related runs (e.g., multi-turn conversations).
		SessionID string
		// TurnID identifies the conversational turn (optional). When set, all events
		// produced during this run are tagged with this TurnID for UI grouping.
		TurnID string
		// Messages carries the conversation history supplied by the caller.
		Messages []planner.AgentMessage
		// Labels contains caller-provided metadata (tenant, priority, etc.).
		Labels map[string]string
		// Metadata allows orchestrators to attach arbitrary structured data.
		Metadata map[string]any

		// WorkflowOptions carries engine-specific start options (memo, search attributes,
		// custom task queues). If nil, the runtime derives defaults from the agent
		// registration.
		WorkflowOptions *WorkflowOptions

		// Policy carries optional per-run policy overrides applied on every planner turn.
		// These options allow callers to set caps and tool filters without modifying
		// the agent registration defaults.
		Policy PolicyOverrides
	}

	// WorkflowOptions mirrors the subset of engine start options we expose through
	// the runtime. Memo/SearchAttributes follow Temporal semantics but remain generic
	// maps so other engines can interpret them as needed.
	WorkflowOptions struct {
		Memo             map[string]any
		SearchAttributes map[string]any
		TaskQueue        string
		RetryPolicy      engine.RetryPolicy
	}

	// PolicyOverrides configures per-run policy constraints.
	// All fields are optional; zero values mean no override.
	PolicyOverrides struct {
		// PerTurnMaxToolCalls limits the number of tool calls the runtime will execute
		// in a single turn (prior to PlanResume). Zero means no per-turn limit.
		PerTurnMaxToolCalls int
		// RestrictToTool filters the candidate tools to a single tool for this run.
		// Empty means no restriction.
		RestrictToTool tools.Ident
		// AllowedTags filters candidate tools to those whose tags intersect this list.
		// Empty means allow all tags.
		AllowedTags []string
		// DeniedTags filters out candidate tools that have any of these tags.
		// Empty means no denylist.
		DeniedTags []string
	}

	// RunOutput represents the final outcome returned by a run workflow, including the
	// concluding assistant message plus tool traces and planner notes for callers.
	RunOutput struct {
		// AgentID echoes the agent that produced the result.
		AgentID string
		// RunID echoes the workflow execution identifier.
		RunID string
		// Final is the assistant reply returned to the caller.
		Final planner.AgentMessage
		// ToolEvents captures the last set of tool results emitted before completion.
		ToolEvents []planner.ToolResult
		// Notes aggregates planner annotations produced during the final turn.
		Notes []planner.PlannerAnnotation
	}

	// ToolInput is the payload passed to tool executors. All tool types (activity-based,
	// agent-based, MCP, etc.) use this common envelope. Payload is JSON-encoded to maintain
	// flexibility - each executor unmarshals it according to its specific tool schema.
	ToolInput struct {
		// RunID ties the call to a durable workflow execution.
		RunID string
		// AgentID identifies which agent the tool request belongs to.
		AgentID string
		// ToolsetName is the qualified toolset identifier (e.g., "service.toolset_name").
		// Used to lookup the toolset registration and call its Execute function.
		ToolsetName string
		// ToolName is the fully qualified tool identifier (`service.toolset.tool`).
		ToolName tools.Ident
		// ToolCallID is a unique identifier for this specific tool call, used for
		// parent-child tracking and event correlation.
		ToolCallID string
		// Payload is the JSON-encoded argument payload. Each executor unmarshals this
		// according to its tool's schema (activity input, agent args, MCP request, etc.).
		Payload json.RawMessage
		// SessionID groups related runs (e.g., multi-turn conversations).
		SessionID string
		// TurnID identifies the conversational turn for event sequencing.
		TurnID string
		// ParentToolCallID links nested tool calls to their parent (for agent-as-tool).
		ParentToolCallID string
	}

	// ToolOutput is returned by tool executors after invoking the tool implementation.
	// All tool types use this common envelope. Payload is JSON-encoded using the tool-specific
	// codec so callers can decode it back into strong types.
	ToolOutput struct {
		// Payload is the JSON-encoded tool result.
		Payload json.RawMessage
		// Telemetry carries observability metadata collected during execution (timing,
		// tokens, model info, etc.). Nil if no telemetry was collected.
		Telemetry *telemetry.ToolTelemetry
		// Error captures the string form of any tool-level error that should be surfaced
		// to planners without failing the workflow. Empty string indicates success.
		Error string
		// RetryHint forwards structured retry guidance alongside the error when a tool
		// can classify why it failed (invalid arguments, tool unavailable, etc.).
		RetryHint *planner.RetryHint
	}

	// PlanActivityInput carries the data needed to run a planner turn via an activity.
	PlanActivityInput struct {
		// AgentID identifies the agent whose planner should run.
		AgentID string
		// RunID is the durable workflow execution identifier.
		RunID string
		// Messages contains the conversation context supplied to the planner.
		Messages []planner.AgentMessage
		// RunContext carries caps, labels, and attempt metadata for the planner.
		RunContext run.Context
		// ToolResults lists the results since the previous planner turn (empty for PlanStart).
		ToolResults []planner.ToolResult
	}

	// PlanActivityOutput wraps the planner result produced by a plan/resume activity.
	PlanActivityOutput struct {
		// Result is the planner output returned to the workflow loop.
		Result planner.PlanResult
	}

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
		Execute(ctx context.Context, meta ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error)
	}

	// ToolCallExecutorFunc adapts a function to the ToolCallExecutor interface.
	ToolCallExecutorFunc func(ctx context.Context, meta ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error)
)

// Execute calls f(ctx, meta, call).
func (f ToolCallExecutorFunc) Execute(ctx context.Context, meta ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error) {
	return f(ctx, meta, call)
}

// ToolActivityExecutor handles execution of a single tool via workflow
// activities. Implementations decide how to schedule and await activity
// completion while preserving workflow determinism.
type ToolActivityExecutor interface {
	// Execute runs the tool with the given input and returns the result.
	// The workflow context is provided for workflow-level operations (activities,
	// timers, etc.). Input and output use the ToolInput/ToolOutput envelope.
	Execute(ctx context.Context, wfCtx engine.WorkflowContext, input ToolInput) (ToolOutput, error)
}

// Ensure ActivityToolExecutor implements ToolActivityExecutor at compile time.
var _ ToolActivityExecutor = (*ActivityToolExecutor)(nil)

// Execute schedules the tool as a workflow activity and waits for its result.
// This maintains workflow determinism while allowing the tool to run out-of-process.
// The input is passed through to the activity as-is (already properly formatted).
func (e *ActivityToolExecutor) Execute(ctx context.Context, wfCtx engine.WorkflowContext, input ToolInput) (ToolOutput, error) {
	req := engine.ActivityRequest{
		Name:  e.activityName,
		Queue: e.queue,
		Input: input,
	}

	future, err := wfCtx.ExecuteActivityAsync(ctx, req)
	if err != nil {
		return ToolOutput{}, err
	}

	var result ToolOutput
	if err := future.Get(ctx, &result); err != nil {
		return ToolOutput{}, err
	}

	return result, nil
}
