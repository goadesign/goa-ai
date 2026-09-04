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
//	    // Process input.ToolOutputs and decide next step
//	    // The Finalize field is non-nil when runtime forces termination
//	}
package planner

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
// Retry safety: PlanStart and PlanResume are activity handlers. A workflow has
// one logical call per planner turn, but an engine may execute that call more
// than once under its retry policy. Implementations must not perform
// non-idempotent side effects outside runtime-owned planner output.
//
// Error handling: Planner errors end the run with a failed status. Return
// NewOutputContractError when a planner value does not follow its required
// rules; Temporal will not ask for the same value again. Network and
// model-provider failures keep their existing retry behavior. If
// PlanStart runs out of time, the runtime calls PlanResume once to finish the
// run. A failed tool returns ToolFailure in its ToolResult, and the runtime
// applies the next action stated by that failure.
type Planner interface {
	// PlanStart receives the initial messages and returns the first decision.
	// This is one logical call at the start of each run; its activity may retry.
	PlanStart(ctx context.Context, input *PlanInput) (*PlanResult, error)

	// PlanResume receives messages plus tool results from the previous turn.
	// This is called after each batch of tool executions until the planner
	// returns a FinalResponse or the runtime terminates due to policy limits.
	// When the runtime forces termination (caps exhausted, time budget expired),
	// the Finalize field is set and the planner should produce a final response.
	// A time-budget finalization may be the first completed planner turn when
	// PlanStart exhausted its activity deadline.
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

	// AdvertisedToolDefinitions returns the model-facing tool definitions after
	// applying runtime policy filtering for the current run.
	AdvertisedToolDefinitions() []*model.ToolDefinition

	// ModelClient returns the raw model client configured for the given model ID.
	// The boolean result is false when the requested model is not configured.
	//
	// Runtime policy wrappers such as tracing, cache defaults, and tool
	// availability are applied, but PlannerEvents emission is not. Planners that
	// want runtime-owned text/thinking/usage streaming should use
	// PlannerModelClient instead.
	ModelClient(id string) (model.Client, bool)

	// PlannerModelClient returns a planner-scoped model client that owns
	// PlannerEvents emission for the current turn. The boolean result is false
	// when the requested model is not configured.
	PlannerModelClient(id string) (PlannerModelClient, bool)

	// RenderPrompt resolves and renders a registered prompt by ID.
	//
	// Runtime applies prompt override precedence using the current run scope
	// (session and labels) before rendering.
	RenderPrompt(ctx context.Context, id prompt.Ident, data any) (*prompt.PromptContent, error)

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

// PlannerEvents lets planners publish durable annotations and model usage.
// Runtime-managed model clients publish validated text and thinking as each
// chunk arrives; planners never receive or republish partial tool JSON.
type PlannerEvents interface {
	// PlannerThought emits a planner note with optional labels for debugging.
	PlannerThought(ctx context.Context, note string, labels map[string]string)

	// UsageDelta reports incremental token usage for the current planning phase.
	UsageDelta(ctx context.Context, usage model.TokenUsage)
}

// ToolRequest is one tool invocation selected by a planner. It contains only
// planner-authored intent; the runtime adds execution metadata after validating
// the complete PlanResult.
type ToolRequest struct {
	// Name is the fully-qualified tool identifier (for example, "svc.read.get_time_series").
	Name tools.Ident

	// Payload is the canonical JSON payload for the tool call.
	Payload rawjson.Message

	// ModelToolCallID is the provider's correlation ID when this request forwards
	// a validated model call. ConsumeStream and ToolRequestFromModelCall set it;
	// planner-authored requests leave it empty. The runtime always assigns a
	// separate execution ID.
	ModelToolCallID string
}

// ToolResult captures the outcome of a tool invocation.
type ToolResult struct {
	// Name is the fully-qualified tool identifier that produced this result.
	Name tools.Ident

	// Result is the decoded tool result value. Its concrete type depends on the
	// tool's result schema and codec.
	Result any

	// ServerData carries server-only data emitted by tool providers (for example,
	// UI projections, evidence, or provenance records) that must not be sent to
	// model providers.
	//
	// Contract:
	//   - This is canonical JSON bytes (typically a JSON array of server-data items).
	//   - The runtime treats the payload as opaque bytes; sinks and UIs decode it
	//     using the tool specs/codecs for the corresponding kinds.
	ServerData rawjson.Message

	// Bounds, when non-nil, describes how the result has been bounded relative
	// to the full underlying data set (for example, list/window/graph caps).
	// Tool implementations and adapters populate this field; the runtime and
	// sinks surface it but never mutate or derive it.
	Bounds *agent.Bounds

	// Failure is the structured tool failure, when execution did not produce a
	// result. Result and Failure are mutually exclusive.
	Failure *ToolFailure

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

// MarshalJSON rejects ToolResult at every nesting depth. Its decoded Result is
// an in-process typed value, not a JSON or workflow-boundary contract. Temporal
// callers must use the typed data converter with a workflow-safe top-level API
// envelope whose tool values are canonical generated-codec bytes.
func (ToolResult) MarshalJSON() ([]byte, error) {
	return nil, errors.New(
		"planner.ToolResult cannot be JSON marshaled; use the Temporal typed converter with a workflow-safe top-level API envelope",
	)
}

// ToolOutput captures one executed tool call in canonical JSON form for planner
// resume and finalization logic.
//
// Contract:
//   - Payload is the canonical JSON input sent to the tool.
//   - Result and ServerData are canonical JSON bytes produced by the tool.
//   - Resume activities hydrate all planner-visible state for this type from the
//     canonical run log before invoking planner code.
//   - This type intentionally does not carry decoded `any` values.
type ToolOutput struct {
	// CallRunID identifies the run log containing the canonical scheduled call.
	// Planners must treat it as opaque execution metadata.
	CallRunID string

	// ResultRunID identifies the run log containing the canonical result. It can
	// differ from CallRunID when external input crosses a workflow suspension.
	ResultRunID string

	// Name is the fully-qualified tool identifier that was executed.
	Name tools.Ident

	// ToolCallID is the correlation identifier for this tool invocation.
	ToolCallID string

	// ModelToolCallID is the provider correlation identifier from the model call
	// that produced this tool invocation. It is empty when planner code authored
	// the call. Planners use it only to match this output to the exact tool-use
	// part in the model transcript; ToolCallID remains the execution identity.
	ModelToolCallID string

	// ContinuationRootToolCallID identifies the original bounded query advanced
	// by this continuation result. It is empty for source queries and ordinary
	// tool calls.
	ContinuationRootToolCallID string

	// Payload is the canonical JSON payload passed to the tool.
	Payload rawjson.Message

	// Result is the canonical JSON result payload encoded with the tool result codec.
	Result rawjson.Message

	// ServerData carries canonical server-only JSON emitted alongside the tool result.
	ServerData rawjson.Message

	// Bounds describes how the result has been bounded relative to the full data set.
	Bounds *agent.Bounds

	// Failure is the structured tool failure, when execution did not produce a
	// result. Result and Failure are mutually exclusive.
	Failure *ToolFailure

	// Telemetry contains execution metrics attributed to this tool output.
	Telemetry *telemetry.ToolTelemetry
}

// FinalResponse contains the assistant message that concludes the run.
type FinalResponse struct {
	// Message is the assistant message returned to the user.
	Message *model.Message
}

// FinalToolResult contains the workflow-safe final tool result emitted by a
// nested planner when it owns the parent tool contract directly.
//
// Contract:
//   - Result is canonical JSON bytes for the parent tool's result schema.
//   - This type must remain workflow-boundary safe; it intentionally does not
//     carry a typed `any` result value.
//   - Top-level planners normally leave this nil and return FinalResponse
//     instead.
type FinalToolResult struct {
	// Result is the canonical JSON result payload encoded with the parent tool's
	// result codec.
	Result rawjson.Message

	// ServerData carries server-only data associated with the final tool result.
	ServerData rawjson.Message

	// Bounds describes how the result has been bounded relative to the full data
	// set when applicable.
	Bounds *agent.Bounds

	// Failure is the structured tool failure, when the nested planner did not
	// produce a result. Result and Failure are mutually exclusive.
	Failure *ToolFailure

	// Telemetry contains execution metrics attributed to the final tool result.
	Telemetry *telemetry.ToolTelemetry
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
//   - Await is one ordered barrier per planner result: the workflow ends with
//     the complete set in a suspension.
//   - Await.Items preserves order. Callers may present items as a wizard; the
//     runtime consumes one item per continuation workflow and resumes planning
//     only after all items are satisfied.
//   - Items may mix kinds (clarification, questions, external tools).
type Await struct {
	Items []AwaitItem
}

// AwaitItemKind identifies the kind of external input required.
type AwaitItemKind string

const (
	AwaitItemKindClarification     AwaitItemKind = "clarification"
	AwaitItemKindToolClarification AwaitItemKind = "tool_clarification"
	AwaitItemKindQuestions         AwaitItemKind = "questions"
	AwaitItemKindExternalTools     AwaitItemKind = "external_tools"
)

// AwaitItem describes one external-input prompt.
//
// Exactly one payload field must be set and must match Kind.
type AwaitItem struct {
	Kind AwaitItemKind

	Clarification     *AwaitClarification
	ToolClarification *AwaitToolClarification
	Questions         *AwaitQuestions
	ExternalTools     *AwaitExternalTools
}

// NewAwait constructs an Await barrier with items in the given order.
func NewAwait(items ...AwaitItem) *Await {
	return &Await{Items: items}
}

// AwaitClarificationItem constructs a clarification await item.
func AwaitClarificationItem(c *AwaitClarification) AwaitItem {
	return AwaitItem{Kind: AwaitItemKindClarification, Clarification: c}
}

// AwaitToolClarificationItem constructs a model-authored free-text tool await item.
func AwaitToolClarificationItem(c *AwaitToolClarification) AwaitItem {
	return AwaitItem{Kind: AwaitItemKindToolClarification, ToolClarification: c}
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

	// ExampleJSON is a canonical JSON example payload to guide the user.
	ExampleJSON rawjson.Message

	// ClarifyingPrompt is an optional prompt to use when building follow-up messages.
	ClarifyingPrompt string
}

// AwaitToolClarification requests one free-text answer for a model-authored
// tool call. The runtime preserves the original call in the provider transcript
// and returns the human answer through the tool's generated result codec.
//
// Contract: the tool result is an object with one required string field named
// "answer". The planner supplies ToolName, ModelToolCallID, and Payload from the
// model call and leaves ToolCallID empty. The workflow assigns ToolCallID before
// exposing the suspension.
type AwaitToolClarification struct {
	// ID uniquely identifies this clarification request.
	ID string

	// ToolName identifies the model-authored free-text tool.
	ToolName tools.Ident

	// ToolCallID is the runtime-owned execution identifier. Planners must leave
	// it empty; the workflow assigns it before suspension.
	ToolCallID string

	// ModelToolCallID is the provider correlation identifier for the
	// model-authored call.
	ModelToolCallID string

	// Payload is the canonical JSON payload for the model-authored call.
	Payload rawjson.Message

	// Question is the user-facing question to ask.
	Question string
}

// AwaitQuestions requests structured multiple-choice answers from the user.
//
// Contract: AwaitQuestions represents one model-authored tool invocation whose
// answers arrive in the next workflow continuation. The planner supplies
// ToolName, ModelToolCallID, and Payload and leaves ToolCallID empty. Place
// multiple questions in that one payload rather than merging calls.
type AwaitQuestions struct {
	// ID uniquely identifies this questions request.
	ID string

	// ToolName identifies the tool awaiting user answers (for example, "assistant.ask_question").
	ToolName tools.Ident

	// ToolCallID is the runtime-owned execution identifier. Planners must leave
	// it empty; the workflow assigns it before suspension.
	ToolCallID string

	// ModelToolCallID is the provider correlation identifier for the
	// model-authored call.
	ModelToolCallID string

	// Payload is the canonical JSON payload for the awaited tool call.
	Payload rawjson.Message

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
// Model-authored items preserve their original order, names, provider IDs, and
// payloads while the workflow assigns separate execution IDs.
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

	// ToolCallID is the runtime-owned execution identifier. Planners must leave
	// it empty; the workflow assigns it before suspension.
	ToolCallID string

	// ModelToolCallID is the provider correlation identifier for the
	// model-authored call.
	ModelToolCallID string

	// Payload is the canonical JSON payload for the external tool call.
	Payload rawjson.Message
}

// TerminationReason indicates why the runtime forced finalization.
type TerminationReason string

const (
	// TerminationReasonTimeBudget indicates the run exceeded its time budget.
	TerminationReasonTimeBudget TerminationReason = "time_budget"

	// TerminationReasonToolCap indicates the run exceeded its allowed tool call count.
	TerminationReasonToolCap TerminationReason = "tool_cap"

	// TerminationReasonRecoveryCap indicates that the run exhausted its allowed
	// consecutive replacement planner activities.
	TerminationReasonRecoveryCap TerminationReason = "recovery_cap"

	// TerminationReasonToolFailure indicates a tool required the run to stop
	// domain work and finalize from the evidence already collected.
	TerminationReasonToolFailure TerminationReason = "tool_failure"
)

// Termination carries a runtime-initiated finalize request.
type Termination struct {
	// Reason explains which runtime condition required finalization.
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

// PlanResumeInput carries messages plus execution history into PlanResume.
type PlanResumeInput struct {
	// Messages is the full conversation history including the most recent tool_use/tool_result blocks.
	Messages []*model.Message

	// RunContext contains durable identifiers and links for the run.
	RunContext run.Context

	// Agent provides access to runtime services (models, memory, telemetry).
	Agent PlannerContext

	// Events allows planners to emit streaming updates.
	Events PlannerEvents

	// ToolOutputs is the accumulated executed tool-call history for the run so far.
	//
	// This is the single truthful planner-facing execution-history boundary.
	// Planners that need typed convenience views derive them locally from these
	// canonical tool outputs instead of relying on a duplicate runtime field.
	ToolOutputs []*ToolOutput

	// SynthesisOnly requires this turn to produce a final response without new
	// tool calls. The workflow sets it only after a successful tool batch whose
	// PlanResult requested SynthesizeAfterTools.
	SynthesisOnly bool

	// Finalize is non-nil when the runtime forbids further domain work and asks
	// the planner for either a final response or terminal bookkeeping calls.
	Finalize *Termination

	// Reminders contains the active system reminders for this planner turn.
	Reminders []reminder.Reminder
}

// PlanResult is the planner's decision for the next step.
type PlanResult struct {
	// ToolCalls are the tool invocations the runtime should execute next.
	ToolCalls []ToolRequest

	// SynthesizeAfterTools requires the planner turn following a successful tool
	// batch to produce a final response without new tool calls. Recoverable tool
	// failures retain their normal repair path; terminal failures still proceed
	// to synthesis.
	SynthesizeAfterTools bool

	// FinalResponse ends the run with a final assistant message.
	FinalResponse *FinalResponse

	// FinalToolResult ends a nested agent run with a canonical parent tool result
	// instead of an assistant message.
	FinalToolResult *FinalToolResult

	// Await requests that the current workflow end with required external input.
	Await *Await

	// ExpectedChildren is an optional hint for how many nested tool results a planner expects.
	ExpectedChildren int

	// Notes are optional planner annotations surfaced to subscribers.
	Notes []PlannerAnnotation
}
