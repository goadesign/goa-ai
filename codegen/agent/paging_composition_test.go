// These tests reject incompatible promises before a paging pair becomes one
// public operation and preserve the dialect and references of its input schemas.
package codegen

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/toolregistry/schema"
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

func TestPagingExecutionSchemaPreservesBranchContracts(t *testing.T) {
	query := []byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","additionalProperties":false,"required":["filter"],
		"properties":{
			"filter":{"$ref":"#/$defs/Filter","maxProperties":2,"description":"Selected records."},
			"copy":{"$ref":"#/properties/filter"},
			"$ref":{"type":"string"},
			"list":{"type":"array","items":{"$ref":"#/$defs/Filter"}},
			"map":{"type":"object","additionalProperties":{"$ref":"#/$defs/Filter"}},
			"choice":{"oneOf":[{"$ref":"#/$defs/Filter"},{"type":"integer"}]}
		},
		"example":{"$ref":"#/$defs/Filter"},
		"default":{"$ref":"#/$defs/Filter"},
		"$defs":{
			"Filter":{"type":"object","additionalProperties":false,"properties":{
				"value":{"type":"string"},"child":{"$ref":"#/$defs/Filter"},"peer":{"$ref":"#/$defs/Other"}
			}},
			"Other":{"type":"object","additionalProperties":false,"properties":{"back":{"$ref":"#/$defs/Filter"}}}
		}
	}`)
	page := []byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","additionalProperties":false,"required":["cursor","filter"],
		"properties":{"cursor":{"type":"string"},"filter":{"$ref":"#/$defs/Filter","minimum":2}},
		"$defs":{"Filter":{"type":"integer"}}
	}`)
	combined, err := pagingExecutionSchema(query, page)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(combined, &document))
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", document["$schema"])
	assert.Equal(t, "object", document["type"])
	branches := document["oneOf"].([]any)
	require.Len(t, branches, 2)
	definitions := document["$defs"].(map[string]any)
	require.Len(t, definitions, 3)
	for index, test := range []struct {
		original []byte
		prefix   string
		// These paths name every schema reference in the original fixture.
		// Restoring them must recover the entire document, including data values.
		references []string
	}{
		{query, "query__", []string{
			"properties/filter", "properties/copy", "properties/list/items",
			"properties/map/additionalProperties", "properties/choice/oneOf/0",
			"$defs/Filter/properties/child", "$defs/Filter/properties/peer", "$defs/Other/properties/back",
		}},
		{page, "page__", []string{"properties/filter"}},
	} {
		var original map[string]any
		require.NoError(t, json.Unmarshal(test.original, &original))
		restored := branches[index].(map[string]any)
		assert.NotContains(t, restored, "$schema")
		assert.NotContains(t, restored, "$defs")
		restored["$schema"] = document["$schema"]
		restoredDefinitions := make(map[string]any)
		for name, definition := range definitions {
			if strings.HasPrefix(name, test.prefix) {
				restoredDefinitions[strings.TrimPrefix(name, test.prefix)] = definition
			}
		}
		restored["$defs"] = restoredDefinitions
		for _, reference := range test.references {
			before := pagingTestNode(t, original, reference)["$ref"].(string)
			node := pagingTestNode(t, restored, reference)
			if strings.HasPrefix(before, "#/$defs/") {
				assert.Equal(t, "#/$defs/"+test.prefix+strings.TrimPrefix(before, "#/$defs/"), node["$ref"])
			} else {
				assert.Equal(t, "#/oneOf/"+strconv.Itoa(index)+before[1:], node["$ref"])
			}
			node["$ref"] = before
		}
		assert.Equal(t, original, restored)
	}
	for _, test := range []struct {
		input string
		valid bool
	}{
		{`{"filter":{"value":"record","child":{"peer":{"back":{"value":"nested"}}}}}`, true},
		{`{"filter":{},"copy":{},"list":[{}],"map":{"item":{}},"choice":7,"$ref":"literal"}`, true},
		{`{"cursor":"next","filter":3}`, true},
		{`{"filter":"record"}`, false},
		{`{"filter":{"value":"record","child":{},"peer":{}}}`, false},
		{`{"filter":{"child":{"value":7}}}`, false},
		{`{"cursor":"next","filter":1}`, false},
		{`{"cursor":"next","filter":{}}`, false},
		{`{"filter":{},"unknown":true}`, false},
		{`{}`, false},
	} {
		err := schema.Validate(combined, json.RawMessage(test.input), "input")
		if test.valid {
			require.NoError(t, err, test.input)
		} else {
			require.Error(t, err, test.input)
		}
	}
	composed, err := schema.PagingComposition(combined, "cursor")
	require.NoError(t, err)
	assert.True(t, composed)
}

func TestPagingExecutionSchemaWithoutDefinitions(t *testing.T) {
	query := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"]}`)
	page := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"]}`)
	combined, err := pagingExecutionSchema(query, page)
	require.NoError(t, err)
	assert.True(t, bytes.Equal([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[{"additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"},{"additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"],"type":"object"}],"type":"object"}`), combined), "schemas without definitions must retain their exact bytes")
}

func TestPagingCompositionLeavesOrdinaryToolUnchanged(t *testing.T) {
	document := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"filter":{"$ref":"#/$defs/Filter"}},"$defs":{"Filter":{"type":"string"}}}`)
	entry := &toolEntry{
		Name:      "records.read",
		Payload:   &typeData{SchemaJSON: append([]byte(nil), document...), ExecutionSchemaJSON: append([]byte(nil), document...)},
		Execution: &executionContractData{Schema: append([]byte(nil), document...)},
	}
	require.NoError(t, composePagingEntries([]*ToolData{{QualifiedName: entry.Name}}, []*toolEntry{entry}))
	assert.Equal(t, document, entry.Payload.SchemaJSON)
	assert.Equal(t, document, entry.Payload.ExecutionSchemaJSON)
	assert.Equal(t, document, entry.Execution.Schema)
}

// pagingTestNode follows a fixture's explicit reference location so the test can
// reverse the relocation without reusing the composer's schema traversal.
func pagingTestNode(t *testing.T, document map[string]any, location string) map[string]any {
	t.Helper()
	var node any = document
	for _, part := range strings.Split(location, "/") {
		switch value := node.(type) {
		case map[string]any:
			node = value[part]
		case []any:
			index, err := strconv.Atoi(part)
			require.NoError(t, err)
			node = value[index]
		}
	}
	return node.(map[string]any)
}
