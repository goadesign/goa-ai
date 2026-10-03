// These tests verify that portable model encoders omit only declared controls
// and retain exact domain values without changing the executor's input.
package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestTextOnlyPortableModelEncoderPreservesDomainContent(t *testing.T) {
	document := []byte(`{"type":"object","properties":{"selection":{"type":"object","additionalProperties":true}},"required":["selection"],"additionalProperties":false}`)
	selected, err := compileType(document, &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document, Fields: []*genregistry.ToolFieldMetadata{{Path: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("selection")}}}}})
	require.NoError(t, err)
	ordinary := selected
	ordinary.Fields = append(tools.CloneFieldMetadata(selected.Fields), tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("render_ui")}, JSONType: "boolean"})
	codec := compileTextOnlyModelCodec(ordinary, selected)
	input := map[string]any{"selection": map[string]any{"render_ui": "domain text", "series": []any{map[string]any{"kind": "reading", "value": json.Number("9007199254740993")}}}, "render_ui": false}
	encoded, err := codec.ToJSON(input)
	require.NoError(t, err)
	assert.JSONEq(t, `{"selection":{"render_ui":"domain text","series":[{"kind":"reading","value":9007199254740993}]}}`, string(encoded))
	assert.Equal(t, false, input["render_ui"])
	_, err = codec.FromJSON([]byte(`{"selection":{},"render_ui":false}`))
	require.Error(t, err)
	input["undeclared"] = true
	_, err = codec.ToJSON(input)
	require.Error(t, err)
}

func TestTextOnlyPortableModelEncoderPreservesRootShapes(t *testing.T) {
	for _, test := range []struct {
		name, schema, input string
		fields              []tools.FieldMetadata
	}{
		{"string", `{"type":"string"}`, `"chart text"`, nil},
		{"array", `{"type":"array","items":{"type":"string"}}`, `["one","render_ui"]`, nil},
		{"open map", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"render_ui":"domain value","other":"value"}`, []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.DynamicField{}}, JSONType: "string"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, err := compileType([]byte(test.schema), &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: []byte(test.schema)})
			require.NoError(t, err)
			spec.Fields = test.fields
			codec := compileTextOnlyModelCodec(spec, spec)
			value, err := codec.FromJSON([]byte(test.input))
			require.NoError(t, err)
			encoded, err := codec.ToJSON(value)
			require.NoError(t, err)
			assert.JSONEq(t, test.input, string(encoded))
		})
	}
}
