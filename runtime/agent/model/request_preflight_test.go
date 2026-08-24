package model

// This file verifies that request-wide limits run before cloning allocates
// framework-owned slices or byte buffers.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneRequestUsesOneAggregateByteBudget(t *testing.T) {
	half := strings.Repeat("x", maxDynamicValueBytes/2+1)
	request := &Request{
		Messages: []*Message{{
			Role:  ConversationRoleUser,
			Parts: []Part{TextPart{Text: half}},
		}},
		StructuredOutput: &StructuredOutput{
			Name:   "result",
			Schema: []byte(half),
		},
	}

	_, err := cloneRequest(request)

	require.ErrorContains(t, err, "maximum byte size")
}

func TestCloneRequestRejectsCollectionBeforeAllocatingCopy(t *testing.T) {
	request := &Request{
		Tools: make([]*ToolDefinition, maxDynamicValueVisits+1),
	}

	_, err := cloneRequest(request)

	require.ErrorContains(t, err, "maximum visited values")
}

func TestCloneRequestRejectsOversizedToolSchemaBeforeCopy(t *testing.T) {
	request := &Request{
		Tools: []*ToolDefinition{{
			Name: "lookup",
			Input: ToolInput{
				jsonSchema: []byte(strings.Repeat(" ", maxToolSchemaBytes+1)),
			},
		}},
	}

	_, err := cloneRequest(request)

	require.ErrorContains(t, err, "schema uses")
	require.ErrorContains(t, err, "maximum is")
}
