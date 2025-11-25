// Package planner defines the contracts between user-provided planners and the
// goa-ai runtime. Planners decide which tools to call (or when to answer
// directly) based on the current messages, run context, and planner events.
package planner

import (
	"context"
	"encoding/json"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Planner is the agent decision maker: it analyzes messages and returns either
// tool calls to execute or a final assistant response.
type Planner interface {
	PlanStart(ctx context.Context, input *PlanInput) (*PlanResult, error)
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

// ToolResult captures the outcome of a tool invocation.
type ToolResult struct {
	Name          tools.Ident
	Result        any
	Metadata      map[string]any
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
}

// PlanResumeInput carries messages plus recent tool results into PlanResume.
type PlanResumeInput struct {
	Messages    []*model.Message
	RunContext  run.Context
	Agent       PlannerContext
	Events      PlannerEvents
	ToolResults []*ToolResult
	Finalize    *Termination
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
