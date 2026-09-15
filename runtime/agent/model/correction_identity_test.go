// These tests verify that replacement guidance identifies only tools advertised
// by the rejected request. Names, field blocks and examples share the existing
// byte limit; rejected arguments and call IDs remain private.
package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestSingleCorrectionIdentifiesPositionAndCapturedContract(t *testing.T) {
	const schema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	definition := exampleCorrectionDefinition(schema, `{"query":"authored example"}`, testFieldMetadata(map[string]string{"query": "string"}, nil))
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition, advertisedTool("other.lookup")}})
	require.NoError(t, err)
	definition.Name = "mutated.name"
	response := toolResponse("other.lookup")
	response.Content[0].Parts = append(response.Content[0].Parts, ToolUsePart{
		Name: "catalog.lookup", ID: "private-rejected-id", Input: rawjson.Message(`{"query":42}`),
	})

	_, err = contract.ValidateResponse(response)

	var rejected *OutputValidationError
	require.ErrorAs(t, err, &rejected)
	const heading = "Tool call 2, input contract \"catalog.lookup\" (diagnostic identifier, not a callable tool name):\n"
	assert.True(t, strings.HasPrefix(rejected.RecoveryCorrection(), heading+`Field "query" must contain a JSON string.`))
	assert.True(t, strings.HasSuffix(rejected.RecoveryCorrection(), `{"query":"authored example"}`))
	for _, private := range []string{"mutated.name", "other.lookup", "private-rejected-id", "42"} {
		assert.NotContains(t, rejected.RecoveryCorrection(), private)
	}
	stream, err := contract.ValidateStream(&validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: response.ToolCalls()[0]},
			ToolCallChunk{ToolCall: response.ToolCalls()[1]},
			StopChunk{Reason: "tool_use"},
		},
		response: response,
	})
	require.NoError(t, err)
	chunk, err := stream.Recv()
	assert.Nil(t, chunk, "even the valid call in a rejected response must not execute")
	var streamed *OutputValidationError
	require.ErrorAs(t, err, &streamed)
	assert.Equal(t, rejected.RecoveryCorrection(), streamed.RecoveryCorrection())
	require.NoError(t, stream.Close())
}

func TestNamedCorrectionIncludesHeadingInByteLimit(t *testing.T) {
	const schema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	const base = `Field "query" is required. Field description: "".`
	for _, size := range []int{correction.MaxBytes - 1, correction.MaxBytes, correction.MaxBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			description := strings.Repeat("x", size-len(catalogCorrectionHeading)-len(base)-2) + "é"
			definition := exampleCorrectionDefinition(schema, "", testFieldMetadata(
				map[string]string{"query": "string"}, map[string]string{"query": description},
			))
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
			require.NoError(t, err)
			response := toolResponse("catalog.lookup")
			_, err = contract.ValidateResponse(response)
			var rejected *OutputValidationError
			require.ErrorAs(t, err, &rejected)
			if size <= correction.MaxBytes {
				want := catalogCorrectionHeading + fmt.Sprintf(`Field "query" is required. Field description: %q.`, description)
				assert.Equal(t, want, rejected.RecoveryCorrection())
				assert.Len(t, rejected.RecoveryCorrection(), size)
				assert.True(t, utf8.ValidString(rejected.RecoveryCorrection()))
			} else {
				assert.Empty(t, rejected.RecoveryCorrection(), "a required block cannot be cut to make it fit")
			}
			saved, err := rejected.RejectedResponse()
			require.NoError(t, err)
			assert.Equal(t, response.ToolCalls(), saved.ToolCalls())
			stream, err := contract.ValidateStream(&validatedStreamFixture{
				chunks:   []Chunk{ToolCallChunk{ToolCall: response.ToolCalls()[0]}, StopChunk{Reason: "tool_use"}},
				response: response,
			})
			require.NoError(t, err)
			_, err = stream.Recv()
			var streamed *OutputValidationError
			require.ErrorAs(t, err, &streamed)
			assert.Equal(t, rejected.RecoveryCorrection(), streamed.RecoveryCorrection())
			require.NoError(t, stream.Close())
		})
	}
}

func TestMalformedCorrectionRequiresRequestOwnedIdentity(t *testing.T) {
	const input = `{"private-value":`
	for _, advertised := range []bool{true, false} {
		t.Run(fmt.Sprintf("advertised=%t", advertised), func(t *testing.T) {
			definition := advertisedTool("lookup")
			request := &Request{}
			if advertised {
				request.Tools = []*ToolDefinition{definition}
			}
			contract, err := NewRequestContract(request)
			require.NoError(t, err)
			definition.Name = "mutated.name"
			request.Tools = []*ToolDefinition{advertisedTool("other")}
			response := responseWithToolCall(ToolCall{
				Name: "lookup", ID: "private-call-id", Payload: rawjson.Message(input),
			})
			_, err = contract.ValidateResponse(response)
			var rejected *OutputValidationError
			require.ErrorAs(t, err, &rejected)
			assert.Equal(t, OutputValidationToolArguments, rejected.Kind())
			if advertised {
				assert.Equal(t, malformedLookupCorrectionHeading+malformedToolArgumentsCorrection, rejected.RecoveryCorrection())
			} else {
				assert.Empty(t, rejected.RecoveryCorrection())
			}
			assert.NotContains(t, rejected.RecoveryCorrection(), "private-")
			assert.NotContains(t, rejected.RecoveryCorrection(), "mutated.name")
			saved, err := rejected.RejectedResponse()
			require.NoError(t, err)
			assert.Equal(t, input, string(saved.ToolCalls()[0].Payload))
			stream, err := contract.ValidateStream(&validatedStreamFixture{
				chunks: []Chunk{ToolCallChunk{ToolCall: response.ToolCalls()[0]}}, response: response,
			})
			require.NoError(t, err)
			chunk, err := stream.Recv()
			assert.Nil(t, chunk)
			var streamed *OutputValidationError
			require.ErrorAs(t, err, &streamed)
			assert.Equal(t, rejected.RecoveryCorrection(), streamed.RecoveryCorrection())
			require.NoError(t, stream.Close())
		})
	}
}

func TestMalformedMarkerRetainsCauseWithoutAuthorizingOtherFailures(t *testing.T) {
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{advertisedTool("lookup"), advertisedTool("other")}})
	require.NoError(t, err)
	private := errors.New(`private parser cause {"secret":`)
	malformed := NewMalformedToolArgumentsError("lookup", private)
	otherFailure := errors.New("internal cleanup failed")
	for _, cause := range []error{
		private,
		NewMalformedToolArgumentsError("unadvertised-provider-text", private),
		errors.Join(malformed, otherFailure),
		errors.Join(malformed, NewMalformedToolArgumentsError("other", private)),
	} {
		rejected := contract.RejectProviderOutput(OutputValidationToolArguments, nil, cause)
		assert.Empty(t, rejected.RecoveryCorrection())
		require.ErrorIs(t, rejected, private)
	}
	rejected := contract.RejectProviderOutput(OutputValidationToolArguments, nil, malformed)
	assert.Equal(t, malformedLookupCorrectionHeading+malformedToolArgumentsCorrection, rejected.RecoveryCorrection())
	require.ErrorIs(t, rejected, private)
	require.PanicsWithValue(t, "model: malformed tool arguments require a tool name", func() {
		require.NoError(t, NewMalformedToolArgumentsError("", private))
	})
}

func TestMalformedCorrectionNeverTruncatesContractName(t *testing.T) {
	const prefix = "Input contract \""
	const suffix = "\" (diagnostic identifier, not a callable tool name):\n"
	for _, size := range []int{correction.MaxBytes - 1, correction.MaxBytes, correction.MaxBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			name := strings.Repeat("x", size-len(prefix)-len(suffix)-len(malformedToolArgumentsCorrection)-2) + "é"
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{advertisedTool(name)}})
			require.NoError(t, err)
			rejected := contract.RejectProviderOutput(OutputValidationToolArguments, nil,
				NewMalformedToolArgumentsError(tools.Ident(name), errors.New("private cause")),
			)
			if size > correction.MaxBytes {
				assert.Empty(t, rejected.RecoveryCorrection())
				return
			}
			assert.Equal(t, prefix+name+suffix+malformedToolArgumentsCorrection, rejected.RecoveryCorrection())
			assert.Len(t, rejected.RecoveryCorrection(), size)
			assert.True(t, utf8.ValidString(rejected.RecoveryCorrection()))
		})
	}
}
