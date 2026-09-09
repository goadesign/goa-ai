// These tests exercise real request-owned schema validation. Complete authored
// examples guide replacement calls without accepting, rewriting, or retaining
// invalid arguments as successful model output.
package model

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestToolExampleCorrectionPreservesCompleteShapeAcrossFailures(t *testing.T) {
	const schema = `{"type":"object","properties":{"requests":{"type":"array","items":{"type":"object","properties":{"request":{"type":"object","properties":{"type":{"enum":["query","signal"]},"value":{"type":"string"}},"required":["type","value"],"additionalProperties":false}},"required":["request"],"additionalProperties":false}},"time_selection":{"type":"object","properties":{"window":{"type":"object","properties":{"type":{"enum":["lookback"]},"value":{"type":"object","properties":{"amount":{"type":"integer"},"unit":{"enum":["hour","day"]}},"required":["amount","unit"],"additionalProperties":false}},"required":["type","value"],"additionalProperties":false}},"required":["window"],"additionalProperties":false}},"required":["requests","time_selection"],"additionalProperties":false}`
	const example = `{ "requests": [{"request":{"type":"query","value":"example quantity"}}], "time_selection":{"window":{"type":"lookback","value":{"amount":7,"unit":"day"}}} }`
	definition := exampleCorrectionDefinition(schema, example, testFieldMetadata(map[string]string{
		"requests.*.request": "object", "time_selection.window": "object", "time_selection.window.value.unit": "string",
	}, nil))
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	// Later mutation cannot change the example attached to this request.
	definition.Input.exampleJSON[0] = '!'
	for _, test := range []struct{ payload, field string }{
		{`{"requests":[{"query":"submitted quantity"}],"time_selection":{"window":{"type":"lookback","value":{"amount":12,"unit":"hour"}}}}`, `Field "requests.*.request" is required.`},
		{`{"requests":[{"request":{"type":"query","value":"submitted quantity"}}],"time_selection":{"type":"lookback","value":{"amount":12,"unit":"hour"}}}`, `Field "time_selection.window" is required.`},
		{`{"requests":[{"request":{"type":"query","value":"submitted quantity"}}],"time_selection":{"window":{"type":"lookback","value":{"amount":12,"unit":"hours"}}}}`, `Field "time_selection.window.value.unit" must contain one of these JSON values: ["hour","day"].`},
	} {
		response := responseWithToolCall(ToolCall{Name: "catalog.lookup", ID: "rejected-call", Payload: rawjson.Message(test.payload)})
		accepted, err := contract.ValidateResponse(response)
		require.Nil(t, accepted)
		var rejected *OutputValidationError
		require.ErrorAs(t, err, &rejected)
		var schemaErr *jsonschema.ValidationError
		require.ErrorAs(t, rejected, &schemaErr)
		assert.Contains(t, rejected.RecoveryCorrection(), test.field)
		assert.True(t, strings.HasSuffix(rejected.RecoveryCorrection(), example))
		assert.Equal(t, 1, strings.Count(rejected.RecoveryCorrection(), example))
		assert.Contains(t, rejected.RecoveryCorrection(), "use values and a valid variant appropriate to the request")
		assert.NotContains(t, rejected.RecoveryCorrection(), "submitted quantity")
		assert.NotContains(t, rejected.RecoveryCorrection(), "catalog.lookup")
		saved, err := rejected.RejectedResponse()
		require.NoError(t, err)
		assert.Equal(t, test.payload, string(saved.ToolCalls()[0].Payload))
		stream, err := contract.ValidateStream(&validatedStreamFixture{
			chunks: []Chunk{ToolCallChunk{ToolCall: response.ToolCalls()[0]}, StopChunk{Reason: "tool_use"}}, response: response,
		})
		require.NoError(t, err)
		chunk, err := stream.Recv()
		assert.Nil(t, chunk, "invalid calls must not become executable chunks")
		var streamed *OutputValidationError
		require.ErrorAs(t, err, &streamed)
		assert.Equal(t, rejected.RecoveryCorrection(), streamed.RecoveryCorrection())
		require.NoError(t, stream.Close())
	}
	// A different legal branch and user values remain valid, not rewritten to
	// the example's query branch, seven-day duration, or quantity.
	const valid = `{ "requests":[{"request":{"type":"signal","value":"chosen.point"}}],"time_selection":{"window":{"type":"lookback","value":{"amount":12,"unit":"hour"}}}}`
	accepted, err := contract.ValidateResponse(responseWithToolCall(ToolCall{Name: "catalog.lookup", ID: "valid", Payload: rawjson.Message(valid)}))
	require.NoError(t, err)
	assert.Equal(t, valid, string(accepted.ToolCalls()[0].Payload)) //nolint:testifylint // Exact bytes prove accepted arguments were not rewritten.
}

func TestToolExampleCorrectionByteLimit(t *testing.T) {
	const schema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	fields := testFieldMetadata(map[string]string{"query": "string"}, nil)
	get := func(example string) string {
		definition := exampleCorrectionDefinition(schema, example, fields)
		contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
		require.NoError(t, err)
		accepted, err := contract.ValidateResponse(responseWithToolCall(ToolCall{Name: "catalog.lookup", ID: "bad", Payload: rawjson.Message(`{}`)}))
		require.Nil(t, accepted)
		var rejected *OutputValidationError
		require.ErrorAs(t, err, &rejected)
		return rejected.RecoveryCorrection()
	}
	base := get("")
	assert.Equal(t, `Field "query" is required. Return a replacement tool call with valid arguments.`, base)
	const small = `{"query":"é"}`
	withSmall := get(small)
	// The exact limit includes UTF-8 bytes, field text, and all instructions.
	remaining := correction.MaxBytes - len(withSmall)
	atLimit := strings.Replace(small, "é", "é"+strings.Repeat("a", remaining), 1)
	acceptedExample := get(atLimit)
	assert.Len(t, acceptedExample, correction.MaxBytes)
	assert.True(t, utf8.ValidString(acceptedExample))
	assert.True(t, strings.HasSuffix(acceptedExample, atLimit))
	assert.True(t, json.Valid([]byte(atLimit)))
	overLimit := strings.Replace(atLimit, "é", "éa", 1)
	assert.Equal(t, base, get(overLimit), "oversized advisory metadata is omitted whole, never truncated")
}

func TestToolExampleCorrectionRequestIsolation(t *testing.T) {
	const schema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	for _, example := range []string{`{"query":"first"}`, `{"query":"second"}`} {
		t.Run(example, func(t *testing.T) {
			t.Parallel()
			definition := exampleCorrectionDefinition(schema, example, nil)
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
			require.NoError(t, err)
			_, err = contract.ValidateResponse(responseWithToolCall(ToolCall{Name: "catalog.lookup", ID: "same-id", Payload: rawjson.Message(`{}`)}))
			var rejected *OutputValidationError
			require.ErrorAs(t, err, &rejected)
			assert.True(t, strings.HasPrefix(rejected.RecoveryCorrection(), advertisedToolInputCorrection))
			assert.True(t, strings.HasSuffix(rejected.RecoveryCorrection(), example))
		})
	}
}

func exampleCorrectionDefinition(schema, example string, fields []tools.FieldMetadata) *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{Name: "catalog.lookup", Payload: tools.TypeSpec{
		Name: "Lookup", Schema: rawjson.Message(schema), SchemaWithoutRootExample: rawjson.Message(schema),
		ExampleJSON: rawjson.Message(example), Fields: fields, Codec: tools.AnyJSONCodec,
	}})
}
