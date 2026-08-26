package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	unknownID                     = "unknown"
	generatedToolCallIDHashDomain = "goa-ai/runtime-tool-call-id/v2\x00"
	// maxHookPayloadBytes is a safety bound on the serialized hook payload passed
	// across the workflow/activity boundary. Exceeding Temporal's payload limit
	// terminates the workflow task; failing early keeps failures explicit and
	// debuggable.
	maxHookPayloadBytes = 1_000_000
)

type (
	// PromptRenderHookContext identifies one agent run turn for prompt_rendered
	// hook emission.
	//
	// Callers that render prompts through Runtime.PromptRegistry outside planner
	// contexts must stamp the render context with this metadata so runtime can
	// append canonical prompt_rendered events to the run log.
	PromptRenderHookContext struct {
		RunID     string
		AgentID   agent.Ident
		SessionID string
		TurnID    string
	}

	promptRenderHookContextKey struct{}
	plannerEventCollectorKey   struct{}
)

// WithPromptRenderHookContext returns ctx stamped with run metadata used by
// runtime prompt observer callbacks to emit canonical prompt_rendered events.
func WithPromptRenderHookContext(ctx context.Context, meta PromptRenderHookContext) context.Context {
	return context.WithValue(ctx, promptRenderHookContextKey{}, meta)
}

// withPromptRenderHookContext returns a context stamped with runtime run metadata
// used by onPromptRendered to publish canonical prompt_rendered hook events.
func withPromptRenderHookContext(ctx context.Context, meta PromptRenderHookContext) context.Context {
	return WithPromptRenderHookContext(ctx, meta)
}

// promptRenderHookContextFromContext extracts prompt-render hook metadata.
func promptRenderHookContextFromContext(ctx context.Context) (PromptRenderHookContext, bool) {
	if ctx == nil {
		return PromptRenderHookContext{}, false
	}
	meta, ok := ctx.Value(promptRenderHookContextKey{}).(PromptRenderHookContext)
	if !ok {
		return PromptRenderHookContext{}, false
	}
	return meta, true
}

// withPlannerEventCollector makes prompt-render events part of the accepted
// planner activity output instead of publishing from the retryable activity.
func withPlannerEventCollector(ctx context.Context, events *runtimePlannerEvents) context.Context {
	return context.WithValue(ctx, plannerEventCollectorKey{}, events)
}

// plannerEventCollectorFromContext returns the planner event batch attached to
// ctx when prompt rendering is running inside a planner activity.
func plannerEventCollectorFromContext(ctx context.Context) (*runtimePlannerEvents, bool) {
	if ctx == nil {
		return nil, false
	}
	events, ok := ctx.Value(plannerEventCollectorKey{}).(*runtimePlannerEvents)
	return events, ok
}

// hasNonNullJSON reports whether raw contains a non-empty JSON value other than
// the literal `null`.
//
// json.RawMessage marshals nil slices as the bytes `null`. When crossing the
// workflow/activity boundary, an absent tool result payload may therefore
// round-trip as the literal bytes `null` even when the tool produced no result.
// Callers should treat `null` as "no payload" and skip decoding.
func hasNonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// NestedRunID generates a hierarchical run ID for nested agent execution.
// Format: "{parentRunID}/agent/{toolName}". If parentRunID is empty, returns
// "unknown/agent/{toolName}". This ensures nested agent runs are traceable back
// to their parent invocation.
//
// Generated code for agent-tools uses this to construct nested run contexts
// from the parent run metadata passed explicitly in ToolRequest.
func NestedRunID(parentRunID string, toolName tools.Ident) string {
	if parentRunID == "" {
		parentRunID = unknownID
	}
	return fmt.Sprintf("%s/agent/%s", parentRunID, toolName)
}

// NestedRunIDForToolCall generates a child workflow ID for agent-as-tool runs that
// is stable for a given tool invocation and unique across multiple invocations of
// the same tool within a single parent run.
//
// The base prefix matches NestedRunID: "{parentRunID}/agent/{toolName}".
// When toolCallID is set, the exact runtime-owned ID is appended:
// "{parentRunID}/agent/{toolName}/{toolCallID}".
//
// This prevents Temporal child workflow ID collisions when planners invoke the
// same agent tool multiple times in one parent workflow.
func NestedRunIDForToolCall(parentRunID string, toolName tools.Ident, toolCallID string) string {
	base := NestedRunID(parentRunID, toolName)
	if toolCallID == "" {
		return base
	}
	return fmt.Sprintf("%s/%s", base, toolCallID)
}

// generateDeterministicToolCallID creates a replay-safe tool-call ID using the
// run ID, optional turn ID, attempt counter, exact tool name, and the
// deterministic index of the tool within the current batch.
//
// Attempt is required to avoid ID collisions when the same run executes multiple
// tool batches within a single logical turn (for example when callers set TurnID
// to a constant run identifier). The workflow stamps RunContext.Attempt with the
// planner turn attempt before executing that turn's generated tool calls, so
// generated IDs remain unique within the run. Every identity component is
// length-delimited before hashing, so separators inside strings and distinct
// dotted tool names cannot produce the same preimage.
func generateDeterministicToolCallID(runID, turnID string, attempt int, toolName tools.Ident, index int) string {
	identity := []byte(generatedToolCallIDHashDomain)
	identity = appendLengthDelimited(identity, runID)
	identity = appendLengthDelimited(identity, turnID)
	identity = binary.AppendVarint(identity, int64(attempt))
	identity = binary.AppendVarint(identity, int64(index))
	identity = appendLengthDelimited(identity, string(toolName))
	sum := sha256.Sum256(identity)
	return "call-" + hex.EncodeToString(sum[:])
}

// appendLengthDelimited appends one string without allowing neighboring
// identity components to absorb separators or bytes from each other.
func appendLengthDelimited(dst []byte, value string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// generateDeterministicAwaitID creates a replay-safe await identifier using the runID,
// optional turnID, the tool name, and the originating tool call ID when available.
// The format mirrors other runtime IDs for ease of correlation:
// <runID>/<turnID|no-turn>/<tool>/await/<toolCallID|no-call>
func generateDeterministicAwaitID(runID, turnID string, tool tools.Ident, toolCallID string) string {
	if runID == "" {
		runID = unknownID
	}
	safeTool := strings.ReplaceAll(string(tool), ".", "-")
	if safeTool == "" {
		safeTool = "tool"
	}
	tid := turnID
	if tid == "" {
		tid = "no-turn"
	}
	if toolCallID == "" {
		toolCallID = "no-call"
	}
	return strings.Join([]string{runID, tid, safeTool, "await", toolCallID}, "/")
}

// agentMessageText concatenates assistant-visible text parts from a model.Message.
func agentMessageText(msg *model.Message) string {
	if msg == nil || len(msg.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range msg.Parts {
		switch v := p.(type) {
		case model.ThinkingPart:
			// Skip ThinkingPart to avoid leaking non-user-facing reasoning.
			continue
		case model.TextPart:
			if v.Text != "" {
				b.WriteString(v.Text)
			}
		case model.CitationsPart:
			if v.Text != "" {
				b.WriteString(v.Text)
			}
		}
	}
	return b.String()
}

// newTextAgentMessage builds a model.Message with a single TextPart.
// Returns nil when text is empty to allow callers to skip no-op messages.
func newTextAgentMessage(role model.ConversationRole, text string) *model.Message {
	if text == "" {
		return nil
	}
	return &model.Message{
		Role:  role,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
}

// isZeroRetryPolicy checks if a retry policy is effectively zero (no retries configured).
func isZeroRetryPolicy(policy engine.RetryPolicy) bool {
	return policy.MaxAttempts == 0 && policy.InitialInterval == 0 && policy.BackoffCoefficient == 0
}

// cloneLabels creates a defensive copy of a string map. Returns nil if the source
// map is empty to avoid unnecessary allocations.
func cloneLabels(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// cloneToolCall gives an executor ownership of every mutable field while the
// runtime retains the canonical call for result validation, publication, and
// continuation correlation.
func cloneToolCall(src ToolCall) ToolCall {
	cloned := src
	cloned.Payload = append(rawjson.Message(nil), src.Payload...)
	cloned.ModelPayload = append(rawjson.Message(nil), src.ModelPayload...)
	cloned.Labels = cloneLabels(src.Labels)
	return cloned
}

// cloneMetadata creates a defensive copy of an arbitrary metadata map.
// It returns nil if the source map is empty to avoid unnecessary allocations.
func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// cloneToolResults copies tool results before workflow state owns the batch.
func cloneToolResults(src []*planner.ToolResult) []*planner.ToolResult {
	if len(src) == 0 {
		return nil
	}
	out := make([]*planner.ToolResult, 0, len(src))
	for _, tr := range src {
		if tr == nil {
			out = append(out, nil)
			continue
		}
		cp := *tr
		cp.Failure = planner.CloneToolFailure(tr.Failure)
		out = append(out, &cp)
	}
	return out
}

// addTokenUsage combines nonnegative token counts without allowing integer
// overflow to create invalid durable usage.
func addTokenUsage(current, delta model.TokenUsage) (model.TokenUsage, error) {
	input, err := addTokenCount("input", current.InputTokens, delta.InputTokens)
	if err != nil {
		return model.TokenUsage{}, err
	}
	output, err := addTokenCount("output", current.OutputTokens, delta.OutputTokens)
	if err != nil {
		return model.TokenUsage{}, err
	}
	total, err := addTokenCount("total", current.TotalTokens, delta.TotalTokens)
	if err != nil {
		return model.TokenUsage{}, err
	}
	cacheRead, err := addTokenCount("cache read", current.CacheReadTokens, delta.CacheReadTokens)
	if err != nil {
		return model.TokenUsage{}, err
	}
	cacheWrite, err := addTokenCount("cache write", current.CacheWriteTokens, delta.CacheWriteTokens)
	if err != nil {
		return model.TokenUsage{}, err
	}
	return model.TokenUsage{
		InputTokens:      input,
		OutputTokens:     output,
		TotalTokens:      total,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}, nil
}

// addTokenCount rejects invalid input and overflow before addition can wrap.
func addTokenCount(name string, current, delta int) (int, error) {
	if current < 0 || delta < 0 {
		return 0, fmt.Errorf("%s token usage cannot be negative", name)
	}
	sum := current + delta
	if sum < current {
		return 0, fmt.Errorf("%s token usage exceeds the supported integer range", name)
	}
	return sum, nil
}

// mergeLabels merges src labels into dst. When dst is nil, it allocates a new
// map sized to src. When src is empty, it returns dst unchanged.
func mergeLabels(dst map[string]string, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// applyHistoryPolicy applies the agent's history policy to the given messages.
// Policy failures are planning failures because the runtime cannot construct the
// transcript promised by the agent registration.
func (r *Runtime) applyHistoryPolicy(ctx context.Context, reg *AgentRegistration, msgs []*model.Message, tools []*model.ToolDefinition) ([]*model.Message, error) {
	if reg.Policy.History == nil || len(msgs) == 0 {
		return msgs, nil
	}
	out, err := reg.Policy.History(ctx, msgs, tools)
	if err != nil {
		return nil, fmt.Errorf("history policy for agent %s: %w", reg.ID, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("history policy for agent %s returned no messages", reg.ID)
	}
	return out, nil
}

// logWarn emits a warning log and records the error in the current span if tracing
// is enabled. If the logger is nil, this is a no-op.
func (r *Runtime) logWarn(ctx context.Context, msg string, err error, kv ...any) {
	fields := append([]any{}, kv...)
	if err != nil {
		fields = append(fields, "err", err)
	}
	r.logger.Warn(ctx, msg, fields...)
	if err != nil {
		span := r.tracer.Span(ctx)
		if span != nil {
			span.RecordError(err)
		}
	}
}

// publishHookErr emits a runtime hook event and returns an error on failure.
//
// When called from workflow code (ctx carries engine.WorkflowContext), publishHookErr
// schedules the runtime record activity. Outside workflows, it calls the record
// activity directly. In both cases, the record activity is responsible for
// appending the event to the canonical run log before publishing to the bus.
//
// This function exists because runtime hook emission is semantically split:
//   - Run event log append is canonical and must succeed (hard correctness invariant).
//   - Hook bus publish is best-effort and must not fail the workflow (used for live UX).
//
// Callers in non-workflow/server code should prefer publishHookErr and propagate
// the error. Workflow loop code may choose to treat failures as fatal via
// publishHook (panic) to avoid silent divergence.
func (r *Runtime) publishHookErr(ctx context.Context, evt hooks.Event, turnID string) error {
	return r.publishHookWithOptions(ctx, evt, turnID, engine.ActivityOptions{})
}

// publishHookWithOptions persists one hook event with explicit workflow
// activity bounds when the caller owns a completion deadline.
func (r *Runtime) publishHookWithOptions(
	ctx context.Context,
	evt hooks.Event,
	turnID string,
	options engine.ActivityOptions,
) error {
	in, err := prepareHookRecordInput(ctx, evt, turnID)
	if err != nil {
		return err
	}
	return r.publishPreparedHook(ctx, in, options)
}

// prepareHookRecordInput freezes one hook event into its immutable activity
// envelope so retries can reuse the exact event key, timestamp, and payload.
func prepareHookRecordInput(
	ctx context.Context,
	evt hooks.Event,
	turnID string,
) (*RecordActivityInput, error) {
	meta := recordDispatchMetadataForContext(ctx)
	if eventKey := toolLifecycleEventKey(evt); eventKey != "" {
		meta.EventKey = eventKey
	}
	in, err := hooks.EncodeToRecordInput(evt, hooks.EncodeOptions{
		TurnID:      turnID,
		EventKey:    meta.EventKey,
		TimestampMS: meta.TimestampMS,
	})
	if err != nil {
		return nil, err
	}
	if len(in.Payload) > maxHookPayloadBytes {
		return nil, fmt.Errorf(
			"runtime: hook payload exceeds budget (%d > %d bytes, type=%s, run_id=%s)",
			len(in.Payload),
			maxHookPayloadBytes,
			evt.Type(),
			evt.RunID(),
		)
	}
	return in, nil
}

// publishPreparedHook persists a previously frozen hook envelope.
func (r *Runtime) publishPreparedHook(
	ctx context.Context,
	in *RecordActivityInput,
	options engine.ActivityOptions,
) error {
	batch := &api.RecordActivityBatchInput{Records: []*RecordActivityInput{in}}
	if wfCtx := engine.WorkflowContextFromContext(ctx); wfCtx != nil && !engine.IsActivityContext(ctx) {
		return wfCtx.PublishRecords(engine.RecordActivityCall{
			Name:    recordActivityName,
			Input:   batch,
			Options: options,
		})
	}
	return r.recordActivity(ctx, batch)
}

// toolLifecycleEventKey gives each tool-call lifecycle transition one stable
// durable identity so activity retries and completion recovery are idempotent.
func toolLifecycleEventKey(evt hooks.Event) string {
	var toolCallID string
	switch event := evt.(type) {
	case *hooks.ToolCallScheduledEvent:
		toolCallID = event.ToolCallID
	case *hooks.ToolResultReceivedEvent:
		toolCallID = event.ToolCallID
	default:
		return ""
	}
	return fmt.Sprintf("%s/tool/%s/%s", evt.RunID(), toolCallID, evt.Type())
}

// publishHook emits a runtime hook event and returns an error on failure.
//
// Note that bus publish failures do not cause publishHookErr to return an error;
// only failures to encode, dispatch the record activity, or append to the canonical
// run log are considered fatal.
func (r *Runtime) publishHook(ctx context.Context, evt hooks.Event, turnID string) error {
	return r.publishHookErr(ctx, evt, turnID)
}

// publishTranscriptMessagesErr persists canonical transcript messages as a
// durable run-log record with the specified transcript record type.
func (r *Runtime) publishTranscriptMessagesErr(
	ctx context.Context,
	recordType runlog.Type,
	runID string,
	agentID agent.Ident,
	sessionID string,
	turnID string,
	messages []*model.Message,
) error {
	payload, err := transcript.EncodeRunLogDelta(messages)
	if err != nil {
		return err
	}
	if len(payload) > maxHookPayloadBytes {
		return fmt.Errorf(
			"runtime: transcript delta payload exceeds budget (%d > %d bytes, run_id=%s)",
			len(payload),
			maxHookPayloadBytes,
			runID,
		)
	}
	meta := recordDispatchMetadataForContext(ctx)
	input := &runlog.ActivityInput{
		Type:        recordType,
		EventKey:    meta.EventKey,
		RunID:       runID,
		AgentID:     agentID,
		SessionID:   sessionID,
		TurnID:      turnID,
		TimestampMS: meta.TimestampMS,
		Payload:     payload,
	}
	batch := &api.RecordActivityBatchInput{Records: []*RecordActivityInput{input}}
	if wfCtx := engine.WorkflowContextFromContext(ctx); wfCtx != nil && !engine.IsActivityContext(ctx) {
		return wfCtx.PublishRecords(engine.RecordActivityCall{
			Name:  recordActivityName,
			Input: batch,
		})
	}
	return r.recordActivity(ctx, batch)
}

// publishTranscriptSeedErr persists canonical transcript seed messages for a
// run. Seeded transcript messages rebuild run snapshots but must not fan out as
// newly committed assistant turns.
func (r *Runtime) publishTranscriptSeedErr(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID string,
	turnID string,
	messages []*model.Message,
) error {
	return r.publishTranscriptMessagesErr(
		ctx,
		transcript.RunLogMessagesSeeded,
		runID,
		agentID,
		sessionID,
		turnID,
		messages,
	)
}

// publishTranscriptDeltaErr persists canonical transcript messages appended
// during the run. Appended assistant messages are eligible for committed
// assistant-turn fanout.
func (r *Runtime) publishTranscriptDeltaErr(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID string,
	turnID string,
	messages []*model.Message,
) error {
	return r.publishTranscriptMessagesErr(
		ctx,
		transcript.RunLogMessagesAppended,
		runID,
		agentID,
		sessionID,
		turnID,
		messages,
	)
}

// publishTranscriptDelta is the panic-free wrapper used by workflow/runtime code
// when transcript persistence failures must stop the run.
func (r *Runtime) publishTranscriptDelta(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID string,
	turnID string,
	messages []*model.Message,
) error {
	return r.publishTranscriptDeltaErr(ctx, runID, agentID, sessionID, turnID, messages)
}

// publishTranscriptSeed is the panic-free wrapper used by workflow/runtime code
// for run-start transcript seed persistence.
func (r *Runtime) publishTranscriptSeed(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID string,
	turnID string,
	messages []*model.Message,
) error {
	return r.publishTranscriptSeedErr(ctx, runID, agentID, sessionID, turnID, messages)
}

type recordDispatchMetadata struct {
	EventKey    string
	TimestampMS int64
}

// recordDispatchMetadataForContext returns canonical append metadata for one
// durable record emission.
//
// Workflow code must use a replay-stable identity owned by the emitting workflow
// itself. Non-workflow callers do not have deterministic sequencing, so they use
// a UUID instead.
func recordDispatchMetadataForContext(ctx context.Context) recordDispatchMetadata {
	if wfCtx := engine.WorkflowContextFromContext(ctx); wfCtx != nil && !engine.IsActivityContext(ctx) {
		workflowID := wfCtx.WorkflowID()
		if workflowID == "" {
			panic("runtime: workflow context missing workflow id")
		}
		return recordDispatchMetadata{
			EventKey:    formatWorkflowRecordEventKey(workflowID, wfCtx.NextSequence()),
			TimestampMS: wfCtx.Now().UnixMilli(),
		}
	}
	return recordDispatchMetadata{
		EventKey:    uuid.NewString(),
		TimestampMS: time.Now().UnixMilli(),
	}
}

// formatWorkflowRecordEventKey builds the canonical durable identity for one
// workflow-emitted record.
//
// The identity is scoped to the emitting workflow, not the target run log. That
// keeps the contract simple and lets multiple child workflows append distinct
// records into the same parent run without key collisions.
func formatWorkflowRecordEventKey(emitterWorkflowID string, seq uint64) string {
	return fmt.Sprintf("%s/%d", url.PathEscape(emitterWorkflowID), seq)
}

// onPromptRendered is the runtime-owned observer callback used by PromptRegistry.
//
// The registry must never publish directly to the hook bus. Runtime owns the
// append-to-runlog-then-publish ordering through publishHookErr, and this callback
// is the integration seam where prompt-render hook events are emitted.
func (r *Runtime) onPromptRendered(ctx context.Context, event prompt.RenderEvent) {
	meta, ok := promptRenderHookContextFromContext(ctx)
	if !ok {
		panic(fmt.Sprintf(
			"runtime: prompt_rendered missing hook context (prompt_id=%s version=%s)",
			event.PromptID,
			event.Version,
		))
	}
	hookEvent := hooks.NewPromptRenderedEvent(
		meta.RunID,
		meta.AgentID,
		meta.SessionID,
		event.PromptID,
		event.Version,
		event.Scope,
	)
	if events, ok := plannerEventCollectorFromContext(ctx); ok {
		events.publish(ctx, hookEvent)
		return
	}
	if err := r.publishHookErr(ctx, hookEvent, meta.TurnID); err != nil {
		panic(fmt.Errorf(
			"runtime: prompt_rendered hook publish failed (run_id=%s prompt_id=%s version=%s): %w",
			meta.RunID,
			event.PromptID,
			event.Version,
			err,
		))
	}
}

// mergeCaps merges policy decision caps into the current caps state. Policy
// decisions may only tighten configured caps; they never create new caps or
// raise the remaining budget.
func mergeCaps(current policy.CapsState, decision policy.CapsState) policy.CapsState {
	current.MaxToolCalls = mergeCapDown(current.MaxToolCalls, decision.MaxToolCalls)
	current.RemainingToolCalls = mergeCapDown(current.RemainingToolCalls, decision.RemainingToolCalls)
	current.MaxRecoveryTurns = mergeCapDown(
		current.MaxRecoveryTurns,
		decision.MaxRecoveryTurns,
	)
	current.RemainingRecoveryTurns = mergeCapDown(
		current.RemainingRecoveryTurns,
		decision.RemainingRecoveryTurns,
	)
	return current
}

// mergeCapDown returns the tighter of two configured caps. Zero means the cap is
// not configured and therefore cannot be introduced or raised by policy.
func mergeCapDown(current int, decision int) int {
	if current == 0 || decision == 0 {
		return current
	}
	return min(current, decision)
}

// toolHandles converts tool call requests into policy tool handles for policy evaluation.
func toolHandles(calls []ToolCall) []tools.Ident {
	handles := make([]tools.Ident, len(calls))
	for i, call := range calls {
		handles[i] = call.Name
	}
	return handles
}

// hasIntersection reports whether two string slices share at least one common value.
func hasIntersection(a []string, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := set[v]; ok {
			return true
		}
	}
	return false
}

// isBookkeeping reports whether the tool is exempt from the run-level
// MaxToolCalls retrieval budget, consulting the canonical policy metadata
// BudgetClass assigned once at registration time.
func (r *Runtime) isBookkeeping(name tools.Ident) bool {
	meta, ok := r.policyMetadata(name)
	if !ok {
		return false
	}
	return meta.BudgetClass == policy.ToolBudgetClassBookkeeping
}

// toolMetadata retrieves policy metadata for each tool call by looking up the
// registered canonical metadata. If the tool is not found, it constructs minimal
// metadata with the tool name and the default budget class.
func (r *Runtime) toolMetadata(calls []ToolCall) []policy.ToolMetadata {
	metas := make([]policy.ToolMetadata, 0, len(calls))
	for _, call := range calls {
		if meta, ok := r.policyMetadata(call.Name); ok {
			metas = append(metas, cloneToolMetadata(meta))
			continue
		}
		metas = append(metas, policy.ToolMetadata{
			ID:          call.Name,
			Title:       defaultToolTitle(call.Name),
			BudgetClass: policy.ToolBudgetClassBudgeted,
		})
	}
	return metas
}

func canonicalToolMetadata(spec tools.ToolSpec, lookup ToolMetadataLookup) policy.ToolMetadata {
	if lookup != nil {
		meta, ok := lookup(spec.Name)
		if !ok {
			panic(fmt.Sprintf("runtime: missing policy metadata for tool %q", spec.Name))
		}
		return cloneToolMetadata(meta)
	}
	return policy.ToolMetadata{
		ID:          spec.Name,
		Title:       defaultToolTitle(spec.Name),
		Description: spec.Description,
		Tags:        append([]string(nil), spec.Tags...),
		BudgetClass: toolBudgetClass(spec.Bookkeeping),
	}
}

func cloneToolMetadata(meta policy.ToolMetadata) policy.ToolMetadata {
	meta.Tags = append([]string(nil), meta.Tags...)
	return meta
}

func toolBudgetClass(bookkeeping bool) policy.ToolBudgetClass {
	if bookkeeping {
		return policy.ToolBudgetClassBookkeeping
	}
	return policy.ToolBudgetClassBudgeted
}

// defaultToolTitle derives a human-friendly title from a fully-qualified tool id.
// It uses the last segment after '.' and converts snake_case/kebab-case to Title Case.
func defaultToolTitle(id tools.Ident) string {
	s := string(id)
	// take last segment after '.'
	if last := lastSegment(s, '.'); last != "" {
		s = last
	}
	// Normalize separators to spaces
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	// Collapse multiple spaces
	s = strings.Join(strings.Fields(s), " ")
	// Title-case words
	var b strings.Builder
	for i, w := range strings.Fields(s) {
		if i > 0 {
			b.WriteByte(' ')
		}
		if len(w) == 0 {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		for j := 1; j < len(r); j++ {
			r[j] = unicode.ToLower(r[j])
		}
		b.WriteString(string(r))
	}
	return b.String()
}

// lastSegment returns the last segment of a string after the last separator.
func lastSegment(s string, sep rune) string {
	for i := len(s) - 1; i >= 0; i-- {
		if rune(s[i]) == sep {
			if i+1 < len(s) {
				return s[i+1:]
			}
			return ""
		}
	}
	return s
}

// filterToolCalls filters tool calls to only those present in the allowed list.
// If the allowed list is empty, returns all calls unchanged.
func filterToolCalls(calls []ToolCall, allowed []tools.Ident) []ToolCall {
	if len(allowed) == 0 {
		return calls
	}
	allow := make(map[tools.Ident]struct{}, len(allowed))
	for _, id := range allowed {
		allow[id] = struct{}{}
	}
	filtered := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Name == tools.ToolUnavailable {
			filtered = append(filtered, call)
			continue
		}
		if _, ok := allow[call.Name]; ok {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

// ConvertRunOutputToToolResult converts a nested agent RunOutput into a
// planner.ToolResult suitable for returning from an agent-as-tool executor.
//
// The final assistant message content is extracted as the tool result payload (string).
// Telemetry from all nested tool executions is aggregated into a single ToolTelemetry
// summary, enabling proper cost/token tracking across agent-as-tool boundaries.
//
// Server data is intentionally NOT projected or merged into the parent tool result.
// Server data must remain attached to the tool events that produced it so UIs and
// sinks can render/ingest it at the correct node in nested tool trees.
//
// Planner notes are currently discarded. Future enhancement: include notes as structured
// metadata or append them to the payload content for visibility to the parent planner.
func ConvertRunOutputToToolResult(toolName tools.Ident, output *RunOutput) (planner.ToolResult, error) {
	if output == nil || output.Final == nil {
		return planner.ToolResult{}, fmt.Errorf("agent-tool %q requires a final assistant response", toolName)
	}
	result := planner.ToolResult{
		Name:   toolName,
		Result: agentMessageText(output.Final),
	}
	// Record child count for agent-as-tool detection in the runtime.
	result.ChildrenCount = len(output.ToolEvents)

	// Aggregate telemetry from all nested tool executions. Historical tool
	// failures remain in the child run log and do not redefine a successful
	// final response.
	if len(output.ToolEvents) > 0 {
		var totalTokens int
		var totalDurationMs int64
		models := make(map[string]struct{})

		for _, event := range output.ToolEvents {
			if event.Telemetry != nil {
				if event.Telemetry.TokensUsed < 0 ||
					event.Telemetry.TokensUsed > int(^uint(0)>>1)-totalTokens {
					return planner.ToolResult{}, fmt.Errorf("agent-tool %q token telemetry overflows int", toolName)
				}
				if event.Telemetry.DurationMs < 0 ||
					event.Telemetry.DurationMs > int64(^uint64(0)>>1)-totalDurationMs {
					return planner.ToolResult{}, fmt.Errorf("agent-tool %q duration telemetry overflows int64", toolName)
				}
				totalTokens += event.Telemetry.TokensUsed
				totalDurationMs += event.Telemetry.DurationMs
				if event.Telemetry.Model != "" {
					models[event.Telemetry.Model] = struct{}{}
				}
			}
		}

		// Create aggregated telemetry if we collected any data
		if totalTokens > 0 || totalDurationMs > 0 || len(models) > 0 {
			result.Telemetry = &telemetry.ToolTelemetry{
				TokensUsed: totalTokens,
				DurationMs: totalDurationMs,
			}
			if len(models) == 1 {
				for modelName := range models {
					result.Telemetry.Model = modelName
				}
			}
		}
	}

	return result, nil
}
