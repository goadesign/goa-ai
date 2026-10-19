// Portable paging declarations fill the same fixed disabled controls as their
// generated first and page codecs, after validating the complete input.
package contract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestTextOnlyPagingExecutionDefaults(t *testing.T) {
	declaration := testDeclaration()
	declaration.ConsumerContract.Confirmation = nil
	declaration.ConsumerContract.Bounds.Paging.ContinueTool = &declaration.Name
	model := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	execution := []byte(`{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer"},"render_ui":{"type":"boolean","enum":[false],"default":false}},"required":["value"]},{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer"},"cursor":{"type":"string"},"page_ui":{"type":"boolean","enum":[false],"default":false}},"required":["value","cursor"]}]}`)
	declaration.ExecutionPayloadSchema = execution
	declaration.ConsumerContract.TextOnly = &genregistry.TextOnlyToolContract{
		Description:     "Read records.",
		Search:          declaration.ConsumerContract.Search,
		PayloadSchema:   model,
		Payload:         &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: model},
		ExecutionSchema: execution,
	}
	spec, err := Compile(declaration)
	require.NoError(t, err)
	for _, test := range []struct{ input, want string }{
		{`{"value":1}`, `{"value":1,"render_ui":false}`},
		{`{"value":1,"render_ui":false}`, `{"value":1,"render_ui":false}`},
		{`{"value":1,"cursor":""}`, `{"value":1,"cursor":"","page_ui":false}`},
		{`{"value":1,"cursor":"next","page_ui":false}`, `{"value":1,"cursor":"next","page_ui":false}`},
	} {
		value, err := spec.TextOnly.ExecutionCodec.FromJSON([]byte(test.input))
		require.NoError(t, err)
		encoded, err := spec.TextOnly.ExecutionCodec.ToJSON(value)
		require.NoError(t, err)
		assert.JSONEq(t, test.want, string(encoded))
	}
	for _, input := range []string{
		`{"value":1,"render_ui":true}`,
		`{"value":1,"cursor":"next","page_ui":true}`,
		`{"cursor":"next"}`,
		`{"value":1,"cursor":"next","render_ui":false}`,
	} {
		_, err := spec.TextOnly.ExecutionCodec.FromJSON([]byte(input))
		require.Error(t, err)
	}
	_, err = spec.TextOnly.Payload.Codec.FromJSON([]byte(`{"value":1,"render_ui":false}`))
	require.Error(t, err)
}
