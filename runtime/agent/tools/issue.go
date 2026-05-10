// Package tools exposes shared tool metadata and codec types used by generated code.
package tools

import "fmt"

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

// ValidationError is the canonical structured validation error emitted by
// generated tool codecs.
//
// It keeps payload/result schema failures as data, not text parsing contracts:
// runtimes consume Issues and Descriptions to produce retry hints and UI copy.
type ValidationError struct {
	message      string
	issues       []*FieldIssue
	descriptions map[string]string
}

// NewValidationError creates a structured validation error from field issues.
func NewValidationError(message string, issues []*FieldIssue, descriptions map[string]string) *ValidationError {
	if message == "" {
		panic("tools.ValidationError requires a message")
	}
	clonedIssues := cloneFieldIssues(issues)
	if len(clonedIssues) == 0 {
		panic("tools.ValidationError requires at least one field issue")
	}
	return &ValidationError{
		message:      message,
		issues:       clonedIssues,
		descriptions: cloneStringMap(descriptions),
	}
}

// NewUnionDiscriminatorError creates the canonical validation error for a
// generated union's {type,value} discriminator.
//
// typePresent=false means the discriminator field was absent. An empty got with
// typePresent=true is an explicit empty string and is therefore invalid.
func NewUnionDiscriminatorError(union, got string, typePresent bool, allowed []string) *ValidationError {
	if union == "" {
		panic("tools.NewUnionDiscriminatorError requires a union name")
	}
	if len(allowed) == 0 {
		panic("tools.NewUnionDiscriminatorError requires allowed values")
	}
	for _, value := range allowed {
		if value == "" {
			panic("tools.NewUnionDiscriminatorError allowed values must be non-empty")
		}
	}
	constraint := "missing_field"
	if typePresent {
		constraint = "invalid_enum_value"
	}
	return NewValidationError(
		fmt.Sprintf("unexpected %s type %q (allowed: %q)", union, got, allowed),
		[]*FieldIssue{{
			Field:      "type",
			Constraint: constraint,
			Allowed:    allowed,
		}},
		nil,
	)
}

// Error returns the human-readable validation summary.
func (e *ValidationError) Error() string {
	return e.message
}

// Issues returns the structured field-level validation failures.
func (e *ValidationError) Issues() []*FieldIssue {
	return cloneFieldIssues(e.issues)
}

// Descriptions returns optional human-readable descriptions for invalid fields.
func (e *ValidationError) Descriptions() map[string]string {
	return cloneStringMap(e.descriptions)
}

// cloneFieldIssues copies validation issues so callers cannot mutate error state.
func cloneFieldIssues(in []*FieldIssue) []*FieldIssue {
	out := make([]*FieldIssue, 0, len(in))
	for _, issue := range in {
		if issue == nil {
			panic("tools.ValidationError field issues must be non-nil")
		}
		if issue.Field == "" {
			panic("tools.ValidationError field issue requires a field")
		}
		if issue.Constraint == "" {
			panic("tools.ValidationError field issue requires a constraint")
		}
		clone := *issue
		clone.Allowed = append([]string(nil), issue.Allowed...)
		clone.MinLen = cloneInt(issue.MinLen)
		clone.MaxLen = cloneInt(issue.MaxLen)
		out = append(out, &clone)
	}
	return out
}

// cloneStringMap copies field descriptions so errors own their metadata.
func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// cloneInt copies optional numeric constraint values on FieldIssue.
func cloneInt(in *int) *int {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
