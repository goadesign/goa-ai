// These tests verify that codec failures expose only declared field guidance
// and retain notice of failures that cannot safely be explained.
package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCodecCorrectionUsesOnlyAdvertisedMetadata(t *testing.T) {
	fields := []tools.FieldMetadata{
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("scope"),
			},

			JSONType: "string",

			Description: `One scope; "*" is not supported.`,
		},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("records"),

				tools.DynamicField{},

				tools.FixedField("value"),
			},

			JSONType: "string",

			Description: "Record value.",
		},
	}
	err := tools.NewValidationError("secret-error", []*tools.FieldIssue{
		{
			Field:      "scope",
			Constraint: "invalid_enum_value",
			Allowed: []string{
				"secret-allowed",
			},
		},
		{
			Field:      "records.secret-key.value",
			Constraint: "invalid_pattern",
			Pattern:    "secret-pattern",
			Format:     "secret-format",
		},
		{
			Field:      "secret-undeclared",
			Constraint: "missing_field",
		},
	}, map[string]string{
		"scope": "secret-description",
	})
	correction := toolFieldCorrection(fmt.Errorf("secret-wrapper: %w", err), rawjson.Message(`{"scope":"secret-input"}`), fields)
	assert.Contains(t, correction, `Field "scope" must use an allowed value.`)
	assert.Contains(t, correction, `One scope; \"*\" is not supported.`)
	assert.Contains(t, correction, `Field "records.*.value" must match its advertised input contract.`)
	assert.NotContains(t, correction, "secret-")
	assert.Contains(t, correction, "Other schema errors are not detailed here.")
	fields[0].Branches = []tools.UnionBranch{
		tools.TaggedUnionBranch{
			Discriminator: []tools.FieldPathSegment{
				tools.FixedField("kind"),
			},
			Value: "one",
		},
	}
	assert.NotContains(t, toolFieldCorrection(err, nil, fields), `Field "scope"`)
	assert.Equal(t, advertisedToolInputCorrection, toolFieldCorrection(fmt.Errorf("private"), nil, fields))
}

func TestCodecCorrectionRetainsJoinedFailures(t *testing.T) {
	fields := []tools.FieldMetadata{
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("first"),
			},

			JSONType: "string",
		},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("second"),
			},

			JSONType: "string",
		},
	}
	first := tools.NewValidationError("private-first", []*tools.FieldIssue{
		{
			Field:      "first",
			Constraint: "missing_field",
		},
	}, nil)
	second := tools.NewValidationError("private-second", []*tools.FieldIssue{
		{
			Field:      "second",
			Constraint: "invalid_enum_value",
		},
	}, nil)
	correction := toolFieldCorrection(fmt.Errorf("private-wrapper: %w", errors.Join(first, second)), nil, fields)
	assert.Contains(t, correction, `Field "first" is required.`)
	assert.Contains(t, correction, `Field "second" must use an allowed value.`)
	assert.NotContains(t, correction, "private-")
}

type emptyCodecErrorTree struct{}

func (emptyCodecErrorTree) Error() string   { return "private-empty-tree" }
func (emptyCodecErrorTree) Unwrap() []error { return nil }

func TestCodecCorrectionRetainsUnexplainedFailures(t *testing.T) {
	fields := []tools.FieldMetadata{
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("scope"),
			},
			JSONType: "string",
		},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("records"),
				tools.DynamicField{},
			},
			JSONType: "string",
		},
	}
	known := tools.NewValidationError("private-known", []*tools.FieldIssue{
		{
			Field:      "scope",
			Constraint: "missing_field",
		},
	}, nil)
	var typedNil *tools.ValidationError
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "unknown dynamic property",
			err: tools.NewValidationError("private-unknown", []*tools.FieldIssue{
				{
					Field:      "records.private-key",
					Constraint: "unknown_field",
				},
			}, nil),
		},
		{
			name: "typed nil",
			err:  typedNil,
		},
		{
			name: "empty issues",
			err:  &tools.ValidationError{},
		},
		{
			name: "empty tree",
			err:  emptyCodecErrorTree{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, advertisedToolInputCorrection, toolFieldCorrection(tc.err, nil, fields))
			correction := toolFieldCorrection(errors.Join(known, tc.err), nil, fields)
			assert.Contains(t, correction, `Field "scope" is required.`)
			assert.Contains(t, correction, "Other schema errors are not detailed here.")
			assert.NotContains(t, correction, "records")
			assert.NotContains(t, correction, "private-")
		})
	}
}
