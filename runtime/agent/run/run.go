// Package run defines primitives for tracking agent run executions.
//
// # Core Concepts
//
// RunID (Infrastructure Layer):
//   - Represents a single durable workflow execution (e.g., Temporal WorkflowID)
//   - Used for workflow operations: replay, cancellation, history, observability
//   - Must be globally unique across all workflow executions
//   - Lifespan: From workflow start to completion (seconds to hours)
//
// TurnID (Application Layer):
//   - Represents a conversational turn (user message → agent response)
//   - Used for conversation tracking, UI rendering, session timeline
//   - Unique within a session, groups events for display
//   - Lifespan: Logical grouping that may span multiple workflow executions
//
// SessionID (Conversation Layer):
//   - Groups related runs/turns into a conversation or interaction thread
//   - Used for context accumulation across multiple turns
//   - Examples: chat session, research task, multi-step workflow
//
// Relationship Examples:
//
//	Simple chat (1 turn = 1 run):
//	  Session "chat-123"
//	    └─ Turn "turn-1" → Run "chat-123-run-1"
//	    └─ Turn "turn-2" → Run "chat-123-run-2"
//
//	Interrupted execution (1 turn = multiple runs):
//	  Session "task-456"
//	    └─ Turn "turn-1" → Run "task-456-run-1" (interrupted)
//	                    → Run "task-456-run-1-resumed" (same turn, new workflow)
//
//	Streaming with tool discovery (1 turn = 1 run, multiple events):
//	  Session "research-789"
//	    └─ Turn "turn-1" → Run "research-789-run-1"
//	       ├─ Event seq=1: "Searching papers..."
//	       ├─ Event seq=2: "Fetching papers..."
//	       └─ Event seq=3: "Analyzing..."
package run

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Context carries execution metadata for the current run invocation.
	// It is passed through the system during workflow execution and contains
	// the identifiers, labels, and constraints active for this specific
	// invocation attempt.
	Context struct {
		// RunID uniquely identifies the durable workflow run (infrastructure layer).
		// This corresponds to the workflow engine's execution identifier (e.g.,
		// Temporal WorkflowID). Used for workflow operations, replay, and observability.
		RunID string

		// ParentToolCallID identifies the parent tool call when this run represents a
		// nested agent execution (agent-as-tool). Empty for top-level runs. Used to
		// correlate ToolCallUpdated events and propagate parent-child relationships.
		ParentToolCallID string

		// ParentRunID identifies the run that scheduled this nested execution. Empty for
		// top-level runs. When set, tool events emitted by this run can be attributed to
		// the parent run for streaming/UI purposes.
		ParentRunID string

		// ParentAgentID identifies the agent that invoked this nested execution. Empty
		// for top-level runs. When set alongside ParentRunID, tool events can retain the
		// parent agent identity even though execution occurs in a child agent.
		ParentAgentID agent.Ident

		// SessionID associates related runs into a conversation or interaction thread.
		// Multiple turns in a chat session share the same SessionID. Optional.
		SessionID string

		// TurnID identifies a conversational turn within a session (application layer).
		// Optional. When set, groups all events produced during this turn for UI
		// rendering and conversation tracking. Multiple runs may share the same TurnID
		// if a turn requires pause/resume or retry with a new workflow execution.
		// Format: typically "turn-1", "turn-2", etc. within a session.
		TurnID string

		// Tool identifies the fully-qualified tool name when this run is a nested
		// agent-as-tool execution. For top-level runs (not invoked via a parent tool),
		// Tool is empty. Planners may use this to select method-specific prompts.
		// Format: "<service>.<toolset>.<tool>".
		Tool tools.Ident

		// ToolArgs carries the original JSON arguments for the parent tool when this run
		// is an agent-as-tool execution. Nil for top-level runs. Nested agent planners
		// can use this structured input to render method-specific prompts without
		// reparsing free-form messages.
		ToolArgs json.RawMessage

		// Attempt counts how many times the run has been attempted/resumed.
		Attempt int

		// Labels carries caller-provided metadata (tenant, priority, etc.).
		Labels map[string]string

		// MaxDuration encodes the wall-clock budget remaining (string form for prompts/telemetry).
		MaxDuration string
	}

	// Handle is a lightweight handle to a run, used for linking parent and
	// child runs in planner contracts and streaming surfaces. It intentionally
	// omits transport/engine details and focuses on logical identity.
	Handle struct {
		// RunID uniquely identifies the durable workflow run.
		RunID string
		// AgentID identifies the agent that owns this run.
		AgentID agent.Ident
		// ParentRunID identifies the run that scheduled this run when used for
		// nested agent-as-tool execution. Empty for top-level runs.
		ParentRunID string
		// ParentToolCallID identifies the parent tool call that created this
		// run when used for nested agent-as-tool execution. Empty for
		// top-level runs.
		ParentToolCallID string
	}

	// Record captures persistent metadata associated with an agent run execution.
	// This is the durable record stored for observability and lifecycle tracking.
	// Each record represents a single workflow invocation and can be associated
	// with a session via SessionID for grouping related runs (e.g., multi-turn
	// conversations).
	Record struct {
		// AgentID identifies which agent processed the run.
		AgentID agent.Ident
		// RunID is the durable workflow run identifier (workflow execution ID).
		RunID string
		// SessionID associates related runs into a conversation thread (optional).
		SessionID string
		// TurnID identifies the conversational turn within the session (optional).
		// May be shared across multiple runs if a turn is interrupted and resumed.
		TurnID string
		// Status indicates the current lifecycle state.
		Status Status
		// StartedAt records when the run began.
		StartedAt time.Time
		// UpdatedAt records when the run metadata was last updated.
		UpdatedAt time.Time
		// Labels stores caller- or policy-provided labels.
		Labels map[string]string
		// Metadata stores implementation-specific metadata (e.g., error codes).
		Metadata map[string]any
	}

	// Store persists run metadata for observability and lookup.
	Store interface {
		Upsert(ctx context.Context, record Record) error
		Load(ctx context.Context, runID string) (Record, error)
	}

	// Status represents the coarse-grained lifecycle state of a run.
	Status string

	// Phase represents a finer-grained lifecycle phase for a run. Phases track
	// where a run is in its execution loop (prompted, planning, executing
	// tools, synthesizing, or in a terminal state). Phases are intended for
	// streaming/UX surfaces and do not replace Status, which is used for
	// durable run metadata.
	Phase string
)

var (
	// ErrNotFound indicates that no run record exists for the given identifier.
	// Callers use this to distinguish between missing runs and other failures
	// when querying run status or metadata.
	ErrNotFound = errors.New("run not found")
)

const (
	// StatusPending indicates the run has been accepted but not started yet.
	StatusPending Status = "pending"
	// StatusRunning indicates the run is actively executing.
	StatusRunning Status = "running"
	// StatusCompleted indicates the run finished successfully.
	StatusCompleted Status = "completed"
	// StatusFailed indicates the run failed permanently.
	StatusFailed Status = "failed"
	// StatusCanceled indicates the run was canceled externally.
	StatusCanceled Status = "canceled"
	// StatusPaused indicates execution is paused awaiting external intervention.
	StatusPaused Status = "paused"

	// PhasePrompted indicates that input has been received and the run is
	// about to begin planning.
	PhasePrompted Phase = "prompted"
	// PhasePlanning indicates that the planner is deciding whether and how to
	// call tools or answer directly.
	PhasePlanning Phase = "planning"
	// PhaseExecutingTools indicates that tools (including nested agents) are
	// currently executing.
	PhaseExecutingTools Phase = "executing_tools"
	// PhaseSynthesizing indicates that the planner is synthesizing a final
	// answer without scheduling additional tools.
	PhaseSynthesizing Phase = "synthesizing"
	// PhaseCompleted indicates the run has completed successfully.
	PhaseCompleted Phase = "completed"
	// PhaseFailed indicates the run has failed.
	PhaseFailed Phase = "failed"
	// PhaseCanceled indicates the run was canceled.
	PhaseCanceled Phase = "canceled"
)
