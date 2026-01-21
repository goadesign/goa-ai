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
//	    // - Request external input: &PlanResult{Await: NewAwait(AwaitClarificationItem(...))}
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
	// ID returns the agent identifier for the run currently being planned.
	ID() agent.Ident

	// RunID returns the run identifier for the run currently being planned.
	RunID() string

	// Memory returns a read-only view of the configured memory store.
	Memory() memory.Reader

	// Logger returns the run-scoped logger.
	Logger() telemetry.Logger

	// Metrics returns the metrics emitter for the run.
	Metrics() telemetry.Metrics

	// Tracer returns the distributed tracer for the run.
	Tracer() telemetry.Tracer

	// State returns ephemeral per-run storage for planner-local state.
	State() AgentState

	// ModelClient returns the model client configured for the given model ID.
	// The boolean result is false when the requested model is not configured.
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
	// Get returns the value stored under key, if any.
	Get(key string) (any, bool)

	// Set stores value under key for the duration of the run.
	Set(key string, value any)

	// Keys returns all currently stored keys.
	Keys() []string
}

// PlannerEvents allows planners to emit streaming updates that the runtime
// captures in its provider ledger and publishes to subscribers.
type PlannerEvents interface {
	// AssistantChunk emits an assistant text delta. Use this for incremental
	// streaming output instead of returning a full FinalResponse at the end.
	AssistantChunk(ctx context.Context, text string)

	// PlannerThinkingBlock emits a structured thinking block (for models that
	// support rich thinking parts) for debugging/trace visibility.
	PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart)

	// PlannerThought emits a planner note with optional labels for debugging.
	PlannerThought(ctx context.Context, note string, labels map[string]string)

	// UsageDelta reports incremental token usage for the current planning phase.
	UsageDelta(ctx context.Context, usage model.TokenUsage)
}

// ToolRequest describes a tool invocation requested by the planner.
type ToolRequest struct {
	// Name is the fully-qualified tool identifier (for example, "atlas.read.get_time_series").
	Name tools.Ident

	// Payload is the canonical JSON payload for the tool call.
	Payload json.RawMessage

	// ArtifactsMode is the normalized per-call artifacts toggle selected by the
	// caller (typically the model) via the reserved `artifacts` payload field.
	// Valid values are tools.ArtifactsModeAuto, tools.ArtifactsModeOn, and
	// tools.ArtifactsModeOff. When empty, the caller did not specify a mode.
	ArtifactsMode tools.ArtifactsMode

	// AgentID is the identifier of the agent that issued this tool request.
	AgentID agent.Ident

	// RunID is the identifier of the run that owns this tool call.
	RunID string

	// SessionID is the logical session identifier (for example, a chat conversation).
	SessionID string

	// TurnID identifies the conversational turn that produced this tool call.
	TurnID string

	// ToolCallID uniquely identifies this tool invocation for correlation across events.
	ToolCallID string

	// ParentToolCallID is the identifier of the parent tool call when this invocation
	// is nested (for example, a tool launched by an agent-as-tool).
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
	// Name is the fully-qualified tool identifier that produced this result.
	Name tools.Ident

	// Result is the decoded tool result value. Its concrete type depends on the
	// tool's result schema and codec.
	Result any

	// Artifacts carries non-model data produced alongside the tool
	// result (for example, UI artifacts or policy annotations). Artifacts
	// are never sent to model providers.
	Artifacts []*Artifact

	// Bounds, when non-nil, describes how the result has been bounded relative
	// to the full underlying data set (for example, list/window/graph caps).
	// Tool implementations and adapters populate this field; the runtime and
	// sinks surface it but never mutate or derive it.
	Bounds *agent.Bounds

	// Error is the structured tool error, when the tool execution failed.
	Error *ToolError

	// RetryHint is optional structured guidance for recovering from tool failures.
	RetryHint *RetryHint

	// Telemetry contains tool execution metrics (duration, token usage, model).
	Telemetry *telemetry.ToolTelemetry

	// ToolCallID is the correlation identifier for this tool invocation.
	ToolCallID string

	// ChildrenCount records how many nested tool results were observed when this
	// result came from an agent-as-tool execution.
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
	// Reason classifies the retry hint for policy/UX decisions.
	Reason RetryReason

	// Tool is the tool identifier associated with this hint.
	Tool tools.Ident

	// RestrictToTool instructs callers to retry only the specified tool.
	RestrictToTool bool

	// MissingFields lists required fields that were missing or invalid.
	MissingFields []string

	// ExampleInput is an example payload (as a JSON object) to guide callers.
	ExampleInput map[string]any

	// PriorInput is the payload (as a JSON object) that caused the failure when
	// available, to assist interactive repair flows.
	PriorInput map[string]any

	// ClarifyingQuestion is a natural-language question to ask the user to obtain
	// the missing or corrected information.
	ClarifyingQuestion string

	// Message is an optional additional explanation for the caller.
	Message string
}

// FinalResponse contains the assistant message that concludes the run.
type FinalResponse struct {
	// Message is the assistant message returned to the user.
	Message *model.Message
}

// PlannerAnnotation is a free-form planner note with optional labels.
type PlannerAnnotation struct {
	// Text is the note content.
	Text string

	// Labels are optional structured tags for tooling/debugging.
	Labels map[string]string
}

// Await describes one or more external-input prompts that must be satisfied
// before the runtime resumes planning.
//
// Contract:
//   - Await is a single barrier per planner result: the runtime pauses once for
//     the full await set.
//   - Await.Items preserves order. Callers may present items as a wizard; the
//     runtime resumes planning only after all items are satisfied.
//   - Items may mix kinds (clarification, questions, external tools).
type Await struct {
	Items []AwaitItem
}

// AwaitItemKind identifies the kind of external input required.
type AwaitItemKind string

const (
	AwaitItemKindClarification AwaitItemKind = "clarification"
	AwaitItemKindQuestions     AwaitItemKind = "questions"
	AwaitItemKindExternalTools AwaitItemKind = "external_tools"
)

// AwaitItem describes one external-input prompt.
//
// Exactly one payload field must be set and must match Kind.
type AwaitItem struct {
	Kind AwaitItemKind

	Clarification *AwaitClarification
	Questions     *AwaitQuestions
	ExternalTools *AwaitExternalTools
}

// NewAwait constructs an Await barrier with items in the given order.
func NewAwait(items ...AwaitItem) *Await {
	return &Await{Items: items}
}

// AwaitClarificationItem constructs a clarification await item.
func AwaitClarificationItem(c *AwaitClarification) AwaitItem {
	return AwaitItem{Kind: AwaitItemKindClarification, Clarification: c}
}

// AwaitQuestionsItem constructs a questions await item.
func AwaitQuestionsItem(q *AwaitQuestions) AwaitItem {
	return AwaitItem{Kind: AwaitItemKindQuestions, Questions: q}
}

// AwaitExternalToolsItem constructs an external-tools await item.
func AwaitExternalToolsItem(e *AwaitExternalTools) AwaitItem {
	return AwaitItem{Kind: AwaitItemKindExternalTools, ExternalTools: e}
}

// AwaitClarification requests missing information from the user.
type AwaitClarification struct {
	// ID uniquely identifies this clarification request.
	ID string

	// Question is the user-facing question to ask.
	Question string

	// MissingFields lists missing or invalid fields the user must supply.
	MissingFields []string

	// RestrictToTool optionally binds the clarification to a single tool.
	RestrictToTool tools.Ident

	// ExampleInput is an example payload (as a JSON object) to guide the user.
	ExampleInput map[string]any

	// ClarifyingPrompt is an optional prompt to use when building follow-up messages.
	ClarifyingPrompt string
}

// AwaitQuestions requests structured multiple-choice answers from the user.
//
// Contract: AwaitQuestions represents a single paused tool invocation that must
// be satisfied out-of-band by the caller (typically a UI) and resumed via the
// runtime's ProvideToolResults mechanism using ToolCallID.
type AwaitQuestions struct {
	// ID uniquely identifies this questions request.
	ID string

	// ToolName identifies the tool awaiting user answers (for example, "chat.ask_question.ask_question").
	ToolName tools.Ident

	// ToolCallID correlates the provided result with this requested call.
	ToolCallID string

	// Payload is the canonical JSON payload for the awaited tool call.
	Payload json.RawMessage

	// Title is an optional display title for the questions form.
	Title *string

	// Questions enumerates the questions to present to the user.
	Questions []AwaitQuestion
}

// AwaitQuestion describes a single multiple-choice question.
type AwaitQuestion struct {
	// ID uniquely identifies this question within the prompt.
	ID string

	// Prompt is the user-facing question text.
	Prompt string

	// Options enumerates the selectable answers.
	Options []AwaitQuestionOption

	// AllowMultiple reports whether multiple options may be selected.
	AllowMultiple bool
}

// AwaitQuestionOption describes a selectable answer option for a question.
type AwaitQuestionOption struct {
	// ID uniquely identifies this option within the question.
	ID string

	// Label is the user-facing option label.
	Label string
}

// AwaitExternalTools requests external tool results (provided out-of-band).
type AwaitExternalTools struct {
	// ID uniquely identifies this external-tools request.
	ID string

	// Items describes the tool calls that the caller must satisfy.
	Items []AwaitToolItem
}

// AwaitToolItem describes one requested external tool call.
type AwaitToolItem struct {
	// Name is the tool identifier to invoke externally.
	Name tools.Ident

	// ToolCallID correlates the provided result with this requested call.
	ToolCallID string

	// Payload is the canonical JSON payload for the external tool call.
	Payload json.RawMessage
}

// TerminationReason indicates why the runtime forced finalization.
type TerminationReason string

const (
	// TerminationReasonTimeBudget indicates the run exceeded its time budget.
	TerminationReasonTimeBudget TerminationReason = "time_budget"

	// TerminationReasonAwaitTimeout indicates the run timed out while awaiting
	// external input (e.g., user clarification, confirmations, or external tool results).
	TerminationReasonAwaitTimeout TerminationReason = "await_timeout"

	// TerminationReasonToolCap indicates the run exceeded its allowed tool call count.
	TerminationReasonToolCap TerminationReason = "tool_cap"

	// TerminationReasonFailureCap indicates the run exceeded its allowed consecutive failure count.
	TerminationReasonFailureCap TerminationReason = "failure_cap"
)

// Termination carries a runtime-initiated finalize request.
type Termination struct {
	// Reason explains which policy cap triggered termination.
	Reason TerminationReason

	// Message is optional additional context suitable for logging or diagnostics.
	Message string
}

// PlanInput carries the initial messages and context into PlanStart.
type PlanInput struct {
	// Messages is the full conversation history at run start.
	Messages []*model.Message

	// RunContext contains durable identifiers and links for the run.
	RunContext run.Context

	// Agent provides access to runtime services (models, memory, telemetry).
	Agent PlannerContext

	// Events allows planners to emit streaming updates.
	Events PlannerEvents

	// Reminders contains the active system reminders for this planner turn.
	// Callers should treat this slice as read-only and rely on
	// PlannerContext.AddReminder to register new reminders for future turns.
	Reminders []reminder.Reminder
}

// PlanResumeInput carries messages plus recent tool results into PlanResume.
type PlanResumeInput struct {
	// Messages is the full conversation history including the most recent tool_use/tool_result blocks.
	Messages []*model.Message

	// RunContext contains durable identifiers and links for the run.
	RunContext run.Context

	// Agent provides access to runtime services (models, memory, telemetry).
	Agent PlannerContext

	// Events allows planners to emit streaming updates.
	Events PlannerEvents

	// ToolResults are the results produced by the previous tool batch.
	ToolResults []*ToolResult

	// Finalize is non-nil when the runtime forces termination and requests a final response.
	Finalize *Termination

	// Reminders contains the active system reminders for this planner turn.
	Reminders []reminder.Reminder
}

// PlanResult is the planner's decision for the next step.
type PlanResult struct {
	// ToolCalls are the tool invocations the runtime should execute next.
	ToolCalls []ToolRequest

	// FinalResponse ends the run with a final assistant message.
	FinalResponse *FinalResponse

	// Streamed reports whether assistant text for this result has already been
	// streamed via PlannerEvents.AssistantChunk. When true, runtimes should
	// avoid emitting an additional full AssistantMessageEvent for the
	// FinalResponse to prevent duplicate assistant messages.
	Streamed bool

	// Await requests the runtime to pause the run and wait for additional input.
	Await *Await

	// RetryHint provides structured guidance for recovering from failures without terminating.
	RetryHint *RetryHint

	// ExpectedChildren is an optional hint for how many nested tool results a planner expects.
	ExpectedChildren int

	// Notes are optional planner annotations surfaced to subscribers.
	Notes []PlannerAnnotation
}
