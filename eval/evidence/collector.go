// This file implements the Collector, the state machine that builds Evidence
// from a run tree's stream events. The stream package owns the event
// vocabulary; the Collector owns correlation (tool results to calls by tool
// call ID), root-run scoping of the answer and terminal phase, and the causal
// ordering applied when collection finishes.

package evidence

import (
	"bytes"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Collector accumulates Evidence from stream events. Feed it every event
	// observed for one run tree — the root run plus its agent-as-tool child runs
	// — in stream order. The root run is the run of the first consumed event
	// carrying a run ID, which on a run-scoped stream is the root workflow's
	// first lifecycle event.
	//
	// Collector is not safe for concurrent use; a run's stream is ordered, so
	// consume it from one goroutine.
	Collector struct {
		evidence     Evidence
		calls        []*observedCall
		byCallID     map[string]*observedCall
		priorParents map[string]string
		done         bool
		expectRoot   bool
	}

	// observedCall owns an invocation independently of the public snapshots.
	observedCall struct {
		rootRunID string
		call      ToolCall
	}
)

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{byCallID: make(map[string]*observedCall)}
}

// Consume applies one stream event to the evidence. It returns an error when
// the event violates the stream contract: an unknown or duplicate invocation,
// a repeated completion, or a result with a different tool name or parent.
// Event kinds that carry no evidence (thoughts, deltas, usage, session markers)
// are ignored.
func (c *Collector) Consume(event stream.Event) error {
	if c.expectRoot && event.RunID() != "" {
		if event.RunID() != c.evidence.RunID || event.SessionID() != c.evidence.SessionID {
			return fmt.Errorf("continuation stream root/session does not match the accepted successor")
		}
		c.expectRoot = false
	}
	if c.evidence.RunID == "" && event.RunID() != "" {
		c.evidence.RunID = event.RunID()
		c.evidence.SessionID = event.SessionID()
	}
	switch e := event.(type) {
	case stream.ToolStart:
		if c.evidence.RunID == "" {
			return fmt.Errorf("tool_start requires an observed root run")
		}
		_, exists := c.byCallID[e.Data.ToolCallID]
		_, priorParent := c.priorParents[e.Data.ToolCallID]
		if exists || priorParent {
			return fmt.Errorf("duplicate tool_start for tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		observed := &observedCall{
			rootRunID: c.evidence.RunID,
			call: ToolCall{
				Name:             tools.Ident(e.Data.ToolName),
				ToolCallID:       e.Data.ToolCallID,
				ParentToolCallID: e.Data.ParentToolCallID,
				Args:             bytes.Clone(e.Data.Payload),
			},
		}
		c.byCallID[e.Data.ToolCallID] = observed
		c.calls = append(c.calls, observed)
	case stream.ToolEnd:
		observed, ok := c.byCallID[e.Data.ToolCallID]
		if !ok {
			return fmt.Errorf("tool_end for unknown tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		call := &observed.call
		if string(call.Name) != e.Data.ToolName || call.ParentToolCallID != e.Data.ParentToolCallID {
			return fmt.Errorf("tool_end name or parent does not match tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		if call.Completed {
			return fmt.Errorf("duplicate tool_end for tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		call.Result = bytes.Clone(e.Data.Result)
		call.Bounds = agent.CloneBounds(e.Data.Bounds)
		call.Failure = planner.CloneToolFailure(e.Data.Failure)
		call.Completed = true
		c.evidence.ToolCompletions = append(c.evidence.ToolCompletions, ToolCompletion{
			InvocationRootRunID: observed.rootRunID,
			Call:                *call,
		})
	case stream.AssistantReply:
		if e.RunID() == c.evidence.RunID {
			c.evidence.Answer += e.Data.Text
		}
	case stream.Workflow:
		if e.RunID() != c.evidence.RunID {
			return nil
		}
		phase := run.Phase(e.Data.Phase)
		if phase == run.PhaseCompleted || phase == run.PhaseFailed || phase == run.PhaseCanceled {
			c.evidence.TerminalPhase = phase
			if e.Data.Failure != nil {
				failure := *e.Data.Failure
				c.evidence.TerminalFailure = &failure
			} else {
				c.evidence.TerminalFailure = nil
			}
		}
	case stream.AwaitConfirmation:
		c.evidence.Confirmation = &Confirmation{
			ToolName:   tools.Ident(e.Data.ToolName),
			ToolCallID: e.Data.ToolCallID,
			Prompt:     e.Data.Prompt,
			Payload:    bytes.Clone(e.Data.Payload),
		}
	case stream.RunStreamEnd:
		if e.RunID() == c.evidence.RunID {
			c.done = true
		}
	}
	return nil
}

// Done reports whether the root run's stream boundary marker was observed.
// The runtime emits run_stream_end after all stream-visible events for the
// run, so Done is the signal to stop consuming without relying on timers.
// Trailing child-run markers consumed after Done are still processed.
func (c *Collector) Done() bool {
	return c.done
}

// Finish returns an independent snapshot with new invocations in canonical
// causal order and results in their observation order. A known parent from an
// accepted continuation can anchor a new child without becoming a new invocation.
// Unknown parents fail. Finishing never reorders or exposes the collector's
// mutable correlation state.
func (c *Collector) Finish() (*Evidence, error) {
	if c.expectRoot {
		return nil, fmt.Errorf("continuation stream has not identified the accepted successor")
	}
	snapshot := c.evidence
	calls := make([]ToolCall, 0, len(c.calls))
	for _, observed := range c.calls {
		if observed.rootRunID == c.evidence.RunID {
			calls = append(calls, cloneToolCall(observed.call))
		}
	}
	ordered, err := causalOrder(calls, c.priorParents)
	if err != nil {
		return nil, err
	}
	snapshot.ToolCalls = ordered
	snapshot.ToolCompletions = make([]ToolCompletion, len(c.evidence.ToolCompletions))
	for i, completed := range c.evidence.ToolCompletions {
		snapshot.ToolCompletions[i] = ToolCompletion{
			InvocationRootRunID: completed.InvocationRootRunID,
			Call:                cloneToolCall(completed.Call),
		}
	}
	if c.evidence.Confirmation != nil {
		confirmation := *c.evidence.Confirmation
		confirmation.Payload = bytes.Clone(confirmation.Payload)
		snapshot.Confirmation = &confirmation
	}
	if c.evidence.TerminalFailure != nil {
		failure := *c.evidence.TerminalFailure
		snapshot.TerminalFailure = &failure
	}
	if c.done {
		context, err := c.continuationContext()
		if err != nil {
			return nil, err
		}
		snapshot.continuation = context
	}
	return &snapshot, nil
}

// cloneToolCall gives each public snapshot independent ownership of mutable
// request, result, bounds, and failure data.
func cloneToolCall(call ToolCall) ToolCall {
	call.Args = bytes.Clone(call.Args)
	call.Result = bytes.Clone(call.Result)
	call.Bounds = agent.CloneBounds(call.Bounds)
	call.Failure = planner.CloneToolFailure(call.Failure)
	return call
}
