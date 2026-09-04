package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeleteModelHiddenFieldsRemovesNestedMetadata(t *testing.T) {
	fields := []*fieldMetadataData{
		{JSONType: "object"},
		{Path: []fieldPathSegmentData{{Name: "actors"}}, JSONType: "array"},
		{Path: []fieldPathSegmentData{{Name: "actors"}, {Name: "id"}}, JSONType: "string"},
		{Path: []fieldPathSegmentData{{Name: "actors"}, {Name: "type"}}, JSONType: "string"},
		{Path: []fieldPathSegmentData{{Name: "query"}}, JSONType: "string"},
	}
	owner := &contractTypeOwner{ModelHiddenPayloadFields: []string{"actors"}}

	fields = deleteModelHiddenFields(fields, owner, usagePayload)

	assert.Equal(t, []*fieldMetadataData{
		{JSONType: "object"},
		{Path: []fieldPathSegmentData{{Name: "query"}}, JSONType: "string"},
	}, fields)
}
