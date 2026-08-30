// Package runlog provides a durable ordered event log for agent runs.
//
// The runlog is the canonical source of truth for run introspection. Runtimes
// append events as runs execute, callers list them using opaque cursors, and
// applications may permanently purge every event owned by an ended session.
package runlog

import (
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

var (
	// ErrEventConflict reports reuse of a stable event key with different
	// immutable identity or payload.
	ErrEventConflict = errors.New("runlog: event conflict")
)

// EventConflictError identifies the run and stable key whose existing record
// differs from the attempted append.
type EventConflictError struct {
	RunID    string
	EventKey string
}

// Error implements error.
func (e *EventConflictError) Error() string {
	return fmt.Sprintf("run %q event key %q: %v", e.RunID, e.EventKey, ErrEventConflict)
}

// Unwrap exposes ErrEventConflict for errors.Is.
func (e *EventConflictError) Unwrap() error {
	return ErrEventConflict
}

type (
	// Type identifies one canonical durable run-log record kind.
	//
	// Hook event types are one subset of durable record types. Other runtime-owned
	// records, such as transcript deltas, also use this namespace without being
	// hook events.
	Type string

	// ActivityInput is the canonical workflow-to-activity envelope for one durable
	// runtime record emitted from workflow code.
	ActivityInput struct {
		// Type identifies the record variant.
		Type Type

		// EventKey is the stable logical identity for this record within the run.
		EventKey string

		// RunID identifies the run that owns this record.
		RunID string

		// AgentID identifies the agent that owns this record.
		AgentID agent.Ident

		// SessionID identifies the logical session that owns this record.
		SessionID string

		// TurnID groups records for a single conversational turn. Empty when turn
		// tracking is disabled.
		TurnID string

		// TimestampMS records when the runtime emitted this record.
		TimestampMS int64

		// Payload holds record-specific fields encoded as JSON.
		Payload rawjson.Message
	}

	// Event is a single immutable run event appended to the run log.
	//
	// Store implementations assign the ID when persisting the event. IDs are
	// opaque, monotonically ordered within a run, and suitable for cursor-based
	// pagination.
	Event struct {
		// ID is the store-assigned opaque identifier for this event.
		ID string
		// EventKey is the stable logical identity for this event within the run.
		// Append deduplicates on (run_id, event_key) while leaving ID as the
		// ordered cursor for pagination.
		EventKey string
		// RunID is the identifier of the run this event belongs to.
		RunID string
		// AgentID is the identifier of the agent that emitted the event.
		AgentID agent.Ident
		// SessionID groups related runs into a conversation thread.
		SessionID string
		// TurnID identifies the conversational turn within the session.
		TurnID string
		// Type identifies the durable record kind.
		Type Type
		// Payload is the canonical JSON-encoded payload for the event.
		Payload rawjson.Message
		// Timestamp is the event time. It uses millisecond precision to match the
		// integer millisecond value carried by runtime records.
		Timestamp time.Time
	}

	// Page is a forward page of run events.
	Page struct {
		// Events are ordered oldest-first.
		Events []*Event
		// NextCursor is the cursor to use to fetch the next page.
		// It is empty when there are no further events.
		NextCursor string
	}
)
