// Package planner defines planner contracts and shared types for agent
// reasoning implementations. Planners are the decision-making core of agents:
// they analyze conversation history, decide which tools to invoke, and generate
// final responses. The runtime invokes planners at workflow decision points
// (start and after each tool execution) and enforces policy constraints on
// their outputs.
package planner

import (
    "context"

    "goa.design/goa-ai/runtime/agent/memory"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/run"
    "goa.design/goa-ai/runtime/agent/telemetry"
    toolerrors "goa.design/goa-ai/runtime/agent/toolerrors"
    "goa.design/goa-ai/runtime/agent/tools"
)

// ToolError represents a structured tool failure and is an alias to the runtime toolerrors type.
type ToolError = toolerrors.ToolError

// NewToolError constructs a ToolError with the provided message.
func NewToolError(message string) *ToolError {
	return toolerrors.New(message)
}

// NewToolErrorWithCause wraps an existing error with a ToolError message.
func NewToolErrorWithCause(message string, cause error) *ToolError {
	return toolerrors.NewWithCause(message, cause)
}

// ToolErrorFromError converts an arbitrary error into a ToolError chain.
func ToolErrorFromError(err error) *ToolError {
	return toolerrors.FromError(err)
}

// ToolErrorf formats according to a format specifier and returns the string as a ToolError.
func ToolErrorf(format string, args ...any) *ToolError {
	return toolerrors.Errorf(format, args...)
}

// RetryReason categorizes the type of failure that triggered a retry hint.
// Policy engines use this to make informed decisions about retry strategies
// (e.g., disable tools, adjust caps, request human intervention).
type RetryReason string

const (
	// RetryReasonInvalidArguments indicates the tool call failed due to invalid
	// or malformed input arguments (schema violation, type mismatch, etc.).
	RetryReasonInvalidArguments RetryReason = "invalid_arguments"

	// RetryReasonMissingFields indicates required fields were missing or empty
	// in the tool call payload. The planner may populate MissingFields to specify
	// which fields are needed.
	RetryReasonMissingFields RetryReason = "missing_fields"

	// RetryReasonMalformedResponse indicates the tool returned data that couldn't
	// be parsed or didn't match the expected schema (e.g., invalid JSON).
	RetryReasonMalformedResponse RetryReason = "malformed_response"

	// RetryReasonTimeout indicates the tool execution exceeded time limits.
	// Policy engines may reduce caps or disable the tool for this run.
	RetryReasonTimeout RetryReason = "timeout"

	// RetryReasonRateLimited indicates the tool or underlying service is rate-limited.
	// Policy engines may back off or disable the tool temporarily.
	RetryReasonRateLimited RetryReason = "rate_limited"

	// RetryReasonToolUnavailable indicates the tool is temporarily or permanently
	// unavailable (service down, not configured, etc.).
	RetryReasonToolUnavailable RetryReason = "tool_unavailable"
)

// Planner defines the contract generated workflows expect planner implementations
// to fulfill. Planners receive conversation history and runtime context, then
// decide whether to request tool invocations or produce a final response.
// Implementations typically wrap LLM clients (via model.Client) and orchestrate
// prompt engineering, tool selection, and response generation.
//
// The runtime calls PlanStart when a workflow begins and PlanResume after each
// batch of tool executions completes. Planners must be stateless; per-run state
// is managed by the runtime via AgentContext.State().
type Planner interface {
	// PlanStart initiates reasoning for a new workflow run. The planner receives
	// the initial messages and context, typically involving a system prompt and
	// user input. Returns either tool calls to execute or a final response.
	// Returns an error if the planner encounters a fatal issue (LLM unavailable,
	// invalid input, etc.).
	PlanStart(ctx context.Context, input PlanInput) (PlanResult, error)

	// PlanResume continues reasoning after tool execution. The planner receives
	// the conversation history plus tool results from the previous turn. It should
	// integrate tool outputs and decide the next action (more tools or final response).
	// Returns an error if the planner cannot continue (LLM failure, context too large).
	PlanResume(ctx context.Context, input PlanResumeInput) (PlanResult, error)
}

type (
	// PlanInput contains the information provided to the planner when a run begins.
	// The runtime constructs this from the workflow input and passes it to PlanStart.
	PlanInput struct {
		// Messages is the conversation history provided at run start, typically including
		// the system prompt (if any) and initial user message. Planners use this as the
		// basis for reasoning and tool selection.
		Messages []AgentMessage

		// RunContext carries identifiers, labels, and caps for the run. Planners can
		// inspect labels for routing decisions or use caps to understand resource limits.
		RunContext run.Context

    // Agent provides access to runtime services (memory, models, telemetry).
    Agent PlannerContext

    // Events provides streaming callbacks to emit assistant chunks, planner thoughts,
    // and usage deltas during provider streaming. Implemented by the runtime and not
    // serialized across workflow/activity boundaries.
    Events PlannerEvents
	}

	// PlanResumeInput contains the information provided to the planner when resuming
	// after tool execution. This extends PlanInput with tool results from the previous turn.
	PlanResumeInput struct {
		// Messages is the conversation history available at resume time, updated to include
		// any new assistant messages or user inputs since the last planner call.
		Messages []AgentMessage

		// RunContext carries identifiers, labels, and caps for the run.
		RunContext run.Context

    // Agent provides access to runtime services (memory, models, telemetry).
    Agent PlannerContext

    // Events provides streaming callbacks to emit assistant chunks, planner thoughts,
    // and usage deltas during provider streaming.
    Events PlannerEvents

		// ToolResults lists the results of the tools executed since the previous planner
		// call. Planners integrate these results (successes and failures) into their
		// reasoning to decide the next action.
		ToolResults []ToolResult
	}

	// PlanResult communicates the planner's decision: either request more tool executions
	// or produce a final response. Exactly one of ToolCalls or FinalResponse should be
	// populated (not both, not neither).
	PlanResult struct {
		// ToolCalls enumerates tool invocations to schedule next. Empty if FinalResponse
		// is set. The runtime validates these against the policy allowlist and executes
		// them (subject to caps).
		ToolCalls []ToolRequest

		// FinalResponse holds the assistant message when the planner decides to terminate
		// the run. Nil if ToolCalls is non-empty. The runtime returns this to the caller.
		FinalResponse *FinalResponse

		// Notes carries optional planner annotations (thoughts, reasoning steps) persisted
		// to memory and propagated to hooks. Empty if the planner doesn't emit annotations.
		Notes []PlannerAnnotation

		// RetryHint allows the planner to influence retry policies after failures (e.g.,
		// disable a failing tool, adjust caps). Nil if no policy changes are suggested.
		RetryHint *RetryHint

		// ExpectedChildren indicates how many child tool calls are expected to be discovered
		// by the tools in this batch. Used for progress tracking in streaming scenarios where
		// tools can dynamically request additional tools (e.g., search → fetch papers).
		// A value of 0 means no children are expected or the planner doesn't track this.
		ExpectedChildren int

		// Await requests the runtime to pause and wait for external input before
		// continuing the plan loop. Exactly one of Clarification or ExternalTools
		// should be set when Await is non-nil. When Await is set, ToolCalls and
		// FinalResponse must be empty.
		Await *Await
	}

	// Await describes a typed external continuation request. The runtime pauses
	// the run and emits an await event; callers can satisfy the request via the
	// runtime Provide APIs (ProvideClarification/ProvideToolResults).
	Await struct {
		// Clarification asks for a human-provided answer before continuing.
		Clarification *ClarificationRequest
		// ExternalTools declares a set of tool calls that will be fulfilled by
		// an external system; the runtime waits for their results to be provided.
		ExternalTools *ExternalToolsRequest
	}

	// ClarificationRequest models a human-in-the-loop pause for missing info or approval.
	ClarificationRequest struct {
		// ID correlates the await with a later ProvideClarification.
		ID string
		// Question is the prompt to present to the user.
		Question string
		// MissingFields optionally lists fields needed to proceed.
		MissingFields []string
		// RestrictToTool optionally narrows the next turn to a specific tool.
		RestrictToTool tools.Ident
		// ExampleInput optionally provides a schema-compliant example.
		ExampleInput map[string]any
	}

	// ExternalToolsRequest models a pause while external systems execute tools.
	ExternalToolsRequest struct {
		// ID correlates the await with a later ProvideToolResults.
		ID string
		// Items enumerate the external tool calls to be satisfied.
		Items []AwaitTool
	}

	// AwaitTool describes a single external tool call to be executed out-of-band.
	AwaitTool struct {
		Name       tools.Ident
		Payload    any
		ToolCallID string
	}

	// ToolRequest schedules a single tool invocation. The runtime validates the
	// tool name against the allowlist and marshals the payload for execution.
	ToolRequest struct {
		// Name identifies the tool to execute (e.g., "service.toolset.tool"). Must match
		// a registered tool in the agent's toolset.
		Name tools.Ident

		// Payload is the tool-specific argument payload, typically a map[string]any or
		// struct matching the tool's input schema. The runtime serializes this for
		// activity execution.
		Payload any

		// ParentToolCallID optionally identifies the tool call that requested this tool
		// to be invoked. Used to track parent-child relationships in multi-stage workflows
		// where one tool's result triggers additional tool calls (e.g., search → fetch).
		// Empty if this is a top-level tool call directly requested by the planner.
		ParentToolCallID string

		// ToolCallID is assigned by the runtime when the tool call is scheduled. It
		// uniquely identifies this specific invocation for correlation (e.g., linking
		// ToolCallUpdated events to the parent tool). Planners typically leave this
		// empty; the runtime populates it automatically.
		ToolCallID string

		// RunID identifies the run making this tool call. Populated by the runtime
		// from ToolInput. Agent-tools use this to construct hierarchical nested run IDs
		// (e.g., "parent-run/agent/tool-name") while regular tools use it for logging.
		RunID string

		// SessionID is the session this tool call belongs to. Agent-tools propagate this
		// to nested agents so conversation context spans agent boundaries.
		SessionID string

		// TurnID is the conversational turn this tool call is part of. Agent-tools
		// propagate this for consistent event sequencing across nested executions.
		TurnID string
	}

	// FinalResponse captures the assistant reply when the planner chooses to terminate
	// the run. This is returned to the workflow caller as the final output.
	FinalResponse struct {
		// Message is the assistant response text returned to the caller. This typically
		// answers the user's query or provides the final agent output.
		Message AgentMessage

		// Structured optionally provides typed data (e.g., JSON schema outputs, Pydantic
		// models). Nil if the response is purely textual. Used for structured generation
		// use cases.
		Structured any
	}

	// PlannerAnnotation represents optional notes or reasoning steps emitted by the
	// planner. These are persisted into memory and propagated to hooks for observability.
	PlannerAnnotation struct {
		// Text is the planner-provided note (e.g., "Calling search to find recent news").
		Text string

		// Labels carries metadata associated with the note for filtering or categorization
		// (e.g., {"type": "reasoning"}, {"confidence": "low"}).
		Labels map[string]string
	}

	// RetryHint communicates planner guidance after failures so the runtime can adjust
	// caps, allowlists, or prompt for user intervention. The policy engine uses this
	// to decide whether to retry with modified constraints (e.g., restrict to one tool,
	// adjust caps, request clarification). Planners populate this when tool calls fail
	// or when they detect recoverable issues.
	RetryHint struct {
		// Reason categorizes the failure that triggered this hint (e.g., invalid_arguments,
		// missing_fields). Policy engines use this to select appropriate recovery strategies.
		// Required field.
		Reason RetryReason

		// Tool identifies the tool involved in the failure (e.g., "search", "calculate").
		// Required field. Policy engines use this to target tool-specific mitigations.
		Tool tools.Ident

		// RestrictToTool signals the policy engine should allow only this tool on the
		// next turn, implementing a circuit breaker pattern for other tools. This prevents
		// the planner from repeating the same error with different tools.
		RestrictToTool bool

		// MissingFields lists specific required fields that were missing or invalid in
		// the tool call. Policy engines can surface these to planners (via prompts) or
		// user interfaces (when InterruptsAllowed is true). Empty if not applicable.
		MissingFields []string

		// ExampleInput provides a correctly formatted example for the planner to reference
		// on retry. This helps the planner understand the expected schema and correct
		// common mistakes. Nil if no example is available.
		ExampleInput map[string]any

		// PriorInput captures the input that failed validation, allowing planners to
		// compare correct vs. incorrect formats when reasoning about the retry. Nil if
		// not applicable or if exposing the prior input isn't useful.
		PriorInput map[string]any

		// ClarifyingQuestion provides a human-readable prompt for user interaction when
		// human-in-the-loop is needed (e.g., InterruptsAllowed is true and the planner
		// cannot proceed without additional information). Empty if not applicable.
		ClarifyingQuestion string

		// Message provides human-readable guidance for debugging, logging, and user-facing
		// error messages (e.g., "Tool returned malformed JSON; suggest disabling for this
		// run"). Policy engines can log this or surface it to users.
		Message string
	}

	// ToolResult summarizes the outcome of a tool call provided back to the planner.
	// The runtime populates these after executing tool activities and passes them to
	// PlanResume for integration into the next reasoning turn.
	ToolResult struct {
		// Name identifies the tool that was executed (matches ToolRequest.Name).
		Name tools.Ident

		// Result carries the tool result payload if successful (e.g., search results,
		// calculation output). Nil if Error is set.
		Result any

		// ToolCallID echoes the identifier of the tool invocation as known to the
		// planner/model. When the planner supplied ToolRequest.ToolCallID (e.g., from
		// a model tool_call.id), the runtime preserves it and returns it here so
		// planners can correlate results back to the model. When the planner omitted
		// an ID, the runtime assigns a deterministic ID and returns it here.
		ToolCallID string

		// Error contains the error returned by the tool execution, if any. Nil on success.
		// Planners should handle errors gracefully (retry, fallback, or report to user).
		Error error

		// RetryHint carries structured guidance directly from the tool execution when
		// available (e.g., invalid arguments, tool unavailable). Planners can leverage
		// this hint to adjust prompts or restrict future tool usage without parsing
		// free-form error strings.
		RetryHint *RetryHint

		// Telemetry holds structured observability metadata gathered during execution
		// (duration, token counts, model info). Planners typically ignore this; it's
		// primarily for metrics, cost tracking, and observability systems.
		Telemetry *telemetry.ToolTelemetry
	}

	// AgentMessage mirrors chat content exchanged between user and assistant. These
	// are the building blocks of conversation history passed to and produced by planners.
	AgentMessage struct {
		// Role indicates who produced the message: "user" (end-user input), "assistant"
		// (agent response), or "system" (instructions/context).
		Role string

		// Content is the textual payload of the message. For user/system messages, this
		// is the input. For assistant messages, this is the generated response.
		Content string

		// Meta contains optional structured metadata about the message (e.g., message IDs,
		// timestamps, citations). Planners may populate this for structured generation.
		Meta map[string]any
	}

    // PlannerContext gives planners access to runtime services (memory, models, logging)
    // and per-run state. Implementations are provided by the runtime and scoped to the
    // current workflow execution. This interface is read-only from the planner's
    // perspective; streaming callbacks live on PlannerEvents.
    PlannerContext interface {
        // ID returns the agent identifier (e.g., "weather_assistant").
        ID() string

		// RunID returns the workflow run identifier for this execution.
		RunID() string

        // Memory returns a reader for querying the agent's persistent history. Planners use
        // this to look up prior turns, tool results, or annotations.
        Memory() memory.Reader

        // ModelClient returns the model client for the given ID. The boolean indicates
        // whether the client exists. Planners use this to invoke LLMs for reasoning.
        ModelClient(id string) (model.Client, bool)

        // Logger returns a logger scoped to this workflow execution.
        Logger() telemetry.Logger

		// Metrics returns a metrics recorder for emitting planner-scoped metrics.
		Metrics() telemetry.Metrics

		// Tracer returns a tracer for creating spans within planner logic.
		Tracer() telemetry.Tracer

		// State returns mutable per-run state storage. Planners use this to persist
		// ephemeral data (conversation context, partial results) across PlanStart/PlanResume
		// calls within a single run.
        State() AgentState
    }

    // PlannerEvents exposes streaming callbacks used by streaming planners to forward
    // assistant chunks, planner thoughts and usage deltas to the runtime. The runtime
    // adapts these into stream and hook events. Implementations must be non-blocking.
    PlannerEvents interface {
        AssistantChunk(ctx context.Context, text string)
        PlannerThought(ctx context.Context, note string, labels map[string]string)
        UsageDelta(ctx context.Context, usage model.TokenUsage)
    }

	// AgentState exposes mutable per-run planner state managed by the runtime. This
	// allows planners to store ephemeral data that doesn't belong in durable memory
	// (e.g., conversation summaries, intermediate reasoning, retry counters).
	AgentState interface {
		// Get retrieves the value for the given key. The boolean indicates whether the
		// key exists. Returns (nil, false) if the key is not set.
		Get(key string) (any, bool)

		// Set stores a value for the given key, overwriting any existing value. Values
		// are scoped to the current run and cleared when the workflow completes.
		Set(key string, value any)

		// Keys returns all currently set keys in the state. Useful for debugging or
		// iterating over stored data.
		Keys() []string
	}
)
