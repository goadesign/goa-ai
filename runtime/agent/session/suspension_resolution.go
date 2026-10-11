// Package session keeps a workflow's historical outcome separate from the
// permanent decision to continue or relinquish its saved suspension. Stores
// choose the resolution atomically with continuation admission.
package session

import "time"

type (
	// SuspensionResolution is the permanent disposition of one saved suspension.
	// A nil value means unresolved. Stores return only the two value variants
	// below, never pointers. Neither variant changes the suspended run's result.
	SuspensionResolution interface {
		suspensionResolution()
	}

	// ContinuedSuspension identifies the real workflow admitted to inherit the
	// suspended run's saved work. The successor has the same agent and session.
	ContinuedSuspension struct {
		// RunID identifies the admitted successor, never an unrelated later run.
		RunID string
	}

	// RelinquishedSuspension records permanent closure of a root suspension
	// after the runtime proved it owned no unfinished Task or child.
	RelinquishedSuspension struct {
		// Reason is the accepted cancellation reason and never changes on retry.
		Reason string
		// ResolvedAt is the storage owner's time of the atomic closure.
		ResolvedAt time.Time
	}
)

func (ContinuedSuspension) suspensionResolution() {}

func (RelinquishedSuspension) suspensionResolution() {}
