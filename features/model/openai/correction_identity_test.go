// These tests pass malformed Responses API arguments through real unary and
// streaming translation. Only a name resolved through the current request can
// identify a contract in correction guidance.
package openai

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestMalformedCorrectionUsesMappedNameInUnaryAndStream(t *testing.T) {
	for _, providerName := range []string{"catalog_lookup", "undeclared_alias"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", providerName, streaming), func(t *testing.T) {
				raw := fmt.Sprintf(`{
					"model":"gpt-4o","status":"completed",
					"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7},
					"output":[{"id":"fc_1","type":"function_call","call_id":"private-call",
						"name":%q,"arguments":"{\"private-value\":","status":"completed"}]
				}`, providerName)
				var response responses.Response
				require.NoError(t, json.Unmarshal([]byte(raw), &response))
				client, err := New(Options{
					DefaultModel: "gpt-4o",
					transport: &mockTransport{
						completeResponse: &response,
						stream: &mockStream{events: []responses.ResponseStreamEventUnion{
							mustStreamEvent(t, `{"type":"response.completed","sequence_number":1,"response":`+raw+`}`),
						}},
					},
				})
				require.NoError(t, err)
				request := openAIToolRequest()
				request.Tools[0].Name = "catalog.lookup"
				if streaming {
					stream, streamErr := client.Stream(t.Context(), request)
					require.NoError(t, streamErr)
					chunk, recvErr := stream.Recv()
					assert.Nil(t, chunk)
					err = recvErr
					assert.Nil(t, stream.Response())
					require.NoError(t, stream.Close())
				} else {
					accepted, completeErr := client.Complete(t.Context(), request)
					assert.Nil(t, accepted)
					err = completeErr
				}
				var rejected *model.OutputValidationError
				require.ErrorAs(t, err, &rejected)
				assert.Equal(t, 7, rejected.Usage().TotalTokens)
				if providerName == "catalog_lookup" {
					assert.Equal(t, model.OutputValidationToolArguments, rejected.Kind())
					assert.Equal(t, "Input contract \"catalog.lookup\" (diagnostic identifier, not a callable tool name):\nThe previous tool call arguments were not valid JSON. Tool arguments must be one JSON object matching the advertised input schema.", rejected.RecoveryCorrection())
				} else {
					assert.Equal(t, model.OutputValidationToolIdentity, rejected.Kind())
					assert.Empty(t, rejected.RecoveryCorrection())
				}
				for _, private := range []string{providerName, "private-call", "private-value"} {
					assert.NotContains(t, rejected.RecoveryCorrection(), private)
				}
			})
		}
	}
}
