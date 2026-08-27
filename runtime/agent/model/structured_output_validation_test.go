// These tests verify that one request-owned JSON Schema governs final
// structured output for unary and streaming model calls.
package model

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestContractAcceptsEveryStructuredOutputRootType(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		payload string
	}{
		{
			name:    "object",
			schema:  `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`,
			payload: `{"value":"ok"}`,
		},
		{
			name:    "array",
			schema:  `{"type":"array","items":{"type":"integer"}}`,
			payload: `[1,2,3]`,
		},
		{
			name:    "string containing object-looking text",
			schema:  `{"type":"string"}`,
			payload: `"{\"value\":\"ok\"}"`,
		},
		{
			name:    "integer with exact large value",
			schema:  `{"type":"integer","const":9007199254740993}`,
			payload: `9007199254740993`,
		},
		{
			name:    "number",
			schema:  `{"type":"number"}`,
			payload: `1.25`,
		},
		{
			name:    "boolean",
			schema:  `{"type":"boolean"}`,
			payload: `true`,
		},
		{
			name:    "null",
			schema:  `{"type":"null"}`,
			payload: `null`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := NewRequestContract(structuredOutputRequest(test.schema))
			require.NoError(t, err)

			response := structuredOutputResponse(test.payload)
			validated, err := contract.ValidateResponse(response)

			require.NoError(t, err)
			require.Equal(t, test.payload, validated.Content[0].Parts[0].(TextPart).Text)
		})
	}
}

func TestRequestContractRejectsStructuredOutputSchemaViolations(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		payload string
	}{
		{
			name:    "object schema rejects object-looking string",
			schema:  `{"type":"object"}`,
			payload: `"{\"value\":\"ok\"}"`,
		},
		{
			name: "nested wrong type",
			schema: `{
				"type":"object",
				"properties":{"settings":{
					"type":"object",
					"properties":{"enabled":{"type":"boolean"}},
					"required":["enabled"]
				}},
				"required":["settings"]
			}`,
			payload: `{"settings":{"enabled":"yes"}}`,
		},
		{
			name:    "missing required field",
			schema:  `{"type":"object","required":["value"]}`,
			payload: `{}`,
		},
		{
			name:    "bad array item",
			schema:  `{"type":"array","items":{"type":"integer"}}`,
			payload: `[1,"two"]`,
		},
		{
			name:    "unknown property",
			schema:  `{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`,
			payload: `{"value":"ok","extra":true}`,
		},
		{
			name:    "enum mismatch",
			schema:  `{"enum":["ready","done"]}`,
			payload: `"waiting"`,
		},
		{
			name:    "adjacent large integer",
			schema:  `{"type":"integer","const":9007199254740992}`,
			payload: `9007199254740993`,
		},
		{
			name:    "union mismatch",
			schema:  `{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
			payload: `true`,
		},
		{
			name:    "number exceeds bound",
			schema:  `{"type":"number","minimum":1,"maximum":2}`,
			payload: `2.0001`,
		},
		{
			name:    "trailing JSON",
			schema:  `{"type":"object"}`,
			payload: `{} []`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := NewRequestContract(structuredOutputRequest(test.schema))
			require.NoError(t, err)
			response := structuredOutputResponse(test.payload)
			response.Usage.TotalTokens = 7

			validated, err := contract.ValidateResponse(response)

			require.Nil(t, validated)
			var validationErr *OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			require.True(t, validationErr.Evidence().Present)
			require.NotEmpty(t, validationErr.Evidence().SHA256)
			require.Equal(t, 7, validationErr.Usage().TotalTokens)
		})
	}
}

func TestNewRequestContractRejectsUncompilableStructuredOutputSchemas(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{
			name:   "invalid schema keyword",
			schema: `{"type":"not-a-json-type"}`,
		},
		{
			name:   "external reference",
			schema: `{"$ref":"https://example.com/external.json"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := NewRequestContract(structuredOutputRequest(test.schema))

			require.Nil(t, contract)
			require.ErrorContains(t, err, "structured output schema")
		})
	}
}

func TestRequestContractsKeepSameNameSchemasIsolated(t *testing.T) {
	objectContract, err := NewRequestContract(structuredOutputRequest(`{"type":"object"}`))
	require.NoError(t, err)
	stringContract, err := NewRequestContract(structuredOutputRequest(`{"type":"string"}`))
	require.NoError(t, err)

	const validations = 32
	var wait sync.WaitGroup
	errs := make(chan error, validations*2)
	for range validations {
		wait.Add(2)
		go validateStructuredOutputConcurrently(
			&wait,
			errs,
			objectContract,
			structuredOutputResponse(`{"value":"ok"}`),
		)
		go validateStructuredOutputConcurrently(
			&wait,
			errs,
			stringContract,
			structuredOutputResponse(`"{\"value\":\"ok\"}"`),
		)
	}
	wait.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	_, err = objectContract.ValidateResponse(structuredOutputResponse(`"{\"value\":\"ok\"}"`))
	require.Error(t, err)
	_, err = stringContract.ValidateResponse(structuredOutputResponse(`{"value":"ok"}`))
	require.Error(t, err)
}

func TestValidatedStreamChecksFinalCompletionAfterPreview(t *testing.T) {
	request := structuredOutputRequest(`{
		"type":"object",
		"properties":{"value":{"type":"integer"}},
		"required":["value"]
	}`)
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			CompletionDeltaChunk{Delta: CompletionDelta{
				Name:  "answer",
				Delta: `{"value":"`,
			}},
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`{"value":"wrong"}`),
			}},
		},
		response: structuredOutputResponse(`{"value":"wrong"}`),
	}, request)

	preview, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, `{"value":"`, preview.(CompletionDeltaChunk).Delta.Delta)

	final, err := stream.Recv()
	require.Nil(t, final)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Nil(t, stream.Response())
}

func TestValidatedStreamStillReconcilesCompletionWithTerminalResponse(t *testing.T) {
	request := structuredOutputRequest(`{
		"type":"object",
		"properties":{"value":{"type":"integer"}},
		"required":["value"]
	}`)
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	stream, err := contract.ValidateStream(&validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`{"value":1}`),
			}},
			StopChunk{Reason: "stop"},
		},
		response: structuredOutputResponse(`{"value":2}`),
	})
	require.NoError(t, err)

	final, err := stream.Recv()
	require.Nil(t, final)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.NotErrorIs(t, err, io.EOF)
	require.ErrorContains(t, err, "stream completion does not match canonical response")
	require.Nil(t, stream.Response())
}

func TestValidatedStreamRejectsSchemaValidRootMismatchBeforeFinalExposure(t *testing.T) {
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`[1]`),
			}},
			StopChunk{Reason: "stop"},
		},
		response: structuredOutputResponse(`{"value":1}`),
	}, structuredOutputRequest(`{"type":["array","object"]}`))

	final, err := stream.Recv()

	require.Nil(t, final)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, err, "stream completion does not match canonical response")
	require.Nil(t, stream.Response())
}

func TestValidatedStreamAlwaysReconcilesWithCustomValidator(t *testing.T) {
	request := structuredOutputRequest(`{"type":"object"}`)
	customCalls := 0
	require.NoError(t, SetCompletionValidator(request, func(*Response, *Completion) error {
		customCalls++
		return nil
	}))
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	stream, err := contract.ValidateStream(&validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`{"value":1}`),
			}},
			StopChunk{Reason: "stop"},
		},
		response: structuredOutputResponse(`{"value":2}`),
	})
	require.NoError(t, err)

	final, err := stream.Recv()

	require.Nil(t, final)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, err, "stream completion does not match canonical response")
	require.Equal(t, 1, customCalls)
}

func TestValidatedStreamPreservesBufferedOrderUsageAndIdentity(t *testing.T) {
	request := structuredOutputRequest(`{"type":"string"}`)
	request.ModelClass = ModelClassSmall
	response := structuredOutputResponse(`"ok"`)
	response.Usage = TokenUsage{
		Model:        "provider-model",
		InputTokens:  3,
		OutputTokens: 2,
		TotalTokens:  5,
	}
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`"ok"`),
			}},
			UsageChunk{Usage: response.Usage},
			StopChunk{Reason: "stop"},
		},
		response: response,
	}, request)

	completion, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, `"ok"`, string(completion.(CompletionChunk).Completion.Payload))
	usage, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "provider-model", usage.(UsageChunk).Usage.Model)
	require.Equal(t, ModelClassSmall, usage.(UsageChunk).Usage.ModelClass)
	stop, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "stop", stop.(StopChunk).Reason)
	final, err := stream.Recv()
	require.Nil(t, final)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, "provider-model", stream.Response().Usage.Model)
	require.Equal(t, ModelClassSmall, stream.Response().Usage.ModelClass)
}

func TestValidatedStreamWithholdsCompletionOnCancellation(t *testing.T) {
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`"ok"`),
			}},
		},
		recvErr:  context.Canceled,
		response: structuredOutputResponse(`"ok"`),
	}, structuredOutputRequest(`{"type":"string"}`))

	final, err := stream.Recv()

	require.Nil(t, final)
	require.ErrorIs(t, err, context.Canceled)
	var validationErr *OutputValidationError
	require.NotErrorAs(t, err, &validationErr)
	require.Nil(t, stream.Response())
}

func TestValidatedStreamWithholdsCompletionOnProviderError(t *testing.T) {
	providerErr := io.ErrUnexpectedEOF
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{
				Name:    "answer",
				Payload: []byte(`"ok"`),
			}},
			UsageChunk{Usage: TokenUsage{
				Model:        "provider-model",
				InputTokens:  3,
				OutputTokens: 2,
				TotalTokens:  5,
			}},
		},
		recvErr:  providerErr,
		response: structuredOutputResponse(`"ok"`),
	}, structuredOutputRequest(`{"type":"string"}`))

	usage, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "provider-model", usage.(UsageChunk).Usage.Model)
	final, err := stream.Recv()

	require.Nil(t, final)
	require.ErrorIs(t, err, providerErr)
	var validationErr *OutputValidationError
	require.NotErrorAs(t, err, &validationErr)
	require.Nil(t, stream.Response())
}

// validateStructuredOutputConcurrently sends one response through one captured
// request contract and reports whether that contract accepted it.
func validateStructuredOutputConcurrently(
	wait *sync.WaitGroup,
	errs chan<- error,
	contract *RequestContract,
	response *Response,
) {
	defer wait.Done()
	_, err := contract.ValidateResponse(response)
	errs <- err
}

// structuredOutputRequest builds a low-level request whose name remains the
// same across tests while its schema selects the accepted JSON value.
func structuredOutputRequest(schema string) *Request {
	return &Request{StructuredOutput: &StructuredOutput{
		Name:   "answer",
		Schema: []byte(schema),
	}}
}

// structuredOutputResponse places the exact JSON bytes in the unary assistant
// envelope that provider adapters return after extracting their wire format.
func structuredOutputResponse(payload string) *Response {
	return &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: payload}},
		}},
		StopReason: "stop",
	}
}
