// Package api defines shared types that cross workflow/activity boundaries in the
// agent runtime.
package api

import (
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/internal/responseevidence"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RunInput captures everything an initial or continuation workflow needs.
	// It includes the full conversational context plus caller-provided labels and
	// metadata.
	RunInput struct {
		// AgentID identifies which agent should process the run.
		AgentID agent.Ident

		// RunID is the durable workflow execution identifier.
		RunID string

		// SessionID groups related runs (for example, multi-turn conversations).
		SessionID string

		// TurnID identifies the conversational turn (optional). When set, all events
		// produced during this run are tagged with this TurnID for UI grouping.
		TurnID string

		// ParentToolCallID identifies the parent tool call when this run represents a
		// nested agent execution (agent-as-tool). Empty for top-level runs. Used to
		// correlate ToolCallUpdated events and propagate parent-child relationships.
		ParentToolCallID string

		// ParentRunID identifies the run that scheduled this nested execution. Empty for
		// top-level runs. When set, tool events emitted by this run can be attributed to
		// the parent run.
		ParentRunID string

		// ParentAgentID identifies the agent that invoked this nested execution. Empty for
		// top-level runs. When set with ParentRunID, tool events can retain the parent agent
		// identity even though execution happens in a child agent.
		ParentAgentID agent.Ident

		// Tool identifies the fully-qualified tool name when this run is a nested
		// agent-as-tool execution. For top-level runs (not invoked via a parent tool),
		// Tool is empty. Planners may use this to select method-specific prompts.
		// Format: "<service>.<toolset>.<tool>".
		Tool tools.Ident

		// ToolArgs carries the original JSON arguments for the parent tool when this run
		// is an agent-as-tool execution. Nil for top-level runs. Nested agent planners
		// can use this structured input to render method-specific prompts without
		// reparsing free-form messages.
		ToolArgs rawjson.Message

		// Messages carries the conversation history supplied by the caller.
		Messages []*model.Message

		// RenderedPrompts lists prompts whose rendered text is already present in
		// Messages. The accepted workflow stores these facts after it creates the
		// run and before it starts planner work.
		RenderedPrompts []prompt.RenderEvent

		// Labels contains caller-provided metadata (account, priority, etc.).
		Labels map[string]string

		// Metadata allows orchestrators to attach arbitrary structured data.
		Metadata map[string]any

		// WorkflowOptions carries engine-specific start options (memo, search attributes,
		// custom task queues). If nil, the runtime derives defaults from the agent registration.
		WorkflowOptions *WorkflowOptions

		// Policy carries optional per-run policy overrides applied on every planner turn.
		// These options allow callers to set caps and tool filters without modifying
		// the agent registration defaults.
		Policy *PolicyOverrides

		// Continuation restores a run that previously ended because it required
		// external input. It is nil for the first workflow in a turn chain.
		Continuation *RunContinuationInput
	}

	// RunContinuationInput starts a new workflow from one exact suspended run.
	// The runtime validates Response against the first pending request before it
	// decodes Checkpoint or schedules additional work.
	RunContinuationInput struct {
		// Suspension is the terminal result returned by the preceding workflow.
		Suspension *RunSuspension

		// Response satisfies the first request in Suspension.Pending.
		Response *PendingInputResponse
	}

	// WorkflowOptions mirrors a subset of engine start options exposed through the runtime.
	// Engine adapters convert these into native options at start time.
	WorkflowOptions struct {
		// Memo is a map of key-value pairs that can be used to store data for the workflow.
		Memo map[string]any

		// SearchAttributes is a map of key-value pairs indexed by the engine for visibility.
		SearchAttributes map[string]any

		// TaskQueue is the name of the task queue to use for the workflow.
		TaskQueue string
	}

	// TagPolicyClause describes one tag-filtering clause for a run.
	//
	// Contract:
	//   - A tool passes the clause when AllowedAny is empty or intersects the tool tags.
	//   - A tool fails the clause when any DeniedAny tag intersects the tool tags.
	//   - Runtimes apply all configured clauses with logical AND.
	TagPolicyClause struct {
		// AllowedAny allows a tool when at least one listed tag is present. Empty
		// means this clause does not constrain allowed tags.
		AllowedAny []string

		// DeniedAny rejects a tool when any listed tag is present. Empty means
		// this clause does not constrain denied tags.
		DeniedAny []string
	}

	// PolicyOverrides configures per-run policy constraints. All fields are optional;
	// zero values mean no override.
	PolicyOverrides struct {
		// RestrictToTool restricts tool execution to the given tool identifier.
		RestrictToTool tools.Ident

		// CompletionTool identifies the budgeted tool whose first successful
		// execution completes this run without a final planner response. The run
		// fails rather than synthesizing a substitute response if it terminates
		// before this tool succeeds.
		CompletionTool tools.Ident

		// TagClauses applies explicit tag-policy clauses using logical AND.
		TagClauses []TagPolicyClause

		// MaxToolCalls caps the total number of budgeted (non-bookkeeping) tool
		// calls a run may execute.
		MaxToolCalls int

		// MaxRecoveryTurns caps consecutive additional planner activities
		// scheduled after rejected tool or model output. Successful budgeted
		// tool work resets the count.
		MaxRecoveryTurns int

		// TimeBudget caps active planner and tool work. External-input waits do
		// not consume this budget.
		TimeBudget time.Duration

		// PlanTimeout overrides the per-turn plan/resume activity timeout.
		PlanTimeout time.Duration

		// ToolTimeout overrides the default per-tool execution timeout.
		ToolTimeout time.Duration

		// PerToolTimeout overrides tool execution timeouts for specific tools.
		PerToolTimeout map[tools.Ident]time.Duration

		// FinalizerGrace extends the active TimeBudget deadline into the Hard
		// deadline available to finalization and bookkeeping.
		FinalizerGrace time.Duration

		// LimitTerminalPlans supplies one terminal tool call for each way a run
		// can exhaust its configured limits. When nil, the planner writes the
		// final response from saved messages.
		LimitTerminalPlans *LimitTerminalPlans
	}

	// LimitTerminalPlans contains the complete set of calls a workflow may
	// select after normal work reaches a configured limit.
	LimitTerminalPlans struct {
		// TimeBudget runs when active planner and tool work exhausts TimeBudget.
		TimeBudget LimitTerminalCall

		// ToolCallCap runs when budgeted tool calls exhaust MaxToolCalls.
		ToolCallCap LimitTerminalCall

		// RecoveryCap runs when replacement planner activities exhaust
		// MaxRecoveryTurns.
		RecoveryCap LimitTerminalCall
	}

	// LimitTerminalCall contains only the application-selected terminal tool
	// and its validated JSON payload. Goa-AI adds run identifiers, labels, the
	// limit reason, and a tool-call identifier when it executes the call.
	LimitTerminalCall struct {
		// Name identifies a registered terminal bookkeeping tool.
		Name tools.Ident

		// Payload is canonical JSON for the tool's generated payload type.
		Payload rawjson.Message
	}

	// RunOutput represents the terminal outcome returned by one workflow,
	// including either a completed result or a suspension plus accumulated tool
	// traces and planner notes for callers.
	RunOutput struct {
		// AgentID echoes the agent that produced the result.
		AgentID agent.Ident

		// RunID echoes the workflow execution identifier.
		RunID string

		// Final is the assistant reply returned to the caller. It is nil when the
		// run ended by terminal-tool contract rather than planner-authored text.
		Final *model.Message

		// FinalToolResult is the canonical tool result that completed the run
		// without an assistant reply. Terminal and required completion tools place
		// their successful result here. Nested agents use the same field when the
		// child planner or runtime owns the outer tool contract directly.
		//
		// Contract:
		// - This uses the same workflow-safe envelope as ToolEvents because it also
		//   crosses a workflow boundary.
		// - Result bytes are canonical JSON for the parent tool's result schema.
		// - Tool-completed runs also retain this event in ToolEvents so callers can
		//   inspect the complete execution history in order.
		FinalToolResult *ToolEvent

		// ToolEvents captures all tool results emitted before completion in execution order.
		//
		// Contract:
		// - ToolEvents must be workflow-boundary safe. Do not embed planner.ToolResult here:
		//   planner.ToolResult contains `any` fields (Result) which Temporal will
		//   rehydrate as map[string]any in parent workflows, eliminating strong typing
		//   at the boundary.
		ToolEvents []*ToolEvent

		// Notes aggregates planner annotations produced during the final turn.
		Notes []*planner.PlannerAnnotation

		// Usage aggregates model-reported token usage during the run when available.
		Usage *model.TokenUsage

		// Suspension is set instead of Final when the run ended because it needs
		// external input. The caller continues by starting a new workflow with this
		// value and one matching PendingInputResponse.
		Suspension *RunSuspension
	}

	// RunSuspension is the complete workflow-safe result of stopping for external
	// input. Applications must keep the complete value in trusted server-side
	// storage; Checkpoint may contain private transcript and execution state and
	// may only be decoded by goa-ai.
	RunSuspension struct {
		// ID uniquely identifies the exact checkpoint and visible pending requests.
		ID string

		// Version identifies the checkpoint schema understood by the runtime.
		Version string

		// Checkpoint contains canonical JSON owned exclusively by the runtime. It
		// must never be sent to an untrusted client.
		Checkpoint rawjson.Message

		// Pending lists required inputs in the exact order they must be supplied.
		Pending []*PendingInput

		// RequiredTools lists the registered tools referenced by the saved state.
		// A continuation worker must register every name; its generated codecs then
		// validate the concrete saved payloads and results during restoration.
		RequiredTools []tools.Ident
	}

	// PendingInputKind identifies the one response shape accepted for a pending
	// external-input request.
	PendingInputKind string

	// PendingInput describes one exact external input requested by the runtime.
	// Exactly one payload field must be set and must match Kind.
	PendingInput struct {
		// Kind selects the required response shape.
		Kind PendingInputKind

		// Confirmation is set for a tool authorization decision.
		Confirmation *PendingConfirmation

		// Await is set for planner-authored clarification, questions, or external
		// tools. Tool-bound awaits carry the runtime ToolCallID used by callers
		// and the provider ModelToolCallID retained for transcript reconstruction.
		Await *planner.AwaitItem
	}

	// PendingConfirmation describes one tool call that cannot execute until a
	// person explicitly approves or denies it.
	PendingConfirmation struct {
		// ID uniquely identifies this request.
		ID string

		// Title is the short heading displayed with the decision.
		Title string

		// Prompt explains the decision being requested.
		Prompt string

		// ToolName is the exact tool awaiting authorization.
		ToolName tools.Ident

		// ToolCallID is the runtime-owned execution identifier.
		ToolCallID string

		// Payload is the exact canonical tool input awaiting authorization.
		Payload rawjson.Message
	}

	// PendingInputResponse supplies exactly one response to the first pending
	// request in a RunSuspension. Exactly one field must be set.
	PendingInputResponse struct {
		// Clarification supplies free-form user text.
		Clarification *ClarificationAnswer

		// Confirmation supplies an approval or denial decision.
		Confirmation *ConfirmationDecision

		// ToolResults supplies structured question answers or external tool results.
		ToolResults *ToolResultsSet
	}

	// ToolEvent carries a tool result through workflow execution without losing
	// its generated Go type. Result and ServerData remain JSON bytes while the
	// workflow engine transports them. The runtime decodes Result with the
	// registered tool codec before giving it to a planner. This is necessary for
	// nested agents because workflow engines decode arbitrary Go values as
	// generic maps.
	ToolEvent struct {
		// Name is the fully-qualified tool identifier that produced this result.
		Name tools.Ident

		// Result is the JSON result encoded with the generated tool result codec.
		Result rawjson.Message

		// ServerData carries server-only data emitted alongside the tool result.
		// It is never sent to model providers.
		ServerData rawjson.Message

		// Bounds, when non-nil, describes how the result has been bounded relative
		// to the full underlying data set (for example, list/window/graph caps).
		Bounds *agent.Bounds

		// Failure is the structured tool failure when execution did not produce a
		// result.
		Failure *planner.ToolFailure

		// Telemetry contains tool execution metrics (duration, token usage, model).
		Telemetry *telemetry.ToolTelemetry

		// ToolCallID is the correlation identifier for this tool invocation.
		ToolCallID string

		// ChildrenCount records how many nested tool results were observed when this
		// result came from an agent-as-tool execution.
		ChildrenCount int

		// RunLink links this tool result to a nested agent run when it was produced by
		// an agent-as-tool. Nil for service-backed tools.
		RunLink *run.Handle
	}

	// ToolOutputRef is the workflow-boundary safe reference to one canonical tool
	// output stored in the run log.
	//
	// Contract:
	// - This is a pure identity envelope for planner resume/finalization.
	// - The runtime hydrates all planner-visible tool state from canonical run-log
	//   events before invoking planners.
	ToolOutputRef struct {
		// CallRunID identifies the run log containing ToolCallScheduled.
		CallRunID string

		// ResultRunID identifies the run log containing ToolResultReceived.
		ResultRunID string

		// ToolCallID is the correlation identifier for this tool invocation.
		ToolCallID string
	}

	// RecoveryCatalog records the exact tools advertised by a recovery planner
	// activity. Its presence lets the workflow enforce the same catalog after
	// the activity returns. Recovery turns require this catalog.
	RecoveryCatalog struct {
		// Tools lists canonical tool identifiers in advertised order. An empty
		// list means the recovery turn required a tool-free result.
		Tools []tools.Ident
	}

	// PlanActivityInput carries the planner input for PlanStart and PlanResume activities.
	PlanActivityInput struct {
		// AgentID identifies which agent is being planned.
		AgentID agent.Ident

		// RunID identifies the run being planned.
		RunID string

		// Messages is the current conversation transcript provided to the planner.
		Messages []*model.Message

		// RunContext carries nested-run metadata (parent IDs, tool identifiers, etc.).
		RunContext run.Context

		// Policy snapshots the per-run policy visible to the planner activity.
		// Runtimes use this to shape the planner-visible advertised tool list.
		Policy *PolicyOverrides

		// ToolOutputs is the accumulated executed tool-call history for the run so far.
		//
		// Contract:
		// - This is the sole planner-facing execution-history field across the
		//   workflow/activity boundary.
		// - Entries are references only; they do not inline planner-visible tool data.
		// - The runtime rehydrates canonical tool input, result, server data, and
		//   planner-visible metadata from the run event log before invoking planners.
		ToolOutputs []*ToolOutputRef

		// RecoveryToolCallIDs selects the failed outputs whose recovery directives
		// shape this planner turn. The activity uses these stable identities to
		// derive its executable catalog and ephemeral recovery guidance from
		// canonical run-log outputs. PlanStart and ordinary PlanResume omit the
		// empty field because they have no failed output to recover.
		RecoveryToolCallIDs []string `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// ModelOutputRecovery requests replacement of one rejected final answer.
		// Its presence makes this a synthesis-only turn, so callers cannot supply
		// correction guidance while leaving domain tools available.
		ModelOutputRecovery *ModelOutputRecovery `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// ModelInvocationRecovery requests replacement of one pre-canonical tool
		// call rejected by generated input validation or provider response
		// validation. Exactly one recovery variant is present, and the ordinary
		// executable catalog remains available on this planner turn.
		ModelInvocationRecovery *ModelInvocationRecovery `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// SynthesisOnly requires the planner to produce a final response without
		// new tool calls after completed tool work.
		SynthesisOnly bool

		// Finalize requests a terminal turn with no further domain tool work.
		// The planner may return a final response or terminal bookkeeping calls.
		Finalize *planner.Termination
	}

	// ModelOutputRecovery contains bounded guidance for replacing one rejected
	// final answer. PlanResume derives synthesis-only behavior from this value.
	ModelOutputRecovery struct {
		// Correction tells the planner which output contract the replacement
		// answer must satisfy.
		Correction string
	}

	// ModelInvocationRecovery contains exactly one bounded fact needed to
	// replace a tool call rejected before a canonical response existed.
	ModelInvocationRecovery struct {
		// Correction names only generated input constraints. It contains no
		// rejected payload or submitted values.
		Correction string

		// UnadvertisedToolName is the exact provider-returned name that was absent
		// from the tools advertised for the failed request. It contains no tool
		// arguments, call identifier, response text, or copied catalog.
		UnadvertisedToolName string
	}

	// PlannerEventRecord is one accepted planner event awaiting workflow-owned
	// identity and durable publication.
	PlannerEventRecord struct {
		// Type identifies the record variant.
		Type runlog.Type
		// Payload contains the encoded event fields.
		Payload rawjson.Message
	}

	// ToolCall is one validated tool invocation prepared by the runtime for
	// workflow execution. Planner implementations cannot construct this type
	// through their PlanResult contract.
	ToolCall struct {
		// Name is the fully-qualified tool identifier the runtime will execute.
		Name tools.Ident

		// Payload is the canonical JSON payload sent to the tool.
		Payload rawjson.Message

		// ModelName is the model-facing tool identifier preserved when runtime
		// compilation changes Name. Empty means the transcript identity is Name.
		ModelName tools.Ident

		// ModelPayload is the exact model payload preserved before the runtime
		// adds or replaces execution fields. Empty means the transcript payload is
		// Payload.
		ModelPayload rawjson.Message

		// AgentID identifies the agent that owns this call.
		AgentID agent.Ident

		// RunID identifies the run that owns this call.
		RunID string

		// SessionID identifies the logical session that owns this call.
		SessionID string

		// Labels carries runtime-assigned metadata used by policies and prompts.
		Labels map[string]string

		// TurnID identifies the conversational turn that owns this call.
		TurnID string

		// ToolCallID uniquely identifies this invocation across runtime records.
		ToolCallID string

		// ModelToolCallID is the provider correlation ID preserved only for
		// rebuilding a model transcript. It is empty for planner-authored calls.
		ModelToolCallID string

		// ParentToolCallID identifies the parent call for nested execution.
		ParentToolCallID string

		// ContinuationRootToolCallID identifies the original bounded query
		// advanced by this continuation call.
		ContinuationRootToolCallID string
	}

	// PlanResult is the runtime-owned, workflow-safe result of one accepted
	// planner activity. ToolCalls have already passed planner-result validation
	// and contain execution metadata unavailable to planner implementations.
	PlanResult struct {
		// ToolCalls are the validated tool invocations to execute next.
		ToolCalls []ToolCall

		// SynthesizeAfterTools requires the next successful planner turn to return
		// a final response without new tool calls.
		SynthesizeAfterTools bool

		// FinalResponse ends the run with a final assistant message.
		FinalResponse *planner.FinalResponse

		// FinalToolResult ends a nested run with a canonical parent tool result.
		FinalToolResult *planner.FinalToolResult

		// Await requests external input before planning resumes.
		Await *planner.Await

		// ExpectedChildren records the planner's expected nested result count.
		ExpectedChildren int

		// Notes are planner annotations surfaced to subscribers.
		Notes []planner.PlannerAnnotation
	}

	// PlanActivityOutput carries one runtime-owned result from a planner
	// activity across the Temporal activity/workflow boundary.
	//
	// Internal runtime contract:
	//   - Before encoding, the complete value must fit a conservative 1 MiB
	//     upper bound for its JSON/Temporal payload and require at most 100,000
	//     reflection visits. The bound includes escaped strings and field names,
	//     scalar text, base64 byte slices, collection punctuation, type
	//     envelopes, Result, Transcript, PlannerEvents, failure metadata, and
	//     every nested message, tool payload, result, label, and dynamic
	//     metadata value.
	//   - The runtime checks this contract before returning an activity result.
	//     An oversized success becomes a planner-origin OutputContractFailure
	//     with no partial Result or PlannerEvents.
	//   - When model output was already rejected, an oversized auxiliary event
	//     batch is removed while numeric Usage, model origin, and bounded model
	//     response evidence remain intact.
	PlanActivityOutput struct {
		// PublicationBatchID uniquely identifies this successful planner activity
		// completion. The activity generates one UUID after planning and carries
		// it with accepted or rejected output so the workflow can retry the exact
		// publication batch without colliding with a later activity completion.
		PublicationBatchID string

		// Result contains the accepted planner decision after tool intents have
		// been converted to runtime-owned execution calls.
		Result *PlanResult

		// Transcript contains the provider-visible transcript produced by the planner.
		Transcript []*model.Message

		// Usage is the token usage reported by the model provider when available.
		Usage model.TokenUsage

		// PlannerEvents contains accepted events for the workflow to publish with
		// deterministic identities after this activity succeeds.
		PlannerEvents []*PlannerEventRecord

		// RecoveryCatalog is present only for a recovery-aware resume activity
		// and records the exact executable catalog shown to that planner turn.
		RecoveryCatalog *RecoveryCatalog `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// OutputContractFailure is present when model or planner output was
		// rejected. The workflow publishes Usage and PlannerEvents from this
		// successful activity result before recovering or terminating the run.
		OutputContractFailure *OutputContractFailure `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// ModelInvocationRecovery is present instead of OutputContractFailure
		// when generated input validation or provider response validation
		// produced exactly one safe replacement fact for the rejected invocation.
		ModelInvocationRecovery *ModelInvocationRecovery `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.
	}

	// OutputContractFailure preserves rejected model or planner evidence across
	// the activity boundary without returning a result beside an activity error.
	OutputContractFailure struct {
		// Origin identifies whether model output or the planner result failed its
		// contract.
		Origin planner.OutputContractOrigin

		// ModelOutputValidationKind identifies the first mechanical response
		// rule that rejected output. It is present only when the rejection came
		// from model.OutputValidationError. Planner-authored policy rejections
		// and histories written before categories existed leave it empty.
		ModelOutputValidationKind model.OutputValidationKind `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// ReasonSHA256 identifies the private validation-cause text without
		// carrying that text through Temporal.
		ReasonSHA256 string

		// ReasonSize is the number of bytes covered by ReasonSHA256.
		ReasonSize int64

		// ModelResponsePresent reports whether the provider returned a complete
		// response before the rejection.
		ModelResponsePresent bool

		// ModelResponseSHA256 identifies the exact versioned encoding of complete
		// provider responses rejected by the activity. It is empty when no
		// complete response existed or when invalid metadata could not be
		// encoded; ModelResponsePresent distinguishes those cases.
		ModelResponseSHA256 string

		// ModelResponseFingerprintVersion identifies the encoding covered by
		// ModelResponseSHA256.
		ModelResponseFingerprintVersion string

		// ModelResponseSize is the number of bytes covered by
		// ModelResponseSHA256.
		ModelResponseSize int64

		// Correction contains bounded guidance for replacing rejected model
		// output. Empty means the rejection is terminal.
		Correction string `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.
	}

	// RecordActivityInput is the canonical workflow-to-activity envelope for
	// durable runtime records.
	RecordActivityInput = runlog.ActivityInput

	// StorageActivityCommand selects exactly one durable state change. Workflow
	// code freezes records before scheduling the activity, and retries reuse the
	// complete command without rebuilding it.
	StorageActivityCommand struct {
		// Append stores ordinary records without changing run lifecycle state.
		Append *AppendRecordsCommand
		// RootStart stores the start of a session root run. An ended session also
		// stores the canceled completion that prevents the run from doing work.
		RootStart *RootRunStartCommand
		// ChildStart stores a parent link and the start of a child run. An ended
		// session also stores the canceled completion that prevents the run from
		// doing work.
		ChildStart *ChildRunStartCommand
		// OneShotStart stores the first record for a sessionless run.
		OneShotStart *OneShotRunStartCommand
		// OneShotChildStart stores a parent link and start for a sessionless child.
		OneShotChildStart *OneShotChildRunStartCommand
		// Cancellation stores the first cancellation reason and its record.
		Cancellation *RunCancellationCommand
		// Suspension stores a continuation checkpoint and its suspended record.
		Suspension *RunSuspensionCommand
		// Terminal stores a terminal record and final run status.
		Terminal *RunTerminalCommand
	}

	// AppendRecordsCommand carries one non-empty immutable ordered record list.
	AppendRecordsCommand struct {
		// Records preserves workflow-assigned event keys, timestamps, and order.
		Records []*RecordActivityInput
	}

	// RootRunStartCommand carries the frozen start record for a session root run.
	RootRunStartCommand struct {
		// Started is the run-started record.
		Started *RecordActivityInput
	}

	// ChildRunStartCommand carries the records that link and start a child run.
	ChildRunStartCommand struct {
		// ParentLinked is stored on the parent run.
		ParentLinked *RecordActivityInput
		// Started is stored on the child run when its session is active.
		Started *RecordActivityInput
	}

	// OneShotRunStartCommand carries the frozen start record for a sessionless run.
	OneShotRunStartCommand struct {
		// Started is the run-started record.
		Started *RecordActivityInput
	}

	// OneShotChildRunStartCommand carries the frozen records for a sessionless child.
	OneShotChildRunStartCommand struct {
		// ParentLinked is stored on the sessionless parent run.
		ParentLinked *RecordActivityInput
		// Started is stored on the child run.
		Started *RecordActivityInput
	}

	// RunCancellationCommand carries the frozen cancellation-intent record.
	RunCancellationCommand struct {
		// Record contains the write-once cancellation reason.
		Record *RecordActivityInput
	}

	// RunSuspensionCommand carries one checkpoint and its matching suspended record.
	RunSuspensionCommand struct {
		// Checkpoint contains the opaque continuation state.
		Checkpoint *RecordActivityInput
		// Suspended records that the workflow stopped with this checkpoint.
		Suspended *RecordActivityInput
	}

	// RunTerminalCommand carries one completed, failed, or canceled record.
	RunTerminalCommand struct {
		// Record is the final run-completed record.
		Record *RecordActivityInput
	}

	// StorageActivityResult reports the result matching the selected command.
	// Exactly one field is set, and it must match the command field.
	StorageActivityResult struct {
		// Append reports ordinary record writes.
		Append *AppendRecordsResult
		// RootStart reports the root-start decision.
		RootStart *StartRunResult
		// ChildStart reports the child-start decision.
		ChildStart *StartRunResult
		// OneShotStart reports the one-shot start decision.
		OneShotStart *StartRunResult
		// OneShotChildStart reports the sessionless child start writes.
		OneShotChildStart *StartRunResult
		// Cancellation reports whether the cancellation reason was accepted.
		Cancellation *RunCancellationResult
		// Suspension reports the suspended record write.
		Suspension *RecordWriteResult
		// Terminal reports the terminal record write.
		Terminal *RecordWriteResult
	}

	// AppendRecordsResult reports ordinary writes in command order.
	AppendRecordsResult struct {
		// Records contains store results in durable commit order.
		Records []storage.AppendResult
	}

	// StartRunResult reports the immutable start decision and every stored record.
	StartRunResult struct {
		// Outcome is exactly proceed or stop.
		Outcome session.RunStartOutcome
		// CancellationReason is set only when Outcome is stop.
		CancellationReason string
		// Records contains the stored records in durable commit order.
		Records []storage.AppendResult
	}

	// RunCancellationResult reports the result of storing a cancellation reason.
	RunCancellationResult struct {
		// Outcome is exactly accepted or conflict.
		Outcome RunCancellationOutcome
		// Record contains the accepted write result. It is zero when Outcome is
		// conflict because the stored reason belongs to an earlier command.
		Record storage.AppendResult
	}

	// RecordWriteResult reports one suspension or terminal record write.
	RecordWriteResult struct {
		// Record is the durable store result.
		Record storage.AppendResult
	}

	// RunCancellationOutcome reports whether the write-once reason was accepted.
	RunCancellationOutcome string

	// ToolInput carries the execution payload for one tool call from workflow
	// code to its activity. The workflow retains model-authored transcript data.
	ToolInput struct {
		// RunID identifies the run that owns this tool call.
		RunID string

		// AgentID identifies the agent that owns this tool call.
		AgentID agent.Ident

		// ToolsetName identifies the owning toolset when known; it may be empty when inferred by ToolName.
		ToolsetName string

		// ToolName is the fully-qualified tool identifier.
		ToolName tools.Ident

		// ToolCallID uniquely identifies the tool invocation for correlation across events.
		ToolCallID string

		// Payload is the execution-enriched JSON sent to the tool.
		Payload rawjson.Message

		// SessionID is the logical session identifier (for example, a chat conversation).
		SessionID string

		// Labels carries caller-defined run metadata dimensions used by runtime
		// policies and prompt scoping.
		Labels map[string]string

		// TurnID identifies the conversational turn that produced this tool call.
		TurnID string

		// ParentToolCallID is the identifier of the parent tool call when this invocation is nested.
		ParentToolCallID string
	}

	// AgentChildActivityInput carries the exact parent state needed to prepare
	// one child-agent run outside workflow code.
	AgentChildActivityInput struct {
		// Call is the validated agent-tool invocation that starts the child.
		Call ToolCall

		// Messages is the parent transcript visible to the child-call validator.
		Messages []*model.Message

		// ParentRun identifies the run that issued Call.
		ParentRun run.Context
	}

	// AgentChildActivitySuccess contains the prompt facts recorded in workflow
	// history after child preparation succeeds.
	AgentChildActivitySuccess struct {
		// Messages is the complete initial child transcript.
		Messages []*model.Message

		// RenderedPrompts identifies every stored prompt version used in Messages.
		RenderedPrompts []prompt.RenderEvent
	}

	// AgentChildActivityOutput contains exactly one preparation result. Workflow
	// replay reuses a recorded success without rendering the prompt again.
	AgentChildActivityOutput struct {
		// Success contains the prepared messages and prompt versions.
		Success *AgentChildActivitySuccess

		// Failure is the canonical correction returned when the model-authored
		// tool payload cannot start a child.
		Failure *planner.ToolFailure
	}

	// ToolOutput is returned by tool executors after invoking the tool implementation.
	ToolOutput struct {
		// Payload is the tool result encoded as JSON. The runtime decodes it using the registered tool codec.
		Payload rawjson.Message

		// ServerData carries server-only data emitted alongside the tool result. It is
		// never forwarded to model providers, but it must cross workflow/activity
		// boundaries so in-process subscribers (persistence, metrics, UIs) can
		// consume it.
		//
		// Contract:
		//   - This is canonical JSON (typically a JSON array of server data items).
		//   - Tool implementations may return nil when no server data is present.
		ServerData rawjson.Message

		// Bounds carries canonical bounded-result metadata across workflow/activity
		// boundaries when the tool contract declares an out-of-band bounds channel.
		Bounds *agent.Bounds

		// Telemetry contains execution timing and provider usage metadata when available.
		Telemetry *telemetry.ToolTelemetry

		// Failure is the structured tool failure when execution did not produce a
		// result.
		Failure *planner.ToolFailure

		// Clarification carries an optional user question emitted with this tool
		// result.
		//
		// Contract:
		//   - This survives the tool activity boundary so the workflow can end with
		//     a continuation request before the next planner call.
		//   - The runtime consumes it from the current batch only and does not
		//     persist it into cumulative planner ToolOutputs history.
		Clarification *ToolClarification
	}

	// ToolClarification requests free-form user input after a tool result and
	// before the runtime's next planner call.
	ToolClarification struct {
		// ID uniquely identifies this clarification request.
		ID string

		// Question is the operator-facing prompt to present.
		Question string
	}

	// ClarificationAnswer carries a typed answer for a suspended clarification request.
	ClarificationAnswer struct {
		// ID is the clarification await identifier.
		ID string

		// Answer is the free-form clarification text provided by the actor.
		Answer string

		// Labels carries optional metadata associated with the clarification answer.
		Labels map[string]string
	}

	// ConfirmationDecision carries a typed decision for a confirmation await.
	ConfirmationDecision struct {
		// ID is the confirmation await identifier.
		ID string

		// Approved is true when the operator approved the pending action.
		Approved bool

		// RequestedBy identifies the logical actor that provided the decision.
		RequestedBy string

		// Labels carries optional metadata associated with the decision.
		Labels map[string]string

		// Metadata carries arbitrary structured data for audit trails (for example, ticket IDs or justification codes).
		Metadata map[string]any
	}

	// ProvidedToolResult carries one externally supplied tool result into a
	// continuation workflow.
	//
	// Contract:
	// - This is a raw input envelope from an external actor (for example, a
	//   user-interface service), not the runtime's canonical `ToolEvent` representation.
	// - Result bytes must use the tool's generated result codec and remain
	//   canonical JSON, but server-only sidecars are never provided here; the
	//   runtime materializes them after decoding.
	ProvidedToolResult struct {
		// Name is the fully-qualified tool identifier that produced this result.
		Name tools.Ident

		// ToolCallID correlates the result with the original awaited tool call.
		ToolCallID string

		// Success is set exactly when external execution produced a result.
		Success *ProvidedToolSuccess

		// Failure is the externally observed failure when execution did not
		// produce a result. Exactly one of Success and Failure must be set.
		Failure *ProvidedToolFailure
	}

	// ProvidedToolSuccess carries a successful external result and its optional
	// bounded-result metadata.
	ProvidedToolSuccess struct {
		// Result contains canonical JSON for the tool's result contract. It must
		// be empty when the registered tool has no result contract. A tool with a
		// result contract must decode to one non-nil value.
		Result rawjson.Message

		// Bounds carries bounded-result metadata when the tool contract requires it.
		Bounds *agent.Bounds
	}

	// ProvidedToolFailure carries the failure facts owned by an external tool
	// executor. The runtime combines these facts with the awaited call and
	// registered tool metadata to construct a canonical planner.ToolFailure.
	ProvidedToolFailure struct {
		// Kind classifies why the external execution failed.
		Kind planner.FailureKind

		// Message describes the external execution failure.
		Message string

		// Action declares the legal next planner transition.
		Action planner.RecoveryAction

		// Issues contains generated field issues when the external boundary
		// rejected model-authored input.
		Issues []*tools.FieldIssue
	}

	// ToolResultsSet carries results for an external-tools or structured-question
	// input request.
	ToolResultsSet struct {
		// ID is the await identifier corresponding to the original AwaitExternalTools event.
		ID string

		// Results contains the tool results provided by an external system.
		//
		// Contract:
		// - This field crosses a workflow continuation boundary. It must be wire-safe and must
		//   not embed planner.ToolResult (which contains `any`) or the runtime-owned
		//   `ToolEvent` envelope.
		// - The runtime owns decoding, result materialization, canonicalization,
		//   and server-side sidecar attachment after the continuation is received.
		Results []*ProvidedToolResult
	}
)

const (
	// RunCancellationAccepted means the command stored this reason or matched
	// the reason stored by an exact retry.
	RunCancellationAccepted RunCancellationOutcome = "accepted"

	// RunCancellationConflict means the run already stores another reason.
	RunCancellationConflict RunCancellationOutcome = "conflict"

	// PendingInputKindClarification requires a Clarification response.
	PendingInputKindClarification PendingInputKind = "clarification"

	// PendingInputKindConfirmation requires a Confirmation response.
	PendingInputKindConfirmation PendingInputKind = "confirmation"

	// PendingInputKindToolResults requires a ToolResults response.
	PendingInputKindToolResults PendingInputKind = "tool_results"

	// RunSuspensionVersion is the checkpoint schema emitted by this runtime.
	// Version 7 stores complete successful tool results and omits metadata that
	// can be calculated from those bytes.
	RunSuspensionVersion = "goa-ai.run-suspension.v7"

	// ModelResponseFingerprintVersionV1 identifies the first stable rejected
	// model-response fingerprint encoding stored in workflow payloads.
	ModelResponseFingerprintVersionV1 = responseevidence.VersionV1

	// ModelResponseFingerprintVersionV2 adds the provider-neutral output-limit
	// classification to the rejected model-response fingerprint.
	ModelResponseFingerprintVersionV2 = responseevidence.VersionV2
)

// TranscriptName returns the tool name recorded in the provider transcript.
func (c ToolCall) TranscriptName() tools.Ident {
	if c.ModelName != "" {
		return c.ModelName
	}
	return c.Name
}

// TranscriptPayload returns the tool payload recorded in the provider transcript.
func (c ToolCall) TranscriptPayload() rawjson.Message {
	if len(c.ModelPayload) > 0 {
		return c.ModelPayload
	}
	return c.Payload
}
