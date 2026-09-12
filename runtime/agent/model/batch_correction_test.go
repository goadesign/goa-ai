package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Both complete and streaming responses must report every invalid call in one
// rejected batch, without exposing any rejected call as an accepted response.
func TestBatchCorrectionReportsEveryInvalidCall(t *testing.T) {
	definition := arrayCorrectionDefinition(`{"type":"object","properties":{"items":{"type":"array","minItems":1,"maxItems":2}},"required":["items"]}`, testFieldMetadata(map[string]string{"items": "array"}, nil))
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	calls := []ToolCall{
		{ID: "first", Name: "catalog.batch", Payload: rawjson.Message(`{"items":[]}`)},
		{ID: "valid", Name: "catalog.batch", Payload: rawjson.Message(`{"items":[1]}`)},
		{ID: "third", Name: "catalog.batch", Payload: rawjson.Message(`{"items":[1,2,3]}`)},
	}
	response := responseWithToolCall(calls[0])
	for _, call := range calls[1:] {
		response.Content[0].Parts = append(response.Content[0].Parts, ToolUsePart{ID: call.ID, Name: string(call.Name), Input: call.Payload})
	}
	want := "Tool call 1, input contract \"catalog.batch\" (diagnostic identifier, not a callable tool name):\nField \"items\" must contain at least 1 items.\n\nTool call 3, input contract \"catalog.batch\" (diagnostic identifier, not a callable tool name):\nField \"items\" must contain at most 2 items."
	validated, err := contract.ValidateResponse(response)
	require.Nil(t, validated)
	var rejected *OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, want, rejected.RecoveryCorrection())
	assert.Contains(t, errors.Unwrap(rejected).Error(), "minItems")
	assert.Contains(t, errors.Unwrap(rejected).Error(), "maxItems")
	saved, err := rejected.RejectedResponse()
	require.NoError(t, err)
	assert.Equal(t, calls, saved.ToolCalls())
	stream, err := contract.ValidateStream(&validatedStreamFixture{chunks: []Chunk{ToolCallChunk{ToolCall: calls[0]}, ToolCallChunk{ToolCall: calls[1]}, ToolCallChunk{ToolCall: calls[2]}, StopChunk{Reason: "tool_use"}}, response: response})
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	var streamed *OutputValidationError
	require.ErrorAs(t, err, &streamed)
	assert.Equal(t, want, streamed.RecoveryCorrection())
	require.NoError(t, stream.Close())
}

func TestBatchCorrectionBudgetAndInternalFailures(t *testing.T) {
	const schema = `{"type":"object","properties":{"items":{"type":"array","minItems":1},"note":{"type":"string"}},"required":["items"]}`
	internal := errors.New("decoder unavailable")
	for _, test := range []struct {
		name, description, example   string
		decoderFailure, unadvertised bool
		wantRecovery, wantExample    bool
	}{
		{name: "complete examples", example: `{"items":[1]}`, wantRecovery: true, wantExample: true},
		{name: "omit optional examples", example: `{"items":[1],"note":"` + strings.Repeat("x", 2200) + `"}`, wantRecovery: true},
		{name: "field guidance cannot fit", description: strings.Repeat("x", 2200)},
		{name: "internal failure stays terminal", decoderFailure: true},
		{name: "unadvertised name retains identity failure", unadvertised: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := tools.ToolSpec{Name: "catalog.batch", Payload: tools.TypeSpec{
				Name: "Batch", Schema: rawjson.Message(schema), SchemaWithoutRootExample: rawjson.Message(schema), ExampleJSON: rawjson.Message(test.example),
				Fields: testFieldMetadata(map[string]string{"items": "array"}, map[string]string{"items": test.description}), Codec: tools.AnyJSONCodec,
			}}
			second := spec
			second.Name = "catalog.other"
			if test.decoderFailure {
				second.Payload.Codec = tools.JSONCodec[any]{FromJSON: func([]byte) (any, error) { return nil, internal }}
			}
			definitions := []*ToolDefinition{ToolDefinitionFromSpec(spec)}
			if !test.unadvertised {
				definitions = append(definitions, ToolDefinitionFromSpec(second))
			}
			contract, err := NewRequestContract(&Request{Tools: definitions})
			require.NoError(t, err)
			response := responseWithToolCall(ToolCall{ID: "first", Name: spec.Name, Payload: rawjson.Message(`{"items":[]}`)})
			payload := rawjson.Message(`{"items":[]}`)
			if test.decoderFailure {
				payload = rawjson.Message(`{"items":[1]}`)
			}
			response.Content[0].Parts = append(response.Content[0].Parts, ToolUsePart{ID: "second", Name: string(second.Name), Input: payload})
			validated, err := contract.ValidateResponse(response)
			require.Nil(t, validated)
			var rejected *OutputValidationError
			require.ErrorAs(t, err, &rejected)
			assert.Equal(t, test.wantRecovery, rejected.RecoveryCorrection() != "")
			assert.Equal(t, test.wantExample, strings.Contains(rejected.RecoveryCorrection(), "Example illustrates"))
			if test.wantRecovery {
				assert.Contains(t, rejected.RecoveryCorrection(), `Tool call 1, input contract "catalog.batch"`)
				assert.Contains(t, rejected.RecoveryCorrection(), `Tool call 2, input contract "catalog.other"`)
			}
			assert.Contains(t, errors.Unwrap(rejected).Error(), "minItems")
			switch {
			case test.decoderFailure:
				require.ErrorIs(t, err, internal)
			case test.unadvertised:
				name, found := UnadvertisedToolName(err)
				require.True(t, found)
				assert.Equal(t, string(second.Name), name)
			default:
				assert.Contains(t, errors.Unwrap(rejected).Error(), "catalog.other")
			}
		})
	}
}
