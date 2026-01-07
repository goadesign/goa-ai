package hooks

// This file defines the user-facing error messages emitted by the runtime.
//
// Callers may override these variables at process startup (before the runtime
// begins emitting events) to customize UX text without forking goa-ai.
//
// Contract:
// - These messages are intended to be rendered directly in UIs.
// - Do not mutate these values concurrently with active runs.
var (
	// PublicErrorTimeout is emitted when a run fails due to a timeout (provider or runtime).
	PublicErrorTimeout = "The request timed out. Please retry."

	// PublicErrorInternal is emitted when a run fails for an unclassified reason.
	PublicErrorInternal = "The request failed. Please retry."

	// PublicErrorProviderRateLimited is emitted when the model provider is throttling requests.
	PublicErrorProviderRateLimited = "The AI provider is rate-limiting requests. Please wait a moment and retry."

	// PublicErrorProviderUnavailable is emitted when the model provider is temporarily unavailable.
	PublicErrorProviderUnavailable = "The AI provider is temporarily unavailable. Please retry."

	// PublicErrorProviderInvalidRequest is emitted when the provider rejects the request as invalid.
	PublicErrorProviderInvalidRequest = "The AI provider rejected the request."

	// PublicErrorProviderAuth is emitted when provider authentication/authorization fails.
	PublicErrorProviderAuth = "The AI provider authentication failed."

	// PublicErrorProviderUnknown is emitted for unclassified provider failures.
	PublicErrorProviderUnknown = "The AI provider returned an unexpected error. Please retry."

	// PublicErrorProviderDefault is emitted when a provider failure does not match any known kind.
	PublicErrorProviderDefault = "The AI provider returned an error. Please retry."
)
