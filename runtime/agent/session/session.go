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
)
