package agent

// Bounds describes how a tool result has been bounded relative to the full
// underlying data set. It is a small, provider-agnostic contract used by
// runtimes, sinks, and services to surface truncation metadata without
// re-inspecting tool-specific fields.
//
// Returned reports how many items or points are present in the bounded view.
// Total, when non-nil, reports the best-effort total before truncation.
// Truncated indicates whether any caps were applied (length, window, depth).
// RefinementHint provides short, human-readable guidance on how to narrow or
// refine the query when Truncated is true.
type Bounds struct {
	Returned       int
	Total          *int
	Truncated      bool
	RefinementHint string
}

// BoundedResult is an optional interface implemented by tool result types that
// expose boundedness metadata directly. When a decoded tool result implements
// this interface, runtimes prefer it over heuristic field inspection so
// services can provide precise bounds semantics.
type BoundedResult interface {
	Bounds() Bounds
}
