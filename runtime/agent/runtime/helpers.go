package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	policyDecisionMetadataKey = "policy_decisions"
	unknownID                 = "unknown"
)

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

// RootRunID returns the root (top-level) run identifier for a given run ID.
// Nested agent executions derive their run IDs from the parent using the
// NestedRunID format: "<parent>/agent/<toolName>". RootRunID strips the
// first "/agent/" suffix and everything after it. If the input does not
// contain the nested marker, RootRunID returns the input unchanged.
//
// Examples:
//
//	RootRunID("chat-run-123")                              -> "chat-run-123"
//	RootRunID("chat-run-123/agent/atlas_data_agent.ada")   -> "chat-run-123"
//	RootRunID("A/agent/B/agent/C")                         -> "A"
func RootRunID(runID string) string {
	if runID == "" {
		return ""
	}
	const marker = "/agent/"
	if idx := strings.Index(runID, marker); idx >= 0 {
		return runID[:idx]
	}
	return runID
}

// generateDeterministicToolCallID creates a replay-safe tool-call ID using the
// run ID, optional turn ID, sanitized tool name, and the deterministic index of
// the tool within the current batch.
func generateDeterministicToolCallID(runID, turnID string, toolName tools.Ident, index int) string {
	if runID == "" {
		runID = unknownID
	}
	if toolName == "" {
		toolName = "tool"
	}
	safeTool := strings.ReplaceAll(string(toolName), ".", "-")
	// Format: <runID>/<turnID|no-turn>/<tool>/<index>
	tid := turnID
	if tid == "" {
		tid = "no-turn"
	}
	return strings.Join([]string{runID, tid, safeTool, strconv.Itoa(index)}, "/")
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

// agentMessageText concatenates text parts from a model.Message.
func agentMessageText(msg *model.Message) string {
	if msg == nil || len(msg.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range msg.Parts {
		// Skip ThinkingPart to avoid leaking non-user-facing reasoning.
		if _, isThinking := p.(model.ThinkingPart); isThinking {
			continue
		}
		if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
			b.WriteString(tp.Text)
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

// generateParentToolCallID creates a deterministic parent ToolCallID suitable
// for agent-as-tool invocations when the parent ID is not supplied.
// generateParentToolCallID is currently unused.
// func generateParentToolCallID(runID, turnID, toolName string) string {
//     if runID == "" {
//         runID = unknownID
//     }
//     if toolName == "" {
//         toolName = "tool"
//     }
//     safeTool := strings.ReplaceAll(toolName, ".", "-")
//     tid := turnID
//     if tid == "" {
//         tid = "no-turn"
//     }
//     // Suffix with 'p' to avoid collisions with batch indices.
//     return strings.Join([]string{runID, tid, safeTool, "p"}, "/")
// }

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

func cloneStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

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
		out = append(out, &cp)
	}
	return out
}

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

// handlesToIDs removed: policy uses []tools.Ident directly.

func appendPolicyDecisionMetadata(meta map[string]any, entry map[string]any) map[string]any {
	if entry == nil {
		return meta
	}
	if meta == nil {
		meta = make(map[string]any)
	}
	switch current := meta[policyDecisionMetadataKey].(type) {
	case []map[string]any:
		meta[policyDecisionMetadataKey] = append(current, entry)
	case []any:
		list := make([]map[string]any, 0, len(current)+1)
		for _, v := range current {
			if m, ok := v.(map[string]any); ok {
				list = append(list, m)
			}
		}
		list = append(list, entry)
		meta[policyDecisionMetadataKey] = list
	case map[string]any:
		meta[policyDecisionMetadataKey] = []map[string]any{current, entry}
	case nil:
		meta[policyDecisionMetadataKey] = []map[string]any{entry}
	default:
		meta[policyDecisionMetadataKey] = []map[string]any{entry}
	}
	return meta
}

func toPolicyRetryHint(hint *planner.RetryHint) *policy.RetryHint {
	if hint == nil {
		return nil
	}
	return &policy.RetryHint{
		Reason:             policy.RetryReason(hint.Reason),
		Tool:               hint.Tool,
		RestrictToTool:     hint.RestrictToTool,
		MissingFields:      cloneStrings(hint.MissingFields),
		ExampleInput:       cloneMetadata(hint.ExampleInput),
		PriorInput:         cloneMetadata(hint.PriorInput),
		ClarifyingQuestion: hint.ClarifyingQuestion,
		Message:            hint.Message,
	}
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

// suppressionKey derives a stable map key for (runID, parentToolCallID) pairs.
// The separator does not matter as run IDs and tool call IDs are opaque tokens.
func suppressionKey(runID, parentToolCallID string) string {
	return runID + "|" + parentToolCallID
}

// markSuppressedParent records that child inline tool events for the given
// parent tool call (identified by runID and parentToolCallID) should be hidden
// from hooks subscribers. No-op if either identifier is empty.
func (r *Runtime) markSuppressedParent(runID, parentToolCallID string) {
	if runID == "" || parentToolCallID == "" {
		return
	}
	r.suppressMu.Lock()
	r.suppressedParents[suppressionKey(runID, parentToolCallID)] = struct{}{}
	r.suppressMu.Unlock()
}

// unmarkSuppressedParent removes a previously registered suppression entry for
// the given parent tool call. No-op if either identifier is empty.
func (r *Runtime) unmarkSuppressedParent(runID, parentToolCallID string) {
	if runID == "" || parentToolCallID == "" {
		return
	}
	r.suppressMu.Lock()
	delete(r.suppressedParents, suppressionKey(runID, parentToolCallID))
	r.suppressMu.Unlock()
}

// isSuppressedChildEvent reports whether a tool event associated with the
// provided (runID, parentToolCallID) pair should be filtered from the hooks
// bus. Returns false when no suppression entry exists.
func (r *Runtime) isSuppressedChildEvent(runID, parentToolCallID string) bool {
	if runID == "" || parentToolCallID == "" {
		return false
	}
	r.suppressMu.RLock()
	_, ok := r.suppressedParents[suppressionKey(runID, parentToolCallID)]
	r.suppressMu.RUnlock()
	return ok
}

// shouldSuppressHook determines whether a hooks event should be hidden from
// subscribers based on SuppressChildEvents configuration. Only tool start/end
// events associated with a suppressed parent tool call are filtered; all other
// events, including parent ToolCallUpdated events, always flow through.
func (r *Runtime) shouldSuppressHook(evt hooks.Event) bool {
	if r == nil {
		return false
	}
	switch e := evt.(type) {
	case *hooks.ToolCallScheduledEvent:
		return r.isSuppressedChildEvent(e.RunID(), e.ParentToolCallID)
	case *hooks.ToolResultReceivedEvent:
		return r.isSuppressedChildEvent(e.RunID(), e.ParentToolCallID)
	default:
		return false
	}
}

// publishHook publishes an event to the hook bus. If publishing fails, logs a
// warning. If the bus is nil, this is a no-op. The sequencer parameter is optional;
// if nil, events are published without turn tracking. If provided, events are stamped
// with turnID and auto-incremented sequence numbers for deterministic ordering.
//
// Note: This implementation stamps events using reflection to update the embedded
// baseEvent fields. A future enhancement could update event constructors to accept
// turn tracking parameters directly for a cleaner implementation.
func (r *Runtime) publishHook(ctx context.Context, evt hooks.Event, seq *turnSequencer) {
	if r.Bus == nil {
		return
	}
	if r.shouldSuppressHook(evt) {
		return
	}
	// Stamp the event with turn tracking if sequencer is provided
	if seq != nil {
		stampEventWithTurn(evt, seq)
	}
	if err := r.Bus.Publish(ctx, evt); err != nil {
		r.logWarn(ctx, "hook publish failed", err, "event", evt.Type())
	}
}

// initialCaps constructs the initial caps state from the agent's run policy.
// If caps are configured (> 0), the remaining counts are set to match the maximums.
func initialCaps(cfg RunPolicy) policy.CapsState {
	caps := policy.CapsState{
		MaxToolCalls:                  cfg.MaxToolCalls,
		MaxConsecutiveFailedToolCalls: cfg.MaxConsecutiveFailedToolCalls,
	}
	if cfg.MaxToolCalls > 0 {
		caps.RemainingToolCalls = cfg.MaxToolCalls
	}
	if cfg.MaxConsecutiveFailedToolCalls > 0 {
		caps.RemainingConsecutiveFailedToolCalls = cfg.MaxConsecutiveFailedToolCalls
	}
	return caps
}

// decrementCap decrements a cap value by delta. If current is 0 (unlimited), returns 0.
// If the result would be negative, returns 0.
func decrementCap(current int, delta int) int {
	if current == 0 || delta == 0 {
		return current
	}
	result := current - delta
	if result < 0 {
		return 0
	}
	return result
}

// failures counts the number of tool results with non-nil errors.
func failures(results []*planner.ToolResult) int {
	count := 0
	for _, res := range results {
		if res == nil {
			continue
		}
		if res.Error != nil {
			count++
		}
	}
	return count
}

// mergeCaps merges policy decision caps into the current caps state. Decision caps
// override current caps if they are > 0 or if ExpiresAt is set.
func mergeCaps(current policy.CapsState, decision policy.CapsState) policy.CapsState {
	if decision.MaxToolCalls > 0 {
		current.MaxToolCalls = decision.MaxToolCalls
	}
	if decision.RemainingToolCalls > 0 {
		current.RemainingToolCalls = decision.RemainingToolCalls
	}
	if decision.MaxConsecutiveFailedToolCalls > 0 {
		current.MaxConsecutiveFailedToolCalls = decision.MaxConsecutiveFailedToolCalls
	}
	if decision.RemainingConsecutiveFailedToolCalls > 0 {
		current.RemainingConsecutiveFailedToolCalls = decision.RemainingConsecutiveFailedToolCalls
	}
	if !decision.ExpiresAt.IsZero() {
		current.ExpiresAt = decision.ExpiresAt
	}
	return current
}

// toolHandles converts tool call requests into policy tool handles for policy evaluation.
func toolHandles(calls []planner.ToolRequest) []tools.Ident {
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

// toolMetadata retrieves policy metadata for each tool call by looking up the
// toolset registration. If the toolset is not found, constructs minimal metadata
// with the tool name.
func (r *Runtime) toolMetadata(calls []planner.ToolRequest) []policy.ToolMetadata {
	metas := make([]policy.ToolMetadata, 0, len(calls))
	for _, call := range calls {
		if spec, ok := r.toolSpec(call.Name); ok {
			metas = append(metas, policy.ToolMetadata{
				ID:          spec.Name,
				Title:       defaultToolTitle(spec.Name),
				Description: spec.Description,
				Tags:        append([]string(nil), spec.Tags...),
			})
			continue
		}
		metas = append(metas, policy.ToolMetadata{
			ID:    call.Name,
			Title: defaultToolTitle(call.Name),
		})
	}
	return metas
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
func filterToolCalls(calls []planner.ToolRequest, allowed []tools.Ident) []planner.ToolRequest {
	if len(allowed) == 0 {
		return calls
	}
	allow := make(map[tools.Ident]struct{}, len(allowed))
	for _, id := range allowed {
		allow[id] = struct{}{}
	}
	filtered := make([]planner.ToolRequest, 0, len(calls))
	for _, call := range calls {
		if _, ok := allow[call.Name]; ok {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

// stampEventWithTurn updates the baseEvent fields in an event with turn tracking
// information. This uses a type switch to explicitly handle each event type in a
// type-safe manner without reflection. The compiler will catch if we add new event
// types and forget to handle them here.
func stampEventWithTurn(evt hooks.Event, seq *turnSequencer) {
	seqNum := seq.nextSeq()

	// Type switch to access and update the embedded baseEvent in each concrete event type.
	// This is explicit, type-safe, and the compiler will help us maintain it.
	switch e := evt.(type) {
	case *hooks.RunStartedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.RunCompletedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.ToolCallScheduledEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.ToolResultReceivedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.ToolCallUpdatedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.PlannerNoteEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.AssistantMessageEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.RetryHintIssuedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.MemoryAppendedEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.PolicyDecisionEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.AwaitClarificationEvent:
		e.SetTurn(seq.turnID, seqNum)
	case *hooks.AwaitExternalToolsEvent:
		e.SetTurn(seq.turnID, seqNum)
	}
}

// ConvertRunOutputToToolResult converts a RunOutput (from ExecuteAgentInline) into
// a planner.ToolResult. This helper is used by generated Execute functions for
// agent-tools to adapt the nested agent's output into the ToolResult format expected
// by the ToolsetRegistration.Execute signature.
//
// The final assistant message content is extracted as the tool result payload (string).
// Telemetry from all nested tool executions is aggregated into a single ToolTelemetry
// summary, enabling proper cost/token tracking across agent-as-tool boundaries.
//
// Error propagation: If the nested agent executed tools and ALL of them failed, the
// ToolResult.Error field is set with a summary. This allows the parent planner to
// react appropriately (retry, skip, or abort) rather than treating a failed nested
// agent as a successful tool execution.
//
// Planner notes are currently discarded. Future enhancement: include notes as structured
// metadata or append them to the payload content for visibility to the parent planner.
func ConvertRunOutputToToolResult(toolName tools.Ident, output RunOutput) planner.ToolResult {
	var resultContent string
	if output.Final != nil {
		resultContent = agentMessageText(output.Final)
	}
	result := planner.ToolResult{
		Name:   toolName,
		Result: resultContent,
	}
	// Record child count for agent-as-tool detection in the runtime.
	result.ChildrenCount = len(output.ToolEvents)

	// Aggregate telemetry and track failures from all nested tool executions
	if len(output.ToolEvents) > 0 {
		var totalTokens int
		var totalDurationMs int64
		var models []string
		var failedCount int
		var lastError error
		modelSeen := make(map[string]bool)

		for _, event := range output.ToolEvents {
			if event.Telemetry != nil {
				totalTokens += event.Telemetry.TokensUsed
				totalDurationMs += event.Telemetry.DurationMs
				if event.Telemetry.Model != "" && !modelSeen[event.Telemetry.Model] {
					models = append(models, event.Telemetry.Model)
					modelSeen[event.Telemetry.Model] = true
				}
			}
			// Track tool failures
			if event.Error != nil {
				failedCount++
				lastError = event.Error
			}
		}

		// If ALL tools failed, propagate error to parent planner
		if failedCount > 0 && failedCount == len(output.ToolEvents) {
			if failedCount == 1 {
				result.Error = planner.NewToolErrorWithCause(fmt.Sprintf("agent-tool %q: nested tool failed", toolName), lastError)
			} else {
				result.Error = planner.NewToolErrorWithCause(fmt.Sprintf("agent-tool %q: all %d nested tools failed", toolName, failedCount), lastError)
			}
		}

		// Create aggregated telemetry if we collected any data
		if totalTokens > 0 || totalDurationMs > 0 || len(models) > 0 {
			result.Telemetry = &telemetry.ToolTelemetry{
				TokensUsed: totalTokens,
				DurationMs: totalDurationMs,
			}
			// If multiple models were used, record first one
			if len(models) > 0 {
				result.Telemetry.Model = models[0]
			}
		}
	}

	return result
}
