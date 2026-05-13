package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidationErrorClonesIssues(t *testing.T) {
	const mutated = "mutated"

	minLen := 2
	issues := []*FieldIssue{{
		Field:      "type",
		Constraint: "invalid_enum_value",
		Allowed:    []string{"schedule"},
		MinLen:     &minLen,
	}}
	err := NewValidationError("bad type", issues, map[string]string{"type": "Rule type"})

	issues[0].Field = mutated
	issues[0].Allowed[0] = mutated
	minLen = 9
	err.Descriptions()["type"] = mutated

	got := err.Issues()
	assert.Equal(t, "bad type", err.Error())
	assert.Equal(t, "type", got[0].Field)
	assert.Equal(t, []string{"schedule"}, got[0].Allowed)
	assert.Equal(t, 2, *got[0].MinLen)
	assert.Equal(t, map[string]string{"type": "Rule type"}, err.Descriptions())
}

func TestNewValidationErrorPanicsOnInvalidConstruction(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{
			name: "empty message",
			fn: func(t *testing.T) {
				err := NewValidationError(
					"",
					[]*FieldIssue{{Field: "type", Constraint: "missing_field"}},
					nil,
				)
				assert.Nil(t, err)
			},
		},
		{
			name: "empty issues",
			fn: func(t *testing.T) {
				assert.Nil(t, NewValidationError("bad type", nil, nil))
			},
		},
		{
			name: "nil issue",
			fn: func(t *testing.T) {
				assert.Nil(t, NewValidationError("bad type", []*FieldIssue{nil}, nil))
			},
		},
		{
			name: "empty field",
			fn: func(t *testing.T) {
				err := NewValidationError(
					"bad type",
					[]*FieldIssue{{Constraint: "missing_field"}},
					nil,
				)
				assert.Nil(t, err)
			},
		},
		{
			name: "empty constraint",
			fn: func(t *testing.T) {
				err := NewValidationError("bad type", []*FieldIssue{{Field: "type"}}, nil)
				assert.Nil(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() {
				tt.fn(t)
			})
		})
	}
}

func TestNewUnionDiscriminatorErrorDistinguishesMissingAndEmpty(t *testing.T) {
	missing := NewUnionDiscriminatorError("Rule", "", false, []string{"schedule"})
	explicitEmpty := NewUnionDiscriminatorError("Rule", "", true, []string{"schedule"})

	assert.Equal(t, "missing_field", missing.Issues()[0].Constraint)
	assert.Equal(t, "invalid_enum_value", explicitEmpty.Issues()[0].Constraint)
	assert.Equal(t, []string{"schedule"}, explicitEmpty.Issues()[0].Allowed)
}

func TestNewUnionDiscriminatorErrorPanicsOnInvalidConstruction(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{
			name: "empty union",
			fn: func(t *testing.T) {
				err := NewUnionDiscriminatorError("", "schedule", true, []string{"schedule"})
				assert.Nil(t, err)
			},
		},
		{
			name: "empty allowed values",
			fn: func(t *testing.T) {
				err := NewUnionDiscriminatorError("Rule", "schedule", true, nil)
				assert.Nil(t, err)
			},
		},
		{
			name: "empty allowed value",
			fn: func(t *testing.T) {
				err := NewUnionDiscriminatorError("Rule", "schedule", true, []string{""})
				assert.Nil(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() {
				tt.fn(t)
			})
		})
	}
}
