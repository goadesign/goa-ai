// Package planner defines the contracts between user-provided planners and the
// goa-ai runtime. Planners are the decision-makers in agent execution: they
// analyze conversation history and either request tool calls or produce a final
// assistant response.
//
// The runtime invokes planners through two entry points:
//   - PlanStart: Called once at run start with the initial messages
//   - PlanResume: Called after each batch of tool calls with their results
//
// Planners have read-only access to runtime services (memory, models, telemetry)
// through PlannerContext, and can emit streaming events through PlannerEvents.
// The runtime handles workflow orchestration, policy enforcement, and tool
// execution; planners focus purely on decision-making.
//
// Implementing a Planner:
//
//	type MyPlanner struct{}
//
//	func (p *MyPlanner) PlanStart(ctx context.Context, input *PlanInput) (*PlanResult, error) {
//	    // Analyze input.Messages and decide:
//	    // - Return tool calls: &PlanResult{ToolCalls: [...]}
//	    // - Return final answer: &PlanResult{FinalResponse: &FinalResponse{...}}
//	    // - Request clarification: &PlanResult{Await: &Await{Clarification: ...}}
//	}
//
//	func (p *MyPlanner) PlanResume(ctx context.Context, input *PlanResumeInput) (*PlanResult, error) {
//	    // Process input.ToolResults and decide next step
//	    // The Finalize field is non-nil when runtime forces termination
//	}
package planner

import (
	"context"
	"encoding/json"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Planner is the core decision-making interface for agents. Each agent has exactly
// one planner that determines how the agent responds to user messages.
//
// The contract is deliberately simple: receive context, return a decision. The
// runtime handles everything else—workflow execution, tool scheduling, policy
// enforcement, memory persistence, and event streaming.
//
// Thread safety: A single Planner instance may be invoked concurrently for
// different runs. Implementations must be safe for concurrent use. Avoid
// storing per-run state in the Planner struct; use PlannerContext.State() for
// ephemeral per-run data if needed.
//
// Error handling: Errors returned from PlanStart or PlanResume terminate the
// run with a failed status. Use RetryHint in PlanResult to communicate
// recoverable failures (like validation errors) without terminating.
type Planner interface {
	// PlanStart receives the initial messages and returns the first decision.
	// This is called exactly once at the start of each run.
	PlanStart(ctx context.Context, input *PlanInput) (*PlanResult, error)

	// PlanResume receives messages plus tool results from the previous turn.
	// This is called after each batch of tool executions until the planner
	// returns a FinalResponse or the runtime terminates due to policy limits.
	// When the runtime forces termination (caps exhausted, time budget expired),
	// the Finalize field is set and the planner should produce a final response.
	PlanResume(ctx context.Context, input *PlanResumeInput) (*PlanResult, error)
}

// PlannerContext exposes runtime services to planners.
type PlannerContext interface {
	ID() agent.Ident
	RunID() string
	Memory() memory.Reader
	Logger() telemetry.Logger
	Metrics() telemetry.Metrics
	Tracer() telemetry.Tracer
	State() AgentState
	ModelClient(id string) (model.Client, bool)
	// AddReminder registers or updates a run-scoped system reminder. Planners use
	// this to surface structured, rate-limited guidance (for example, “review
	// open todos”) without baking prompt text directly into planner logic.
	AddReminder(r reminder.Reminder)
	// RemoveReminder clears a previously registered reminder by ID. Planners call
	// this when the conditions for a reminder no longer hold so future turns and
	// prompts stop surfacing outdated guidance.
	RemoveReminder(id string)
}

// AgentState provides ephemeral, per-run state storage for planners.
type AgentState interface {
	Get(key string) (any, bool)
	Set(key string, value any)
	Keys() []string
}

// PlannerEvents allows planners to emit streaming updates that the runtime
// captures in its provider ledger and publishes to subscribers.
type PlannerEvents interface {
	AssistantChunk(ctx context.Context, text string)
	PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart)
	PlannerThought(ctx context.Context, note string, labels map[string]string)
	UsageDelta(ctx context.Context, usage model.TokenUsage)
}

// ToolRequest describes a tool invocation requested by the planner.
type ToolRequest struct {
	Name             tools.Ident
	Payload          json.RawMessage
	AgentID          agent.Ident
	RunID            string
	SessionID        string
	TurnID           string
	ToolCallID       string
	ParentToolCallID string
}

// Artifact carries non-model data produced alongside a tool result.
// Artifacts are not sent to model providers; they are surfaced to
// hooks, streams, and UIs for rich visualization and provenance.
type Artifact struct {
	// Kind identifies the logical shape of this artifact
	// (for example, "atlas.time_series" or "atlas.control_narrative").
	// UIs dispatch renderers based on Kind.
	Kind string
	// Data contains the artifact payload. It must be JSON-serializable.
	Data any
	// SourceTool is the fully-qualified tool identifier that produced
	// this artifact. It is used for provenance and debugging.
	SourceTool tools.Ident
	// RunLink links this artifact to a nested agent run when it was
	// produced by an agent-as-tool. Nil for service-backed tools.
	RunLink *run.Handle
}

// ToolResult captures the outcome of a tool invocation.
type ToolResult struct {
	Name   tools.Ident
	Result any
	// Artifacts carries non-model data produced alongside the tool
	// result (for example, UI artifacts or policy annotations). Artifacts
	// are never sent to model providers.
	Artifacts []*Artifact
	// Bounds, when non-nil, describes how the result has been bounded relative
	// to the full underlying data set (for example, list/window/graph caps).
	// Tool implementations and adapters populate this field; the runtime and
	// sinks surface it but never mutate or derive it.
	Bounds        *agent.Bounds
	Error         *ToolError
	RetryHint     *RetryHint
	Telemetry     *telemetry.ToolTelemetry
	ToolCallID    string
	ChildrenCount int
	// RunLink, when non-nil, links this result to a nested agent run that
	// was executed as an agent-as-tool. For service-backed tools this field
	// is nil. Callers can use RunLink to subscribe to or display the child
	// agent run separately from the parent tool call.
	RunLink *run.Handle
}

// RetryHint communicates planner guidance after tool failures so policy engines
// and UIs can react. See policy.RetryHint for the runtime-converted form.
type RetryHint struct {
	Reason             RetryReason
	Tool               tools.Ident
	RestrictToTool     bool
	MissingFields      []string
	ExampleInput       map[string]any
	PriorInput         map[string]any
	ClarifyingQuestion string
	Message            string
}

// FinalResponse contains the assistant message that concludes the run.
type FinalResponse struct {
	Message *model.Message
}

// PlannerAnnotation is a free-form planner note with optional labels.
type PlannerAnnotation struct {
	Text   string
	Labels map[string]string
}

// Await describes a pause point awaiting user/system input.
type Await struct {
	Clarification *AwaitClarification
	ExternalTools *AwaitExternalTools
}

// AwaitClarification requests missing information from the user.
type AwaitClarification struct {
	ID               string
	Question         string
	MissingFields    []string
	RestrictToTool   tools.Ident
	ExampleInput     map[string]any
	ClarifyingPrompt string
}

// AwaitExternalTools requests external tool results (provided out-of-band).
type AwaitExternalTools struct {
	ID    string
	Items []AwaitToolItem
}

// AwaitToolItem describes one requested external tool call.
type AwaitToolItem struct {
	Name       tools.Ident
	ToolCallID string
	Payload    json.RawMessage
}

// TerminationReason indicates why the runtime forced finalization.
type TerminationReason string

const (
	TerminationReasonTimeBudget TerminationReason = "time_budget"
	TerminationReasonToolCap    TerminationReason = "tool_cap"
	TerminationReasonFailureCap TerminationReason = "failure_cap"
)

// Termination carries a runtime-initiated finalize request.
type Termination struct {
	Reason  TerminationReason
	Message string
}

// PlanInput carries the initial messages and context into PlanStart.
type PlanInput struct {
	Messages   []*model.Message
	RunContext run.Context
	Agent      PlannerContext
	Events     PlannerEvents
	// Reminders contains the active system reminders for this planner turn.
	// Callers should treat this slice as read-only and rely on
	// PlannerContext.AddReminder to register new reminders for future turns.
	Reminders []reminder.Reminder
}

// PlanResumeInput carries messages plus recent tool results into PlanResume.
type PlanResumeInput struct {
	Messages    []*model.Message
	RunContext  run.Context
	Agent       PlannerContext
	Events      PlannerEvents
	ToolResults []*ToolResult
	Finalize    *Termination
	// Reminders contains the active system reminders for this planner turn.
	Reminders []reminder.Reminder
}

// PlanResult is the planner's decision for the next step.
type PlanResult struct {
	ToolCalls     []ToolRequest
	FinalResponse *FinalResponse
	// Streamed reports whether assistant text for this result has already been
	// streamed via PlannerEvents.AssistantChunk. When true, runtimes should
	// avoid emitting an additional full AssistantMessageEvent for the
	// FinalResponse to prevent duplicate assistant messages.
	Streamed         bool
	Await            *Await
	RetryHint        *RetryHint
	ExpectedChildren int
	Notes            []PlannerAnnotation
}
