// Package runlog provides a durable, append-only event log for agent runs.
//
// The runlog is the canonical source of truth for run introspection. Runtimes
// append events as runs execute and callers list them using opaque cursors.
package runlog

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

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

	// AppendResult describes the outcome of storing a canonical run event.
	//
	// IDs remain store-assigned cursor values. Inserted reports whether this call
	// inserted a new event or replayed an existing event with the same logical
	// event key.
	AppendResult struct {
		// ID is the store-assigned opaque identifier for the canonical event.
		ID string
		// Inserted reports whether the event was newly inserted.
		Inserted bool
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
		// Timestamp is the event time.
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

	// Store is an append-only event store for run introspection.
	//
	// Implementations must provide stable ordering within a run. Cursor values are
	// store-owned and opaque to callers.
	Store interface {
		// Append stores the event in the run log.
		//
		// Store implementations assign the event ID and persist the payload
		// verbatim. Append must be durable and idempotent on (run_id, event_key):
		// exact duplicates return the existing event ID with Inserted=false, while
		// conflicting bodies for the same key must fail loudly.
		Append(ctx context.Context, e *Event) (AppendResult, error)

		// List returns the next forward page of events for the given run ID.
		//
		// Cursor is an opaque value returned by a previous call to List (or empty
		// to start from the beginning). Limit must be greater than zero.
		List(ctx context.Context, runID string, cursor string, limit int) (Page, error)
	}
)
