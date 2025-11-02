// Package tools exposes shared tool metadata and codec types used by generated code.
package tools

// FieldIssue represents a single validation issue for a payload.
// Constraint values follow goa error kinds: missing_field, invalid_enum_value,
// invalid_format, invalid_pattern, invalid_range, invalid_length, invalid_field_type.
// Generated tool codecs return []FieldIssue from ValidationError.Issues().
type FieldIssue struct {
	Field      string
	Constraint string
	// Optional extras for richer UIs and retry hints; not all are populated by the codecs.
	Allowed []string
	MinLen  *int
	MaxLen  *int
	Pattern string
	Format  string
}
