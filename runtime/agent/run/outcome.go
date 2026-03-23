// Package run defines the canonical terminal outcome payloads for runs.
//
// These structs sit in the run domain rather than in hook/stream adapters so
// every layer can share one contract for failures and cancellations.
package run

type (
	// Failure captures the canonical terminal failure payload for a run.
	//
	// Contract:
	// - Present only when the run ends in a failed terminal state.
	// - Message is stable and safe to render directly in UX surfaces.
	// - DebugMessage is for operators, logs, and traces only.
	Failure struct {
		// Message is the user-safe terminal failure summary.
		Message string `json:"message,omitempty"`
		// DebugMessage is the raw diagnostic error text for internal use.
		DebugMessage string `json:"debug_message,omitempty"`
		// Provider identifies the failing upstream provider when the failure came
		// from a provider-specific error.
		Provider string `json:"provider,omitempty"`
		// Operation identifies the provider operation associated with the failure.
		Operation string `json:"operation,omitempty"`
		// Kind is the stable error category used by retry and UX logic.
		Kind string `json:"kind,omitempty"`
		// Code is the provider-specific error code when available.
		Code string `json:"code,omitempty"`
		// HTTPStatus is the upstream HTTP status code when available.
		HTTPStatus int `json:"http_status,omitempty"`
		// Retryable reports whether retrying may succeed without changing input.
		Retryable bool `json:"retryable"`
	}

	// Cancellation captures the canonical terminal cancellation payload for a run.
	//
	// Contract:
	// - Present only when the run ends in a canceled terminal state.
	// - Reason is always populated and describes who or what canceled the run.
	Cancellation struct {
		// Reason identifies the cancellation provenance.
		Reason string `json:"reason"`
	}
)

const (
	// CancellationReasonUserRequested indicates an explicit user stop request.
	CancellationReasonUserRequested = "user_requested"

	// CancellationReasonSessionEnded indicates the owning session ended and the
	// runtime canceled the run as part of session teardown.
	CancellationReasonSessionEnded = "session_ended"

	// CancellationReasonEngineCanceled indicates the workflow engine reported a
	// cancellation that did not originate from a runtime-owned cancel request.
	CancellationReasonEngineCanceled = "engine_canceled"
)
