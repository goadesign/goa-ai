package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// fakeValidationError mimics the generated ValidationError without importing the concrete type.
type fakeValidationError struct {
	issues []*tools.FieldIssue
	descs  map[string]string
}

func (f *fakeValidationError) Error() string                   { return "validation error" }
func (f *fakeValidationError) Issues() []*tools.FieldIssue     { return f.issues }
func (f *fakeValidationError) Descriptions() map[string]string { return f.descs }

func TestBuildRetryHint_MissingField(t *testing.T) {
	ferr := &fakeValidationError{
		issues: []*tools.FieldIssue{{Field: "q", Constraint: "missing_field"}},
		descs:  map[string]string{"q": "Search query"},
	}
	fields, q, reason, ok := buildRetryHintFromValidation(ferr, "svc.search")
	require.True(t, ok)
	require.Equal(t, planner.RetryReasonMissingFields, reason)
	require.Len(t, fields, 1)
	require.Equal(t, "q", fields[0])
	require.NotEmpty(t, q)
	require.True(t, containsAll(q, []string{"svc.search", "q"}))
}

func TestBuildRetryHint_InvalidEnum(t *testing.T) {
	ferr := &fakeValidationError{
		issues: []*tools.FieldIssue{{Field: "format", Constraint: "invalid_enum_value", Allowed: []string{"a", "b"}}},
		descs:  map[string]string{"format": "Output format"},
	}
	fields, q, reason, ok := buildRetryHintFromValidation(ferr, "svc.process")
	require.True(t, ok)
	require.Equal(t, planner.RetryReasonInvalidArguments, reason)
	require.Empty(t, fields)
	require.True(t, containsAll(q, []string{"format", "one of: a, b"}))
}

func TestBuildRetryHint_LengthPatternFormat(t *testing.T) {
	min := 2
	ferr := &fakeValidationError{
		issues: []*tools.FieldIssue{
			{Field: "name", Constraint: "invalid_length", MinLen: &min},
			{Field: "email", Constraint: "invalid_format", Format: "email"},
			{Field: "code", Constraint: "invalid_pattern", Pattern: "^[A-Z]+$"},
		},
	}
	fields, q, reason, ok := buildRetryHintFromValidation(ferr, "svc.create")
	require.True(t, ok)
	require.Equal(t, planner.RetryReasonInvalidArguments, reason)
	require.Empty(t, fields)
	require.NotEmpty(t, q)
	require.True(t, containsAll(q, []string{"name", "email", "code"}))
}

// containsAll helper
func containsAll(s string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
