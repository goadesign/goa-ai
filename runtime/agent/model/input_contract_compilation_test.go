// Package model checks that constructing tool inputs preserves schema validation
// and caller ownership when the main and alternate documents are identical.
package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestToolInputContractCompilationPreservesValidation(t *testing.T) {
	const schema = `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`
	tests := []struct {
		name      string
		schema    string
		alternate string
		example   string
		wantError string
	}{
		{
			name:      "identical documents",
			schema:    schema,
			alternate: schema,
			example:   `{"value":"ok"}`,
		},
		{name: "no alternate", schema: schema},
		{
			name:      "different root example",
			schema:    strings.TrimSuffix(schema, "}") + `,"example":{"value":"ok"}}`,
			alternate: schema,
			example:   `{"value":"ok"}`,
		},
		{name: "different formatting", schema: schema, alternate: " " + schema},
		{
			name:      "invalid example",
			schema:    schema,
			alternate: schema,
			example:   `{"value":1}`,
			wantError: "example JSON does not satisfy its input schema",
		},
		{
			name:      "malformed example",
			schema:    schema,
			alternate: schema,
			example:   `{`,
			wantError: `invalid example JSON for tool "sample"`,
		},
		{
			name:      "identical root examples rejected",
			schema:    `{"type":"object","example":{}}`,
			alternate: `{"type":"object","example":{}}`,
			wantError: "schema without root example: alternate schema changes fields other than root examples",
		},
		{
			name:      "identical invalid schemas rejected first",
			schema:    `{"type":"object","properties":false}`,
			alternate: `{"type":"object","properties":false}`,
			example:   `{`,
			wantError: "schema without root example: compile JSON Schema",
		},
		{
			name:      "identical external references rejected",
			schema:    `{"type":"object","$ref":"https://example.com/schema"}`,
			alternate: `{"type":"object","$ref":"https://example.com/schema"}`,
			wantError: "schema without root example: compile JSON Schema",
		},
		{
			name:      "different constraints rejected",
			schema:    schema,
			alternate: `{"type":"object"}`,
			wantError: "schema without root example: alternate schema changes fields other than root examples",
		},
		{
			name:      "empty main and absent alternate",
			wantError: "input schema: decode tool schema: EOF",
		},
		{
			name:      "whitespace documents",
			schema:    " ",
			alternate: "\n",
			wantError: "input schema: decode tool schema: EOF",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := ToolInputContract{Schema: rawjson.Message(test.schema)}
			if test.alternate != "" {
				contract.SchemaWithoutRootExample = rawjson.Message(test.alternate)
			}
			if test.example != "" {
				contract.ExampleJSON = rawjson.Message(test.example)
			}
			input, err := ToolInputFromContract("sample", contract)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.NoError(t, input.validate(rawjson.Message(`{"value":"ok"}`)))
			require.Error(t, input.validate(rawjson.Message(`{"value":1}`)))
			assert.Equal(t, contract, input.Contract())
		})
	}
}

func TestToolInputIdenticalSchemaOwnsDocuments(t *testing.T) {
	const schema = `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`
	shared := rawjson.Message(schema)
	contract := ToolInputContract{Schema: shared, SchemaWithoutRootExample: shared}
	input, err := ToolInputFromContract("sample", contract)
	require.NoError(t, err)
	want := ToolInputContract{
		Schema:                   rawjson.Message(schema),
		SchemaWithoutRootExample: rawjson.Message(schema),
	}
	shared[0] = '['
	assert.Equal(t, want, input.Contract())
	require.NoError(t, input.validate(rawjson.Message(`{"value":"ok"}`)))
	require.Error(t, input.validate(rawjson.Message(`{"value":1}`)))
	returned := input.Contract()
	returned.Schema[0] = '['
	returned.SchemaWithoutRootExample[0] = '['
	assert.Equal(t, want, input.Contract())
}

func BenchmarkToolInputSchemaCompilation(b *testing.B) {
	properties := make([]string, 128)
	for i := range 128 {
		properties[i] = fmt.Sprintf(`"field%d":{"type":"string","minLength":1}`, i)
	}
	schema := rawjson.Message(`{"type":"object","properties":{` + strings.Join(properties, ",") + `},"additionalProperties":false}`)
	for _, identical := range []bool{true, false} {
		b.Run(fmt.Sprintf("identical=%t", identical), func(b *testing.B) {
			contract := ToolInputContract{Schema: schema, SchemaWithoutRootExample: schema}
			if !identical {
				contract.Schema = rawjson.Message(strings.TrimSuffix(string(schema), "}") + `,"example":{}}`)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := ToolInputFromContract("sample", contract); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
