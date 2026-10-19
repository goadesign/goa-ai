// Direct native operations accept the complete execution input. Model-only
// input restrictions remain separate from direct call and saved-call validation.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestComposedOperationAcceptsFirstAndPageInput(t *testing.T) {
	operation := testNativeRegistryResolution("revision/1").Toolset.Tools[0]
	operation.ExecutionPayloadSchema = []byte(composedExecutionSchema)
	operation.PayloadSchema = []byte(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"]}`)
	operation.ConsumerContract.Payload.SchemaWithoutRootExample = operation.PayloadSchema
	operation.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{ContinueTool: &operation.Name, CursorField: "cursor", NextCursorField: "next_cursor"}}
	for _, input := range []string{`{"query":"active"}`, `{"query":"active","cursor":"next"}`} {
		request, err := prepareOperationRequest("", "operation", operation, rawjson.Message(input), nil, nil)
		require.NoError(t, err)
		assert.Equal(t, input, string(request.Input.ToolArgs))
		require.NoError(t, validateDirectOperationInput(request.Input))
	}
	_, err := prepareOperationRequest("", "operation", operation, rawjson.Message(`{"cursor":"next"}`), nil, nil)
	require.Error(t, err)
}
