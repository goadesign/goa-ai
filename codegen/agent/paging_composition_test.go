// These tests reject incompatible promises before a paging pair becomes one
// public operation and preserve references inside its two input schemas.
package codegen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPagingCompositionRejectsDifferentPublicPromises(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*toolEntry)
	}{
		{"timeout", func(page *toolEntry) {
			page.ReplanOnTimeout = true
		}},
		{"UI", func(page *toolEntry) {
			page.RequiresUI = true
		}},
		{"terminal", func(page *toolEntry) {
			page.TerminalRun = true
		}},
		{"bookkeeping", func(page *toolEntry) {
			page.Bookkeeping = true
		}},
		{"metadata", func(page *toolEntry) { page.Meta = map[string][]string{"readOnly": {"true"}} }},
		{"result", func(page *toolEntry) { page.Result = &typeData{SchemaJSON: []byte(`{"type":"string"}`)} }},
		{"reminder", func(page *toolEntry) {
			page.ResultReminder = "A different result promise."
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, page := &toolEntry{Name: "records.search"}, &toolEntry{Name: "records.next"}
			test.change(page)
			assert.Error(t, validatePagingComposition(source, page))
		})
	}
	source := &toolEntry{Name: "records.search", Tags: []string{"initial"}, ReplanOnTimeout: true}
	page := &toolEntry{Name: "records.next", Tags: []string{"page"}, ReplanOnTimeout: true}
	assert.NoError(t, validatePagingComposition(source, page))
}

func TestPagingExecutionSchemaKeepsBranchReferences(t *testing.T) {
	query := []byte(`{"type":"object","additionalProperties":false,"properties":{"filter":{"$ref":"#/$defs/Filter"}},"$defs":{"Filter":{"type":"string"}}}`)
	page := []byte(`{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"},"filter":{"$ref":"#/$defs/Filter"}},"required":["cursor"],"$defs":{"Filter":{"type":"integer"}}}`)
	combined, err := pagingExecutionSchema(query, page)
	require.NoError(t, err)
	var document map[string][]map[string]any
	require.NoError(t, json.Unmarshal(combined, &document))
	require.Len(t, document["oneOf"], 2)
	assert.Contains(t, string(combined), `#/oneOf/0/$defs/Filter`)
	assert.Contains(t, string(combined), `#/oneOf/1/$defs/Filter`)
	assert.NotContains(t, string(query), "oneOf")
	assert.NotContains(t, string(page), "oneOf")
}
