// Package hooks implements fan-out hooks for runtime observability and memory.
//
// The hooks package provides an event bus that enables the runtime to publish
// lifecycle events (workflow start/completion, tool execution, planner notes)
// to multiple subscribers. This decouples event producers (workflows, planners,
// tool executors) from consumers (memory stores, streaming sinks, telemetry).
//
// The primary types are:
//   - Bus: the event bus interface for publishing and subscribing
//   - Event: the event payload carrying type, IDs, and metadata
//   - Subscriber: the interface implementations must satisfy to receive events
//   - Subscription: a handle for unregistering from the bus
//
// Typical usage pattern:
//
//	bus := hooks.NewBus()
//
//	// Register a subscriber
//	sub := hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
//	    if evt.Type == hooks.WorkflowStarted {
//	        fmt.Printf("Workflow %s started\n", evt.RunID)
//	    }
//	    return nil
//	})
//	subscription, _ := bus.Register(sub)
//	defer subscription.Close()
//
//	// Publish events
//	bus.Publish(ctx, hooks.Event{
//	    Type:    hooks.WorkflowStarted,
//	    RunID:   "run-123",
//	    AgentID: "chat-agent",
//	})
package hooks

import "context"

type (
	// SubscriberFunc is an adapter that allows ordinary functions to act as
	// Subscribers. This is useful for quick prototypes, tests, or simple handlers
	// that don't require stateful subscriber implementations.
	//
	// Example:
	//
	//	sub := hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
	//	    log.Printf("Received %s for run %s", evt.Type, evt.RunID)
	//	    return nil
	//	})
	//	subscription, _ := bus.Register(sub)
	SubscriberFunc func(ctx context.Context, event Event) error
)

// EventType enumerates well-known runtime events broadcast on the hook bus.
// Each type corresponds to a specific phase in the agent workflow lifecycle.
type EventType string

const (
	// RunStarted fires when a run begins execution. The Payload
	// typically contains the initial RunContext and input parameters.
	RunStarted EventType = "run_started"

	// RunCompleted fires after a run finishes, whether successfully
	// or with a failure. The Payload contains final status and any error details.
	RunCompleted EventType = "run_completed"

	// RunPaused fires when execution is suspended awaiting external action.
	RunPaused EventType = "run_paused"

	// RunResumed fires when a previously paused run resumes execution.
	RunResumed EventType = "run_resumed"

	// ToolCallScheduled fires when the runtime schedules a tool activity for
	// execution. The Payload contains the tool name, arguments, and queue metadata.
	ToolCallScheduled EventType = "tool_call_scheduled"

	// ToolResultReceived fires when a tool activity completes and returns a
	// result or error. The Payload contains the tool name, result, duration,
	// and any execution errors.
	ToolResultReceived EventType = "tool_result_received"

	// ToolCallUpdated fires when a tool call's metadata is updated, typically
	// when a parent tool (agent-as-tool) dynamically discovers additional child
	// tools. The Payload contains the updated expected child count.
	ToolCallUpdated EventType = "tool_call_updated"

	// PlannerNote fires when the planner emits an annotation or intermediate
	// thought. The Payload contains the note text and optional labels for
	// categorization.
	PlannerNote EventType = "planner_note"

	// AssistantMessage fires when a final assistant response is produced,
	// indicating the workflow is completing with a user-facing message. The
	// Payload contains the message content and any structured output.
	AssistantMessage EventType = "assistant_message"

	// RetryHintIssued fires when the planner or runtime suggests a retry
	// policy change, such as disabling a failing tool or adjusting caps.
	// The Payload contains the hint reason and affected tool metadata.
	RetryHintIssued EventType = "retry_hint"

	// MemoryAppended fires when new memory entries are successfully persisted
	// to the memory store. The Payload may contain the event IDs or counts
	// for observability.
	MemoryAppended EventType = "memory_appended"

	// PolicyDecision fires when a policy engine returns a decision for the turn.
	PolicyDecision EventType = "policy_decision"
)

// HandleEvent implements Subscriber by invoking the function.
func (fn SubscriberFunc) HandleEvent(ctx context.Context, event Event) error {
	return fn(ctx, event)
}
