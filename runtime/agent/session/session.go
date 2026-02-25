// Package session defines durable session lifecycle and run metadata primitives.
//
// A Session is the first-class conversational container. Runs must always belong
// to a session. Session lifecycle is explicit: sessions are created and ended
// independently of run/workflow lifecycle.
package session

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/prompt"
)

type (
	// Session captures durable session lifecycle state.
	//
	// Contract:
	// - Session IDs are stable and caller-provided (typically owned by an application).
	// - Sessions are created explicitly (CreateSession) and ended explicitly (EndSession).
	// - Ended sessions are terminal: new runs must not start under an ended session.
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

	// RunMeta captures persistent metadata associated with a run execution.
	RunMeta struct {
		// AgentID identifies which agent processed the run.
		AgentID string
		// RunID is the durable workflow run identifier.
		RunID string
		// SessionID associates related runs (e.g., chat sessions).
		SessionID string
		// Status indicates the current lifecycle state.
		Status RunStatus
		// StartedAt records when the run began.
		StartedAt time.Time
		// UpdatedAt records when the run metadata was last updated.
		UpdatedAt time.Time
		// Labels stores caller- or policy-provided labels.
		Labels map[string]string
		// PromptRefs captures the unique prompt versions rendered during the run.
		//
		// The runtime appends to this slice when it observes PromptRendered hook
		// events, de-duplicating by (prompt_id, version). This enables downstream
		// consumers to correlate run outcomes with the exact prompt versions that
		// were used without re-scanning the run event log.
		PromptRefs []prompt.PromptRef
		// ChildRunIDs captures direct child runs linked from this run.
		//
		// Child runs are produced by agent-as-tool execution. Consumers that need
		// full prompt attribution should walk this graph to include descendants.
		ChildRunIDs []string
		// Metadata stores implementation-specific metadata (e.g., error codes).
		Metadata map[string]any
	}

	// Store persists session lifecycle state and run metadata.
	//
	// Store implementations must be durable: failures are surfaced to callers so
	// workflows can fail fast when session/run metadata is unavailable.
	Store interface {
		// CreateSession creates (or returns) an active session.
		//
		// Contract:
		// - Idempotent for active sessions: returns the existing session.
		// - Returns ErrSessionEnded when the session exists but is terminal.
		CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (Session, error)
		// LoadSession loads an existing session.
		// Returns ErrSessionNotFound when the session does not exist.
		LoadSession(ctx context.Context, sessionID string) (Session, error)
		// EndSession ends a session and returns its terminal state.
		// Idempotent: ending an already-ended session returns the stored session.
		EndSession(ctx context.Context, sessionID string, endedAt time.Time) (Session, error)

		// UpsertRun inserts or updates run metadata.
		UpsertRun(ctx context.Context, run RunMeta) error
		// LinkChildRun links a child run to a parent run atomically.
		//
		// Contract:
		// - parentRunID and child identifiers must be non-empty.
		// - The parent run must already exist, otherwise ErrRunNotFound is returned.
		// - Parent and child runs must belong to the same session.
		// - Child linkage is idempotent (duplicate links are ignored).
		// - The implementation must ensure no observer can observe a linked child ID
		//   without a corresponding child run record.
		LinkChildRun(ctx context.Context, parentRunID string, child RunMeta) error
		// LoadRun loads run metadata. Returns ErrRunNotFound when missing.
		LoadRun(ctx context.Context, runID string) (RunMeta, error)
		// ListRunsBySession lists runs for the given session. When statuses is
		// non-empty, only runs whose status matches one of the provided values
		// are returned.
		ListRunsBySession(ctx context.Context, sessionID string, statuses []RunStatus) ([]RunMeta, error)
	}

	// SessionStatus represents the lifecycle state of a session.
	SessionStatus string

	// RunStatus represents the lifecycle state of a run.
	RunStatus string
)

const (
	// StatusActive indicates the session is open for new runs.
	StatusActive SessionStatus = "active"
	// StatusEnded indicates the session is terminal and must not accept new runs.
	StatusEnded SessionStatus = "ended"

	// RunStatusPending indicates the run has been accepted but not started yet.
	RunStatusPending RunStatus = "pending"
	// RunStatusRunning indicates the run is actively executing.
	RunStatusRunning RunStatus = "running"
	// RunStatusPaused indicates the run is waiting for external input (pause/await).
	RunStatusPaused RunStatus = "paused"
	// RunStatusCompleted indicates the run finished successfully.
	RunStatusCompleted RunStatus = "completed"
	// RunStatusFailed indicates the run failed permanently.
	RunStatusFailed RunStatus = "failed"
	// RunStatusCanceled indicates the run was canceled externally.
	RunStatusCanceled RunStatus = "canceled"
)

var (
	// ErrSessionNotFound indicates a session does not exist in the store.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionEnded indicates a session exists but is ended.
	ErrSessionEnded = errors.New("session ended")
	// ErrRunNotFound indicates run metadata does not exist in the store.
	ErrRunNotFound = errors.New("run not found")
	// ErrParentRunIDRequired indicates a child-link operation is missing parent run ID.
	ErrParentRunIDRequired = errors.New("parent run id is required")
	// ErrChildRunIDRequired indicates a child-link operation is missing child run ID.
	ErrChildRunIDRequired = errors.New("child run id is required")
	// ErrChildAgentIDRequired indicates a child-link operation is missing child agent ID.
	ErrChildAgentIDRequired = errors.New("child agent id is required")
	// ErrChildSessionIDRequired indicates a child-link operation is missing child session ID.
	ErrChildSessionIDRequired = errors.New("child session id is required")
	// ErrChildStatusRequired indicates a child-link operation is missing child run status.
	ErrChildStatusRequired = errors.New("child status is required")
	// ErrRunSessionMismatch indicates parent and child runs belong to different sessions.
	ErrRunSessionMismatch = errors.New("parent and child runs must belong to the same session")
)

// ValidateChildRunLink validates required identifiers for Store.LinkChildRun input.
func ValidateChildRunLink(parentRunID string, child RunMeta) error {
	switch {
	case parentRunID == "":
		return ErrParentRunIDRequired
	case child.RunID == "":
		return ErrChildRunIDRequired
	case child.AgentID == "":
		return ErrChildAgentIDRequired
	case child.SessionID == "":
		return ErrChildSessionIDRequired
	case child.Status == "":
		return ErrChildStatusRequired
	default:
		return nil
	}
}
