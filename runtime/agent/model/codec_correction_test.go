package model

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"testing"
)

func TestCodecCorrectionUsesOnlyAdvertisedMetadata(t *testing.T) {
	fields := []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("scope")}, JSONType: "string", Description: `One scope; "*" is not supported.`}, {Path: []tools.FieldPathSegment{tools.FixedField("records"), tools.DynamicField{}, tools.FixedField("value")}, JSONType: "string", Description: "Record value."}}
	err := tools.NewValidationError("secret-error", []*tools.FieldIssue{
		{Field: "scope", Constraint: "invalid_enum_value", Allowed: []string{"secret-allowed"}},
		{Field: "records.secret-key.value", Constraint: "invalid_pattern", Pattern: "secret-pattern", Format: "secret-format"},
		{Field: "secret-undeclared", Constraint: "missing_field"},
	}, map[string]string{"scope": "secret-description"})
	correction := toolFieldCorrection(fmt.Errorf("secret-wrapper: %w", err), rawjson.Message(`{"scope":"secret-input"}`), fields)
	assert.Contains(t, correction, `Field "scope" must use an allowed value.`)
	assert.Contains(t, correction, `One scope; \"*\" is not supported.`)
	assert.Contains(t, correction, `Field "records.*.value" must match its advertised input contract.`)
	assert.NotContains(t, correction, "secret-")
	assert.Contains(t, correction, "Other schema errors are not detailed here.")
	fields[0].Branches = []tools.UnionBranch{tools.TaggedUnionBranch{Discriminator: []tools.FieldPathSegment{tools.FixedField("kind")}, Value: "one"}}
	assert.NotContains(t, toolFieldCorrection(err, nil, fields), `Field "scope"`)
	assert.Equal(t, advertisedToolInputCorrection, toolFieldCorrection(fmt.Errorf("private"), nil, fields))
}

func TestCodecCorrectionRetainsJoinedFailures(t *testing.T) {
	fields := []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("first")}, JSONType: "string"}, {Path: []tools.FieldPathSegment{tools.FixedField("second")}, JSONType: "string"}}
	first := tools.NewValidationError("private-first", []*tools.FieldIssue{{Field: "first", Constraint: "missing_field"}}, nil)
	second := tools.NewValidationError("private-second", []*tools.FieldIssue{{Field: "second", Constraint: "invalid_enum_value"}}, nil)
	correction := toolFieldCorrection(fmt.Errorf("private-wrapper: %w", errors.Join(first, second)), nil, fields)
	assert.Contains(t, correction, `Field "first" is required.`)
	assert.Contains(t, correction, `Field "second" must use an allowed value.`)
	assert.NotContains(t, correction, "private-")
}
