// Package boundedresult defines the canonical bounded-result contract shared by
// DSL validation, code generation, and runtime projection.
//
// Contract:
//   - Canonical bounded fields are owned by Goa-AI, not by authored semantic tool
//     result shapes.
//   - returned/truncated are always required for bounded successful results.
//   - total/refinement_hint/next_cursor are optional and emitted only when set.
package boundedresult

const (
	// FieldReturned is the canonical count of items present in this response.
	FieldReturned = "returned"
	// FieldTotal is the optional total number of matching items before truncation.
	FieldTotal = "total"
	// FieldTruncated indicates whether runtime caps/truncation were applied.
	FieldTruncated = "truncated"
	// FieldRefinementHint is optional guidance for narrowing a truncated query.
	FieldRefinementHint = "refinement_hint"
)

// CanonicalFieldNames returns canonical bounded-result field names and includes
// nextCursorField when non-empty.
func CanonicalFieldNames(nextCursorField string) []string {
	names := []string{
		FieldReturned,
		FieldTotal,
		FieldTruncated,
		FieldRefinementHint,
	}
	if nextCursorField != "" {
		names = append(names, nextCursorField)
	}
	return names
}

// RequiredFieldNames returns canonical bounded-result fields that are always
// required for successful bounded results.
func RequiredFieldNames() []string {
	return []string{
		FieldReturned,
		FieldTruncated,
	}
}

// OptionalFieldNames returns canonical bounded-result fields that must remain
// optional in JSON schemas and result payloads.
func OptionalFieldNames(nextCursorField string) []string {
	names := []string{
		FieldTotal,
		FieldRefinementHint,
	}
	if nextCursorField != "" {
		names = append(names, nextCursorField)
	}
	return names
}

// HasContinuation reports whether a truncated result carries either a paging
// cursor or refinement guidance.
func HasContinuation(nextCursor *string, refinementHint string) bool {
	return nextCursor != nil || refinementHint != ""
}
