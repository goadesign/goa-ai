// Package tools exposes shared tool metadata and codec types used by generated code.
package tools

import (
	"fmt"
	"strings"
)

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
	// ExpectedJSONType and ActualJSONType are populated for invalid_field_type
	// issues emitted by generated codecs after JSON decoding rejects a field.
	ExpectedJSONType string
	ActualJSONType   string
}

// InvalidFieldTypeMessage renders generated JSON type metadata as planner guidance.
func InvalidFieldTypeMessage(issue *FieldIssue) string {
	validateInvalidFieldTypeIssue(issue)
	expected := jsonTypeArticle(issue.ExpectedJSONType)
	actual := jsonTypeArticle(issue.ActualJSONType)
	return fmt.Sprintf("`%s` must be %s, not %s", issue.Field, expected, actual)
}

// HasInvalidFieldTypeMetadata reports whether an issue carries enough generated
// codec metadata to render precise JSON type guidance.
func HasInvalidFieldTypeMetadata(issue *FieldIssue) bool {
	return issue != nil &&
		issue.Constraint == "invalid_field_type" &&
		issue.ExpectedJSONType != "" &&
		issue.ActualJSONType != ""
}

// InvalidFieldTypeQuestion renders invalid_field_type issues as retry guidance.
func InvalidFieldTypeQuestion(issues []*FieldIssue) string {
	if len(issues) == 0 {
		panic("tools.InvalidFieldTypeQuestion requires at least one issue")
	}
	parts := make([]string, 0, min(len(issues), 3))
	for i, issue := range issues {
		if i == 3 {
			break
		}
		parts = append(parts, InvalidFieldTypeMessage(issue))
	}
	return "Please resend the tool call with " + strings.Join(parts, ", ") + "."
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

func jsonTypeArticle(value string) string {
	if strings.HasPrefix(value, "JSON ") {
		return "a " + value
	}
	return "a JSON " + value
}

func validateInvalidFieldTypeIssue(issue *FieldIssue) {
	if issue == nil {
		panic("tools invalid_field_type issue must be non-nil")
	}
	if issue.Constraint != "invalid_field_type" {
		panic("tools invalid field type guidance requires invalid_field_type constraint")
	}
	if issue.ExpectedJSONType == "" {
		panic("tools invalid_field_type issue requires expected JSON type")
	}
	if issue.ActualJSONType == "" {
		panic("tools invalid_field_type issue requires actual JSON type")
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
