package agent

// Bounds describes how a tool result has been bounded relative to the full
// underlying data set. It is a small, provider-agnostic contract used by
// runtimes, sinks, and services to surface truncation metadata without
// re-inspecting tool-specific fields.
//
// Returned reports how many items or points are present in the bounded view.
// Total, when non-nil, reports the best-effort total before truncation.
// Truncated indicates whether any caps were applied (length, window, depth).
// NextCursor, when non-nil, is the provider-owned cursor for fetching the next
// page. Runtimes project a model-visible continuation reference from this value
// instead of exposing the provider cursor directly.
// RefinementHint provides short, human-readable guidance on how to narrow or
// refine the query when Truncated is true.
type Bounds struct {
	Returned       int
	Total          *int
	Truncated      bool
	NextCursor     *string
	RefinementHint string
}
