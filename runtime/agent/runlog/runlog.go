// Package runlog provides a durable, append-only event log for agent runs.
//
// The runlog is the canonical source of truth for run introspection. Runtimes
// append events as runs execute and callers list them using opaque cursors.
package runlog

import (
	"context"
	"encoding/json"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
)

type (
	// Event is a single immutable run event appended to the run log.
	//
	// Store implementations assign the ID when persisting the event. IDs are
	// opaque, monotonically ordered within a run, and suitable for cursor-based
	// pagination.
	Event struct {
		// ID is the store-assigned opaque identifier for this event.
		ID string
		// RunID is the identifier of the run this event belongs to.
		RunID string
		// AgentID is the identifier of the agent that emitted the event.
		AgentID agent.Ident
		// SessionID groups related runs into a conversation thread.
		SessionID string
		// TurnID identifies the conversational turn within the session.
		TurnID string
		// Type is the hook event type.
		Type hooks.EventType
		// Payload is the canonical JSON-encoded payload for the event.
		Payload json.RawMessage
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
		// verbatim. Append must be durable: failures are surfaced to callers so
		// workflows can fail fast when canonical logging is unavailable.
		Append(ctx context.Context, e *Event) error

		// List returns the next forward page of events for the given run ID.
		//
		// Cursor is an opaque value returned by a previous call to List (or empty
		// to start from the beginning). Limit must be greater than zero.
		List(ctx context.Context, runID string, cursor string, limit int) (Page, error)
	}
)
