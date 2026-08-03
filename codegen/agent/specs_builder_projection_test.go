package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeleteModelHiddenFieldsRemovesNestedMetadata(t *testing.T) {
	fields := map[string]string{
		"$payload":    "object",
		"actors":      "array",
		"actors.id":   "string",
		"actors.type": "string",
		"query":       "string",
	}
	owner := &contractTypeOwner{ModelHiddenPayloadFields: []string{"actors"}}

	deleteModelHiddenFields(fields, owner, usagePayload)

	assert.Equal(t, map[string]string{
		"$payload": "object",
		"query":    "string",
	}, fields)
}
