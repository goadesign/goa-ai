// These checks retain the shared workflow argument budget for the exact JSON
// generated when a continuation is constructed.
package workflowcodec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/mcp"
)

func TestDataConverterMeasuresExecutionContinuation(t *testing.T) {
	state := strings.Repeat("x", engine.MaxPayloadBytes/2)
	operation, err := tooloperation.NewInput(&mcp.CallContinuation{RequestState: &state})
	require.NoError(t, err)
	converter := NewDataConverter()
	payload, err := converter.ToPayload(operation)
	require.NoError(t, err)
	var restored tooloperation.Continuation
	require.NoError(t, converter.FromPayload(payload, &restored))
	original, err := json.Marshal(operation)
	require.NoError(t, err)
	saved, err := json.Marshal(restored)
	require.NoError(t, err)
	assert.Equal(t, original, saved)
	_, err = converter.ToPayloads(operation, operation)
	assert.ErrorContains(t, err, "maximum aggregate size")
	_, err = converter.ToPayload(tooloperation.Continuation{})
	assert.Error(t, err)
}
