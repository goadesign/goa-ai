// Portable result codecs validate provider data before the runtime adds counts,
// truncation, and paging information from Bounds. These tests keep public schema
// validation and unbounded business fields strict, including matching names.
package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
	airuntime "goa.design/goa-ai/runtime/agent/runtime"
	registryclient "goa.design/goa-ai/runtime/registry"
	"goa.design/goa-ai/runtime/toolregistry/contract"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

func TestCompileBoundedResultPreservesPublicContract(t *testing.T) {
	continuation := "records.continue"
	for _, test := range []struct {
		name       string
		paging     *genregistry.ToolPaging
		exactTotal bool
	}{
		{name: "bounded"},
		{name: "exact total", exactTotal: true},
		{name: "same tool paging", paging: &genregistry.ToolPaging{CursorField: "cursor", NextCursorField: "next_page", RetainedFields: []string{}}},
		{name: "dedicated continuation", paging: &genregistry.ToolPaging{CursorField: "cursor", NextCursorField: "next_page", RetainedFields: []string{}, ContinueTool: &continuation}},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := boundedDeclaration()
			declaration.ConsumerContract.Bounds.Paging = test.paging
			if test.exactTotal {
				declaration.ResultSchema = []byte(strings.Replace(string(declaration.ResultSchema), `"required":["value","details","returned","truncated"]`, `"required":["value","details","returned","total","truncated"]`, 1))
			}
			if test.paging != nil && test.paging.ContinueTool == nil {
				declaration.ResultSchema = []byte(strings.Replace(string(declaration.ResultSchema), `"properties":{`, `"properties":{"next_page":{"type":"string"},`, 1))
			}
			declaration.ConsumerContract.Result.SchemaWithoutRootExample = declaration.ResultSchema
			original, err := json.Marshal(declaration)
			require.NoError(t, err)
			spec, err := contract.Compile(declaration)
			require.NoError(t, err)
			assert.Equal(t, declaration.ResultSchema, []byte(spec.Result.Schema))
			assert.Equal(t, declaration.ConsumerContract.Result.SchemaWithoutRootExample, []byte(spec.Result.SchemaWithoutRootExample))
			after, err := json.Marshal(declaration)
			require.NoError(t, err)
			assert.Equal(t, original, after)

			semantic := rawjson.Message(`{"value":9007199254740993,"details":{"returned":2}}`)
			value, err := spec.Result.Codec.FromJSON(semantic)
			require.NoError(t, err)
			encoded, err := spec.Result.Codec.ToJSON(value)
			require.NoError(t, err)
			assert.JSONEq(t, string(semantic), string(encoded))
			assert.Equal(t, json.Number("9007199254740993"), value.(map[string]any)["value"])
			total := 4
			bounds := &agent.Bounds{Returned: 1, Total: &total, Truncated: true, RefinementHint: "Narrow the query."}
			if test.paging != nil {
				cursor := "page-2"
				bounds.NextCursor = &cursor
			}
			require.NoError(t, airuntime.ValidateSuccessfulToolResult(spec, semantic, bounds))
			final, err := airuntime.EncodeCanonicalToolResult(spec, value, bounds)
			require.NoError(t, err)
			public, err := schema.Codec(declaration.ResultSchema)
			require.NoError(t, err)
			_, err = public.FromJSON(semantic)
			require.Error(t, err)
			decoded, err := public.FromJSON(final)
			require.NoError(t, err)
			fields := decoded.(map[string]any)
			assert.Equal(t, json.Number("1"), fields["returned"])
			assert.Equal(t, json.Number("4"), fields["total"])
			assert.Equal(t, true, fields["truncated"])
			if test.paging != nil && test.paging.ContinueTool == nil {
				assert.Equal(t, "page-2", fields["next_page"])
			} else {
				assert.NotContains(t, fields, "next_page")
			}
			restored, err := spec.Result.Codec.FromJSON(final)
			require.NoError(t, err)
			assert.Equal(t, decoded, restored)

			strict, err := registryclient.NewToolset(&registryclient.ToolsetSchema{Name: "records", Tools: []*registryclient.ToolSchema{{
				Name: declaration.Name, PayloadSchema: declaration.PayloadSchema,
				ExecutionPayloadSchema: declaration.ExecutionPayloadSchema, ResultSchema: declaration.ResultSchema,
			}}})
			require.NoError(t, err)
			require.Error(t, strict.ValidateResult(spec.Name, value))
			require.NoError(t, strict.ValidateResult(spec.Name, restored))
		})
	}
}

func TestCompileBoundedResultRejectsInvalidValues(t *testing.T) {
	spec, err := contract.Compile(boundedDeclaration())
	require.NoError(t, err)
	for _, input := range []string{
		`{"details":{"returned":2}}`,
		`{"value":1,"details":{}}`,
		`{"value":"wrong","details":{"returned":2}}`,
		`{"value":1,"details":{"returned":2},"unexpected":true}`,
		`{"value":1,"details":{"returned":2},"returned":"wrong"}`,
		`{"value":1,"details":{"returned":2},"truncated":1}`,
		`{"value":1,"details":{"returned":2},"total":false}`,
		`{"value":1,"details":{"returned":2},"refinement_hint":5}`,
	} {
		_, err := spec.Result.Codec.FromJSON([]byte(input))
		require.Error(t, err, input)
		var value any
		require.NoError(t, json.Unmarshal([]byte(input), &value))
		_, err = spec.Result.Codec.ToJSON(value)
		require.Error(t, err, input)
	}
	semantic := rawjson.Message(`{"value":1,"details":{"returned":2}}`)
	for _, bounds := range []*agent.Bounds{nil, {Returned: 1, Truncated: true}} {
		require.Error(t, airuntime.ValidateSuccessfulToolResult(spec, semantic, bounds))
	}
}

func TestCompileBoundedResultUsesSeparateBounds(t *testing.T) {
	spec, err := contract.Compile(boundedDeclaration())
	require.NoError(t, err)
	value, err := spec.Result.Codec.FromJSON([]byte(`{"value":1,"details":{"returned":2},"returned":99,"total":99,"truncated":true,"refinement_hint":"old"}`))
	require.NoError(t, err)
	final, err := airuntime.EncodeCanonicalToolResult(spec, value, &agent.Bounds{Returned: 1, Truncated: false})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":1,"details":{"returned":2},"returned":1,"truncated":false}`, string(final))
}

func TestCompileUnboundedResultKeepsBusinessRequirements(t *testing.T) {
	declaration := boundedDeclaration()
	declaration.ConsumerContract.Bounds = nil
	declaration.ResultSchema = []byte(`{"type":"object","properties":{"returned":{"type":"string"},"total":{"type":"integer"}},"required":["returned","total"],"additionalProperties":false}`)
	declaration.ConsumerContract.Result.SchemaWithoutRootExample = declaration.ResultSchema
	spec, err := contract.Compile(declaration)
	require.NoError(t, err)
	value, err := spec.Result.Codec.FromJSON([]byte(`{"returned":"shipment","total":9007199254740993}`))
	require.NoError(t, err)
	encoded, err := spec.Result.Codec.ToJSON(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"returned":"shipment","total":9007199254740993}`, string(encoded))
	require.NoError(t, airuntime.ValidateSuccessfulToolResult(spec, rawjson.Message(encoded), nil))
	require.Error(t, airuntime.ValidateSuccessfulToolResult(spec, rawjson.Message(encoded), &agent.Bounds{}))
	for _, input := range []string{`{}`, `{"returned":"shipment"}`, `{"total":1}`} {
		_, err := spec.Result.Codec.FromJSON([]byte(input))
		require.Error(t, err)
		var value any
		require.NoError(t, json.Unmarshal([]byte(input), &value))
		_, err = spec.Result.Codec.ToJSON(value)
		require.Error(t, err)
	}
}

// boundedDeclaration supplies a public schema requiring counts and truncation
// and a nested business field named returned whose requirement must survive.
func boundedDeclaration() *genregistry.ToolSchema {
	payload := []byte(`{"type":"object","additionalProperties":false}`)
	result := []byte(`{"type":"object","properties":{"value":{"type":"integer"},"details":{"type":"object","properties":{"returned":{"type":"integer"}},"required":["returned"],"additionalProperties":false},"returned":{"type":"integer"},"truncated":{"type":"boolean"},"total":{"type":"integer"},"refinement_hint":{"type":"string"}},"required":["value","details","returned","truncated"],"additionalProperties":false}`)
	return &genregistry.ToolSchema{
		Name: "records.read", PayloadSchema: payload, ExecutionPayloadSchema: payload, ResultSchema: result,
		ConsumerContract: &genregistry.ConsumerContract{
			Kind: "service", Title: "Read records",
			Search:  &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"records": 1}},
			Payload: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: payload},
			Result:  &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: result},
			Bounds:  &genregistry.ToolBounds{},
		},
	}
}
