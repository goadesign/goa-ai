// Package evidence projects an agent run's client-facing stream events into
// typed, assertable Evidence and evaluates declarative expectations over it.
//
// Evidence is the framework-owned record of one run tree's observable
// behavior: tool calls in canonical causal order, the accumulated assistant
// answer, a pending confirmation boundary, and the terminal workflow phase.
// A Collector builds Evidence from stream.Event values; an Expect converts
// Evidence into eval.Check results.
//
// Contract with adjacent layers: the runtime's stream package defines the
// event vocabulary consumed here. Applications that expose goa-ai streams
// natively feed events straight into a Collector; applications that re-encode
// the stream over their own transport write a small adapter that maps their
// wire type back to stream events, compensating for any information their
// encoding dropped. Assertion semantics live entirely in this package so
// every evaluation suite shares one implementation.
package evidence

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Evidence is everything observed while executing one run tree: the root
	// run and the agent-as-tool child runs it spawned.
	Evidence struct {
		// RunID is the root workflow run whose stream produced this evidence.
		RunID string
		// SessionID is the logical session that owns the root run.
		SessionID string
		// Answer is the accumulated assistant reply text for the root run.
		Answer string
		// ToolCalls are every tool invocation in the run tree in canonical
		// causal order: each parent immediately precedes its nested
		// agent-as-tool calls (identified by ParentToolCallID) while root and
		// sibling order stays exact.
		ToolCalls []ToolCall
		// Confirmation is the pending operator confirmation when the run
		// stopped at an await_confirmation boundary.
		Confirmation *Confirmation
		// TerminalPhase is the root run's final workflow phase
		// (run.PhaseCompleted, run.PhaseFailed, or run.PhaseCanceled), or ""
		// when the stream ended without one.
		TerminalPhase run.Phase
		// TerminalFailure is the canonical failure payload when TerminalPhase
		// is run.PhaseFailed.
		TerminalFailure *run.Failure
	}

	// ToolCall pairs one tool invocation with its result.
	ToolCall struct {
		// Name is the fully qualified tool identifier, e.g.
		// "weather.forecast.get_forecast".
		Name tools.Ident
		// ToolCallID correlates the call with its result.
		ToolCallID string
		// ParentToolCallID is set when an agent-as-tool child run made the
		// call, identifying the parent tool call that spawned it.
		ParentToolCallID string
		// Args is the canonical JSON arguments payload.
		Args rawjson.Message
		// Result is the canonical JSON result payload, nil until the tool_end
		// event arrives or when the call failed.
		Result rawjson.Message
		// Bounds is the bounded-result metadata when the tool reported it.
		Bounds *agent.Bounds
		// Failure is the canonical failure classification when the call
		// failed, nil on success.
		Failure *planner.ToolFailure
		// Completed reports whether the tool_end event for this call was
		// observed.
		Completed bool
	}

	// Confirmation is a pending operator confirmation boundary.
	Confirmation struct {
		// ToolName is the tool whose execution awaits approval.
		ToolName tools.Ident
		// ToolCallID is the pending tool call awaiting the decision.
		ToolCallID string
		// Prompt is the operator-facing confirmation prompt.
		Prompt string
		// Payload is the canonical JSON arguments for the pending call.
		Payload rawjson.Message
	}
)

// Calls returns every tool call with the given name in causal order.
func (e *Evidence) Calls(name tools.Ident) []ToolCall {
	var calls []ToolCall
	for _, call := range e.ToolCalls {
		if call.Name == name {
			calls = append(calls, call)
		}
	}
	return calls
}

// causalOrder projects stream-ordered calls into canonical causal order: each
// parent immediately precedes its descendants while root and sibling order
// stays exact. This keeps exact trajectories independent of how concurrent
// child runs interleave on the stream.
func causalOrder(calls []ToolCall) ([]ToolCall, error) {
	children := make(map[string][]ToolCall)
	var roots []ToolCall
	for _, call := range calls {
		if call.ParentToolCallID == "" {
			roots = append(roots, call)
			continue
		}
		children[call.ParentToolCallID] = append(children[call.ParentToolCallID], call)
	}
	ordered := make([]ToolCall, 0, len(calls))
	var emit func(call ToolCall)
	emit = func(call ToolCall) {
		ordered = append(ordered, call)
		for _, child := range children[call.ToolCallID] {
			emit(child)
		}
	}
	for _, root := range roots {
		emit(root)
	}
	if len(ordered) != len(calls) {
		return nil, fmt.Errorf(
			"causal ordering lost calls: %d observed, %d reachable from roots (orphaned parent tool call IDs)",
			len(calls), len(ordered),
		)
	}
	return ordered, nil
}
