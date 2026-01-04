package run

import (
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Snapshot is a derived view of a run computed by replaying the run event log.
	//
	// Snapshots are not stored directly; they are recomputed from the canonical
	// append-only run log.
	Snapshot struct {
		// RunID uniquely identifies the durable workflow run.
		RunID string
		// AgentID identifies the agent that owns the run.
		AgentID agent.Ident
		// SessionID groups related runs into a logical session.
		SessionID string
		// TurnID groups events for a single conversational turn.
		TurnID string

		// Status is the coarse-grained run lifecycle status derived from events.
		Status Status
		// Phase is the current execution phase derived from events.
		Phase Phase

		// StartedAt is the timestamp of the first observed run event.
		StartedAt time.Time
		// UpdatedAt is the timestamp of the last observed run event.
		UpdatedAt time.Time

		// LastAssistantMessage is the most recent assistant message emitted by the run.
		LastAssistantMessage string

		// Await describes the current await state when the run is paused awaiting input.
		Await *AwaitSnapshot

		// ToolCalls summarizes observed tool calls (scheduled, updated, completed).
		ToolCalls []*ToolCallSnapshot

		// ChildRuns links nested agent runs (agent-as-tool) started during this run.
		ChildRuns []*ChildRunLink
	}

	// AwaitSnapshot describes the latest await state derived from run events.
	AwaitSnapshot struct {
		// Kind identifies which await variant is active.
		Kind string
		// ID is the await correlation identifier.
		ID string
		// ToolName is the tool that triggered the await when applicable.
		ToolName tools.Ident
		// ToolCallID is the tool call that triggered the await when applicable.
		ToolCallID string
		// Question is the clarification question when awaiting clarification.
		Question string
		// Title is the confirmation title when awaiting confirmation.
		Title string
		// Prompt is the confirmation prompt when awaiting confirmation.
		Prompt string
		// ItemCount is the number of external tool items awaited.
		ItemCount int
	}

	// ToolCallSnapshot summarizes the state of a tool invocation derived from events.
	ToolCallSnapshot struct {
		// ToolCallID uniquely identifies the tool invocation.
		ToolCallID string
		// ParentToolCallID identifies the parent tool call when this call is nested.
		ParentToolCallID string
		// ToolName identifies the executed tool.
		ToolName tools.Ident
		// ScheduledAt is the timestamp of the tool scheduling event.
		ScheduledAt time.Time
		// CompletedAt is the timestamp of the tool result event.
		CompletedAt time.Time
		// Duration is the tool execution duration when the result is observed.
		Duration time.Duration
		// ErrorSummary is a human-readable error message when the tool failed.
		ErrorSummary string
		// ExpectedChildrenTotal tracks the expected number of child tool calls for parent tools.
		ExpectedChildrenTotal int
		// ObservedChildrenTotal tracks how many direct child tool calls were observed as scheduled.
		ObservedChildrenTotal int
	}

	// ChildRunLink links a parent tool call to a nested agent run.
	ChildRunLink struct {
		// ToolName is the agent-tool identifier that started the child run.
		ToolName tools.Ident
		// ToolCallID is the parent tool call identifier.
		ToolCallID string
		// ChildRunID is the workflow run identifier of the nested agent execution.
		ChildRunID string
		// ChildAgentID identifies the agent that executed the child run.
		ChildAgentID agent.Ident
	}
)
