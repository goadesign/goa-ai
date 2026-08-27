// This file proves that an unadvertised tool name remains a typed fact while
// output rejection crosses local wrappers and trusted transport restoration.

package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestNewUnadvertisedToolNameErrorRequiresName(t *testing.T) {
	assert.PanicsWithValue(t, "model: unadvertised tool name is required", func() {
		require.NoError(t, NewUnadvertisedToolNameError(""))
	})
}

func TestUnadvertisedToolNameFollowsWrappedAndRestoredErrors(t *testing.T) {
	marker := NewUnadvertisedToolNameError("catalog_list_nearby")
	wrapped := fmt.Errorf("adapter rejected response: %w", marker)
	rejected, err := RestoreOutputValidationError(
		wrapped,
		ResponseEvidence{Present: false},
		&TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	)
	require.NoError(t, err)

	name, ok := UnadvertisedToolName(rejected)
	assert.True(t, ok)
	assert.Equal(t, "catalog_list_nearby", name)

	_, ok = UnadvertisedToolName(errors.New(`tool name "catalog_list_nearby" was not advertised`))
	assert.False(t, ok, "diagnostic text alone must not become typed recovery state")
}

func TestRequestContractRejectsWholeResponseAtFirstUnadvertisedToolName(t *testing.T) {
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:        "catalog_list_items",
		Description: "List catalog items.",
		Input:       mustAdvertisedToolInput(rawjson.Message(`{"type":"object"}`)),
	}}})
	require.NoError(t, err)

	response, err := contract.ValidateResponse(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{
				TextPart{Text: "partial text"},
				ToolUsePart{
					ID:    "valid-call",
					Name:  "catalog_list_items",
					Input: rawjson.Message(`{}`),
				},
				ToolUsePart{
					ID:    "rejected-call",
					Name:  "catalog-list-items",
					Input: rawjson.Message(`{"ignored":true}`),
				},
			},
		}},
		StopReason: "tool_use",
	})

	assert.Nil(t, response)
	name, ok := UnadvertisedToolName(err)
	assert.True(t, ok)
	assert.Equal(t, "catalog-list-items", name)
}

func TestRequestContractAcceptsExactAdvertisedToolName(t *testing.T) {
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:        "catalog_list_items",
		Description: "List catalog items.",
		Input:       mustAdvertisedToolInput(rawjson.Message(`{"type":"object"}`)),
	}}})
	require.NoError(t, err)

	response, err := contract.ValidateResponse(&Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:    "valid-call",
				Name:  "catalog_list_items",
				Input: rawjson.Message(`{}`),
			}},
		}},
		StopReason: "tool_use",
	})

	require.NoError(t, err)
	assert.NotNil(t, response)
}
