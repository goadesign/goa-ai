// This file implements the Collector, the state machine that builds Evidence
// from a run tree's stream events. The stream package owns the event
// vocabulary; the Collector owns correlation (tool results to calls by tool
// call ID), root-run scoping of the answer and terminal phase, and the causal
// ordering applied when collection finishes.

package evidence

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Collector accumulates Evidence from stream events. Feed it every event
// observed for one run tree — the root run plus its agent-as-tool child runs
// — in stream order. The root run is the run of the first consumed event
// carrying a run ID, which on a run-scoped stream is the root workflow's
// first lifecycle event.
//
// Collector is not safe for concurrent use; a run's stream is ordered, so
// consume it from one goroutine.
type Collector struct {
	evidence Evidence
	byCallID map[string]int
	done     bool
}

// NewCollector returns an empty collector.
func NewCollector() *Collector {
	return &Collector{byCallID: make(map[string]int)}
}

// Consume applies one stream event to the evidence. It returns an error when
// the event violates the stream contract: a duplicate tool call ID or a tool
// result for a call that was never started. Event kinds that carry no
// evidence (thoughts, deltas, usage, session markers) are ignored.
func (c *Collector) Consume(event stream.Event) error {
	if c.evidence.RunID == "" && event.RunID() != "" {
		c.evidence.RunID = event.RunID()
		c.evidence.SessionID = event.SessionID()
	}
	switch e := event.(type) {
	case stream.ToolStart:
		if _, exists := c.byCallID[e.Data.ToolCallID]; exists {
			return fmt.Errorf("duplicate tool_start for tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		c.byCallID[e.Data.ToolCallID] = len(c.evidence.ToolCalls)
		c.evidence.ToolCalls = append(c.evidence.ToolCalls, ToolCall{
			Name:             tools.Ident(e.Data.ToolName),
			ToolCallID:       e.Data.ToolCallID,
			ParentToolCallID: e.Data.ParentToolCallID,
			Args:             e.Data.Payload,
		})
	case stream.ToolEnd:
		index, ok := c.byCallID[e.Data.ToolCallID]
		if !ok {
			return fmt.Errorf("tool_end for unknown tool call %s (%s)", e.Data.ToolCallID, e.Data.ToolName)
		}
		call := &c.evidence.ToolCalls[index]
		call.Result = e.Data.Result
		call.Bounds = e.Data.Bounds
		call.Failure = e.Data.Failure
		call.Completed = true
	case stream.AssistantReply:
		if e.RunID() == c.evidence.RunID {
			c.evidence.Answer += e.Data.Text
		}
	case stream.Workflow:
		if e.RunID() != c.evidence.RunID {
			return nil
		}
		switch e.Data.Phase {
		case "completed", "failed", "canceled":
			c.evidence.TerminalPhase = e.Data.Phase
			c.evidence.TerminalFailure = e.Data.Failure
		}
	case stream.AwaitConfirmation:
		c.evidence.Confirmation = &Confirmation{
			ToolName:   tools.Ident(e.Data.ToolName),
			ToolCallID: e.Data.ToolCallID,
			Prompt:     e.Data.Prompt,
			Payload:    e.Data.Payload,
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

// Finish validates the collected calls and returns the evidence with tool
// calls in canonical causal order. It fails when a call references a parent
// tool call that was never observed.
func (c *Collector) Finish() (*Evidence, error) {
	ordered, err := causalOrder(c.evidence.ToolCalls)
	if err != nil {
		return nil, err
	}
	c.evidence.ToolCalls = ordered
	return &c.evidence, nil
}
