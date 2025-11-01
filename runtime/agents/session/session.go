// Package session defines session and run tracking primitives.
package session

import (
	"context"
	"time"
)

type (
	// RunContext carries execution metadata for the current run invocation.
	// It is passed through the system during workflow execution and contains
	// the identifiers, labels, and constraints active for this specific
	// invocation attempt.
	RunContext struct {
		// RunID uniquely identifies the durable workflow run.
		RunID string
		// Attempt counts how many times the run has been attempted/resumed.
		Attempt int
		// Labels carries caller-provided metadata (tenant, priority, etc.).
		Labels map[string]string
		// MaxDuration encodes the wall-clock budget remaining (string form for prompts/telemetry).
		MaxDuration string
	}

	// Run captures persistent metadata associated with an agent execution.
	// This is the record stored in the session store for observability and
	// run lifecycle tracking.
	Run struct {
		// AgentID identifies which agent processed the run.
		AgentID string
		// RunID is the durable workflow run identifier.
		RunID string
		// SessionID associates related runs (e.g., chat sessions).
		SessionID string
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
		Upsert(ctx context.Context, run Run) error
		Load(ctx context.Context, runID string) (Run, error)
	}

	// Status represents the lifecycle state of a run.
	Status string
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
)
