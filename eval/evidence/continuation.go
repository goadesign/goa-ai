// This file transfers observed pending invocations across an application's
// accepted continuation. The application owns that product transition; the
// collector owns invocation identity, immutable evidence, and root-stream scope.

package evidence

import (
	"fmt"
	"maps"
)

type (
	// continuationContext is private proof produced only after observing the
	// root stream boundary. Public Evidence fields cannot author this context.
	continuationContext struct {
		rootRunID string
		sessionID string
		pending   []observedCall
		parents   map[string]string
	}
)

// NewContinuationCollector collects an accepted successor root using the pending
// invocations observed in previous. The application must first obtain that
// successor through its real continuation operation. This constructor performs
// no product action and never discovers a predecessor.
//
// previous must be a collector-produced snapshot taken after its root stream
// ended. The new collector is bound to successorRootRunID and the same logical
// session. Prior invocations retain their original root-stream scope through
// any number of accepted continuations. Their results enter ToolCompletions,
// never the successor's new-invocation ToolCalls.
func NewContinuationCollector(previous *Evidence, successorRootRunID string) (*Collector, error) {
	if previous == nil || previous.continuation == nil {
		return nil, fmt.Errorf("continuation requires evidence from an observed root stream boundary")
	}
	context := previous.continuation
	if context.rootRunID == "" || context.sessionID == "" {
		return nil, fmt.Errorf("continuation requires an observed root run and logical session")
	}
	if previous.RunID != context.rootRunID || previous.SessionID != context.sessionID {
		return nil, fmt.Errorf("previous evidence root/session differs from its observed continuation context")
	}
	if successorRootRunID == "" || successorRootRunID == context.rootRunID {
		return nil, fmt.Errorf("continuation requires a distinct nonempty successor root run")
	}
	c := NewCollector()
	c.evidence.RunID = successorRootRunID
	c.evidence.SessionID = context.sessionID
	c.expectRoot = true
	c.priorParents = maps.Clone(context.parents)
	for _, pending := range context.pending {
		observed := &observedCall{
			rootRunID: pending.rootRunID,
			call:      cloneToolCall(pending.call),
		}
		c.byCallID[observed.call.ToolCallID] = observed
		c.calls = append(c.calls, observed)
	}
	return c, nil
}

// continuationContext retains only unresolved calls and the already-observed
// ancestry needed to recognize their parents in a successor.
func (c *Collector) continuationContext() (*continuationContext, error) {
	context := &continuationContext{
		rootRunID: c.evidence.RunID,
		sessionID: c.evidence.SessionID,
		parents:   make(map[string]string),
	}
	for _, observed := range c.calls {
		if observed.call.Completed {
			continue
		}
		context.pending = append(context.pending, observedCall{
			rootRunID: observed.rootRunID,
			call:      cloneToolCall(observed.call),
		})
		if err := c.retainParents(observed.call.ToolCallID, context.parents); err != nil {
			return nil, err
		}
	}
	return context, nil
}

// retainParents copies observed ancestry without retaining completed payloads or
// inventing a current invocation for an earlier parent.
func (c *Collector) retainParents(callID string, parents map[string]string) error {
	for callID != "" {
		if _, retained := parents[callID]; retained {
			return nil
		}
		var parentID string
		if observed, exists := c.byCallID[callID]; exists {
			parentID = observed.call.ParentToolCallID
		} else {
			var exists bool
			parentID, exists = c.priorParents[callID]
			if !exists {
				return fmt.Errorf("continuation ancestry references unobserved tool call %s", callID)
			}
		}
		parents[callID] = parentID
		callID = parentID
	}
	return nil
}
