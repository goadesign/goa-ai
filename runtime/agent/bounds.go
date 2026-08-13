package agent

// Bounds describes how a tool result has been bounded relative to the full
// underlying data set. It is a small, provider-agnostic contract used by
// runtimes, sinks, and services to surface truncation metadata without
// re-inspecting tool-specific fields.
//
// Returned reports how many items or points are present in the bounded view.
// Total, when non-nil, reports the provider-owned total before truncation.
// Truncated indicates whether any caps were applied (length, window, depth).
// NextCursor, when non-nil, is a non-empty opaque cursor that can be used to
// fetch the next page of results when Truncated is true.
// RefinementHint provides short, human-readable guidance on how to narrow or
// refine the query when Truncated is true.
type Bounds struct {
	Returned       int
	Total          *int
	Truncated      bool
	NextCursor     *string
	RefinementHint string
}

// CloneBounds deep-copies bounded-result metadata when it crosses runtime,
// persistence, registry, or hook ownership boundaries.
func CloneBounds(bounds *Bounds) *Bounds {
	if bounds == nil {
		return nil
	}
	cloned := *bounds
	if bounds.Total != nil {
		total := *bounds.Total
		cloned.Total = &total
	}
	if bounds.NextCursor != nil {
		cursor := *bounds.NextCursor
		cloned.NextCursor = &cursor
	}
	return &cloned
}
