// Deferred definitions must keep the same validators and survive request
// ownership copies without allowing discovery metadata to mutate the caller.
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDeferredRequestPreservesFullValidationContract(t *testing.T) {
	definition := &ToolDefinition{
		Name:        "records.search",
		Search:      tools.NewSearchDocument("records.search Search records Find matching records"),
		Description: "Find matching records.",
		Deferred:    true,
		Input:       mustAdvertisedToolInput(rawjson.Message(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)),
	}
	request := &Request{Tools: []*ToolDefinition{definition}}
	owned, err := cloneRequest(request)
	require.NoError(t, err)
	assert.Equal(t, definition.Search, owned.Tools[0].Search)
	assert.True(t, owned.Tools[0].Deferred)
	assert.Equal(t, definition.Input.Contract(), owned.Tools[0].Input.Contract())
	owned.Tools[0].Search.Terms["records"] = 100
	assert.Equal(t, 3, definition.Search.Terms["records"])

	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := &Response{StopReason: "tool_calls", Content: []Message{{
		Role: ConversationRoleAssistant,
		Parts: []Part{ToolUsePart{
			ID: "call-1", Name: definition.Name, Input: rawjson.Message(`{"query":"alarm"}`),
		}},
	}}}
	_, err = contract.ValidateResponse(response)
	require.NoError(t, err)
	response.Content[0].Parts[0] = ToolUsePart{
		ID: "call-1", Name: definition.Name, Input: rawjson.Message(`{"query":1}`),
	}
	_, err = contract.ValidateResponse(response)
	require.Error(t, err)
}

func TestDeferredToolRequiresSearchDocument(t *testing.T) {
	_, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:     "records.search",
		Deferred: true,
		Input:    mustAdvertisedToolInput(rawjson.Message(`{"type":"object"}`)),
	}}})
	require.ErrorContains(t, err, `deferred tool "records.search" requires a search document`)
}
