// Package session defines durable session lifecycle and run metadata primitives.
//
// A Session groups related conversational runs. Sessionful runs belong to one
// session, while explicit one-shot runs do not. Session lifecycle is independent
// of workflow lifecycle.
package session

import (
	"errors"
	"time"
)

type (
	// Session captures durable session lifecycle state.
	//
	// Contract:
	// - Session IDs are stable and caller-provided (typically owned by an application).
	// - Sessions are created explicitly (CreateSession) and ended explicitly (EndSession).
	// - Ended sessions allow no new workflow work. An engine-accepted workflow
	//   records a canceled run and stops before planner or tool execution.
	Session struct {
		// ID is the durable identifier of the session.
		ID string
		// Status is the current session lifecycle state.
		Status SessionStatus
		// CreatedAt records when the session was created.
		CreatedAt time.Time
		// EndedAt is set when the session is ended.
		EndedAt *time.Time
	}

	// RunMeta captures persistent lifecycle facts for a run execution.
	RunMeta struct {
		// AgentID identifies which agent processed the run.
		AgentID string
		// RunID is the durable workflow run identifier.
		RunID string
		// SessionID associates related runs (e.g., chat sessions).
		SessionID string
		// ParentRunID identifies the parent for an agent-as-tool child run.
		// Root runs leave it empty.
		ParentRunID string
		// Status indicates the current lifecycle state.
		Status RunStatus
		// StartOutcome records whether the accepted workflow was allowed to
		// run. This value never changes, so retrying the start returns the same
		// decision after the run has completed or the session has ended.
		StartOutcome RunStartOutcome
		// StartedAt records when the run began.
		StartedAt time.Time
		// UpdatedAt records when the run metadata was last updated.
		UpdatedAt time.Time
		// Labels stores caller- or policy-provided labels.
		Labels map[string]string
		// CancellationReason records the first runtime cancellation request.
		CancellationReason string
	}

	// RunStart contains the immutable identity, start time, and labels written
	// by the first activity of a workflow that the engine has accepted.
	RunStart struct {
		// AgentID identifies the agent executing the workflow.
		AgentID string
		// RunID is the caller-supplied workflow identifier.
		RunID string
		// SessionID identifies the session that owns the workflow.
		SessionID string
		// ParentRunID identifies the parent workflow for a child start.
		ParentRunID string
		// PredecessorRunID identifies the suspended run whose saved state this
		// workflow restores. Initial and one-shot starts leave it empty.
		PredecessorRunID string
		// StartedAt is the workflow-owned start time. It uses millisecond
		// precision to match the runtime record contract.
		StartedAt time.Time
		// Labels are the run labels visible to lifecycle consumers.
		Labels map[string]string
	}

	// RunSuspension is the opaque runtime-owned value needed to continue one
	// completed run. Runtime stores preserve Data byte-for-byte and use ID to
	// reject conflicting writes for the same run.
	RunSuspension struct {
		// ID identifies the exact suspension encoded in Data.
		ID string
		// Data contains the canonical JSON encoding owned by the agent runtime.
		Data []byte
	}

	// SessionStatus represents the lifecycle state of a session.
	SessionStatus string

	// RunStatus represents the lifecycle state of a run.
	RunStatus string

	// RunStartOutcome records the decision made when an accepted workflow
	// first writes its durable run record.
	RunStartOutcome string
)

const (
	// StatusActive indicates the session is open for new runs.
	StatusActive SessionStatus = "active"
	// StatusEnded indicates the session is terminal and must not accept new runs.
	StatusEnded SessionStatus = "ended"

	// RunStatusRunning indicates the run is actively executing.
	RunStatusRunning RunStatus = "running"
	// RunStatusSuspended indicates the workflow ended and can continue in a new run.
	RunStatusSuspended RunStatus = "suspended"
	// RunStatusCompleted indicates the run finished successfully.
	RunStatusCompleted RunStatus = "completed"
	// RunStatusFailed indicates the run failed permanently.
	RunStatusFailed RunStatus = "failed"
	// RunStatusCanceled indicates the run was canceled externally.
	RunStatusCanceled RunStatus = "canceled"

	// RunStartProceed allows the workflow to plan and execute tools.
	RunStartProceed RunStartOutcome = "proceed"
	// RunStartStop prevents the workflow from doing work because its session
	// had already ended.
	RunStartStop RunStartOutcome = "stop"
)

var (
	// ErrSessionNotFound indicates a session does not exist in the store.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionEnded indicates a session exists but is ended.
	ErrSessionEnded = errors.New("session ended")
	// ErrSessionPurged indicates a session ID was permanently removed and cannot
	// be reused for another session.
	ErrSessionPurged = errors.New("session purged")
	// ErrSessionActive indicates an active session cannot be purged.
	ErrSessionActive = errors.New("session is active")
	// ErrSessionHasActiveRuns indicates an ended session still owns work that
	// has not reached a terminal status.
	ErrSessionHasActiveRuns = errors.New("session has active runs")
	// ErrRunNotFound indicates run metadata does not exist in the store.
	ErrRunNotFound = errors.New("run not found")
	// ErrRunConflict indicates a run ID already owns different immutable start
	// identity.
	ErrRunConflict = errors.New("run metadata conflict")
	// ErrRunTerminalConflict indicates a run already owns another terminal
	// outcome.
	ErrRunTerminalConflict = errors.New("run terminal outcome conflict")
	// ErrRunCancellationConflict indicates a run already owns another
	// cancellation reason.
	ErrRunCancellationConflict = errors.New("run cancellation reason conflict")
	// ErrRunNotActive indicates an operation requires a non-terminal run.
	ErrRunNotActive = errors.New("run is not active")
	// ErrRunSuspensionNotFound indicates the run has no stored suspension.
	ErrRunSuspensionNotFound = errors.New("run suspension not found")
	// ErrRunSuspensionConflict indicates a run already owns another suspension.
	ErrRunSuspensionConflict = errors.New("run suspension conflict")
	// ErrParentRunIDRequired indicates a child-link operation is missing parent run ID.
	ErrParentRunIDRequired = errors.New("parent run id is required")
	// ErrChildRunIDRequired indicates a child-link operation is missing child run ID.
	ErrChildRunIDRequired = errors.New("child run id is required")
	// ErrChildAgentIDRequired indicates a child-link operation is missing child agent ID.
	ErrChildAgentIDRequired = errors.New("child agent id is required")
	// ErrChildSessionIDRequired indicates a child-link operation is missing child session ID.
	ErrChildSessionIDRequired = errors.New("child session id is required")
	// ErrRunSessionMismatch indicates parent and child runs belong to different sessions.
	ErrRunSessionMismatch = errors.New("parent and child runs must belong to the same session")
)

// ValidateRunStart validates the immutable values required by a workflow-owned
// session run start. Child starts additionally require ParentRunID.
func ValidateRunStart(start RunStart, child bool) error {
	switch {
	case child && start.RunID == "":
		return ErrChildRunIDRequired
	case start.RunID == "":
		return errors.New("run id is required")
	case child && start.AgentID == "":
		return ErrChildAgentIDRequired
	case start.AgentID == "":
		return errors.New("agent id is required")
	case child && start.SessionID == "":
		return ErrChildSessionIDRequired
	case start.SessionID == "":
		return errors.New("session id is required")
	case child && start.ParentRunID == "":
		return ErrParentRunIDRequired
	case !child && start.ParentRunID != "":
		return errors.New("root run cannot have parent run id")
	case start.PredecessorRunID == start.RunID:
		return errors.New("predecessor run id must differ from run id")
	case start.StartedAt.IsZero():
		return errors.New("started_at is required")
	case !start.StartedAt.Equal(start.StartedAt.Truncate(time.Millisecond)):
		return errors.New("started_at must use millisecond precision")
	default:
		return nil
	}
}

// IsTerminalRunStatus reports whether status records a workflow that has
// permanently stopped. Stores use this classification to prevent one terminal
// outcome from overwriting another.
func IsTerminalRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusSuspended, RunStatusCompleted, RunStatusFailed, RunStatusCanceled:
		return true
	case RunStatusRunning:
		return false
	default:
		return false
	}
}
