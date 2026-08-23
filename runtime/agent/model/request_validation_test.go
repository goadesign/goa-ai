package model

// This file verifies that request-owned output rules remain immutable across
// provider calls. Providers may report the concrete model they selected, but
// they cannot replace the caller's logical model class.

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestRequestContractOwnsUsageModelClass(t *testing.T) {
	request := &Request{Model: "requested-model", ModelClass: ModelClassSmall}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Usage = TokenUsage{
		Model:      "provider-model",
		ModelClass: ModelClassHighReasoning,
	}

	validated, err := contract.ValidateResponse(response)
	require.NoError(t, err)
	require.Equal(t, "provider-model", validated.Usage.Model)
	require.Equal(t, ModelClassSmall, validated.Usage.ModelClass)
	require.Equal(t, ModelClassHighReasoning, response.Usage.ModelClass)
}

func TestRequestContractFillsMissingUsageModelFromRequest(t *testing.T) {
	request := &Request{Model: "requested-model", ModelClass: ModelClassSmall}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Usage = TokenUsage{TotalTokens: 3}

	validated, err := contract.ValidateResponse(response)

	require.NoError(t, err)
	require.Equal(t, "requested-model", validated.Usage.Model)
	require.Equal(t, ModelClassSmall, validated.Usage.ModelClass)
	require.Empty(t, response.Usage.Model)
}

func TestRequestContractOwnsStreamUsageModelClass(t *testing.T) {
	request := &Request{Model: "requested-model", ModelClass: ModelClassSmall}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Usage = TokenUsage{
		Model:      "provider-model",
		ModelClass: ModelClassHighReasoning,
	}
	stream, err := contract.ValidateStream(&validatedStreamFixture{
		chunks: []Chunk{
			UsageChunk{Usage: TokenUsage{
				Model:      "provider-model",
				ModelClass: ModelClassHighReasoning,
			}},
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "ok"}},
			}},
			StopChunk{Reason: "end_turn"},
		},
		response: response,
	})
	require.NoError(t, err)

	chunk, err := stream.Recv()
	require.NoError(t, err)
	usage := chunk.(UsageChunk).Usage
	require.Equal(t, "provider-model", usage.Model)
	require.Equal(t, ModelClassSmall, usage.ModelClass)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestRequestContractReturnsImmutableOutputValidationError(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	rejected := canonicalTextResponse()
	rejected.StopReason = ""

	owned, err := contract.ValidateResponse(rejected)
	require.Nil(t, owned)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.True(t, validationErr.Evidence().Present)
	require.NotEmpty(t, validationErr.Evidence().SHA256)
	require.ErrorContains(t, errors.Unwrap(validationErr), "stop reason")
	rejected.Content[0].Parts[0] = TextPart{Text: "provider-mutated"}

	first, err := validationErr.RejectedResponse()
	require.NoError(t, err)
	first.Content[0].Parts[0] = TextPart{Text: "mutated"}
	second, err := validationErr.RejectedResponse()
	require.NoError(t, err)
	require.Equal(t, "ok", second.Content[0].Parts[0].(TextPart).Text)
}

func TestRequestContractEnforcesToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		choice   *ToolChoice
		response *Response
		wantErr  string
	}{
		{
			name:     "none accepts no calls",
			choice:   &ToolChoice{Mode: ToolChoiceModeNone},
			response: canonicalTextResponse(),
		},
		{
			name:     "none rejects calls",
			choice:   &ToolChoice{Mode: ToolChoiceModeNone},
			response: toolResponse("first"),
			wantErr:  "tool choice none",
		},
		{
			name:     "any requires a call",
			choice:   &ToolChoice{Mode: ToolChoiceModeAny},
			response: canonicalTextResponse(),
			wantErr:  "tool choice any",
		},
		{
			name:     "any accepts a call",
			choice:   &ToolChoice{Mode: ToolChoiceModeAny},
			response: toolResponse("first"),
		},
		{
			name:     "named choice rejects another advertised tool",
			choice:   &ToolChoice{Mode: ToolChoiceModeTool, Name: "first"},
			response: toolResponse("second"),
			wantErr:  `requires "first"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &Request{
				Tools: []*ToolDefinition{
					advertisedTool("first"),
					advertisedTool("second"),
				},
				ToolChoice: test.choice,
			}
			contract, err := NewRequestContract(request)
			require.NoError(t, err)
			_, err = contract.ValidateResponse(test.response)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			var validationErr *OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestRequestContractSnapshotsToolChoice(t *testing.T) {
	request := &Request{
		Tools:      []*ToolDefinition{advertisedTool("first")},
		ToolChoice: &ToolChoice{Mode: ToolChoiceModeNone},
	}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	request.ToolChoice.Mode = ToolChoiceModeAny

	_, err = contract.ValidateResponse(toolResponse("first"))
	require.ErrorContains(t, err, "tool choice none")
}

func TestRequestContractEnforcesToolChoiceAtStreamEOF(t *testing.T) {
	request := &Request{
		Tools:      []*ToolDefinition{advertisedTool("first")},
		ToolChoice: &ToolChoice{Mode: ToolChoiceModeAny},
	}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	stream, err := contract.ValidateStream(&validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "no tool"}},
			}},
			StopChunk{Reason: "end_turn"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "no tool"}},
			}},
			StopReason: "end_turn",
		},
	})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, err, "tool choice any")
}

func TestNewRequestContractRejectsUnadvertisedToolChoice(t *testing.T) {
	_, err := NewRequestContract(&Request{
		Tools:      []*ToolDefinition{advertisedTool("first")},
		ToolChoice: &ToolChoice{Mode: ToolChoiceModeTool, Name: "missing"},
	})
	require.ErrorContains(t, err, `unadvertised tool "missing"`)

	var validationErr *OutputValidationError
	require.NotErrorAs(t, err, &validationErr)
}

func TestNewRequestContractAcceptsStructuredOutputWithoutLocalValidator(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		StructuredOutput: &StructuredOutput{Name: "answer"},
	})

	require.NoError(t, err)
	require.NotNil(t, contract)
}

func TestNewRequestContractRejectsStructuredOutputWithoutName(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		StructuredOutput: &StructuredOutput{Schema: rawjson.Message(`{"type":"object"}`)},
	})

	require.Nil(t, contract)
	require.EqualError(t, err, "model request structured output name is required")
}

func TestRequestContractValidatesUnaryStructuredOutputEnvelope(t *testing.T) {
	request := &Request{StructuredOutput: &StructuredOutput{
		Name:   "answer",
		Schema: rawjson.Message(`{"type":"object"}`),
	}}
	require.NoError(t, SetCompletionValidator(
		request,
		func(*Response, *Completion) error { return nil },
	))
	contract, err := NewRequestContract(request)
	require.NoError(t, err)

	valid := canonicalTextResponse()
	valid.Content[0].Parts[0] = TextPart{Text: " {\n  \"answer\": true\n} "}
	owned, err := contract.ValidateResponse(valid)
	require.NoError(t, err)
	require.Equal(t, " {\n  \"answer\": true\n} ", owned.Content[0].Parts[0].(TextPart).Text)

	invalid := canonicalTextResponse()
	invalid.Content[0].Parts[0] = TextPart{Text: "not json"}
	_, err = contract.ValidateResponse(invalid)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, err, "not valid JSON")
}

func TestRequestContractRunsGeneratedStructuredOutputDecoder(t *testing.T) {
	request := &Request{StructuredOutput: &StructuredOutput{
		Name:   "answer",
		Schema: rawjson.Message(`{"type":"object"}`),
	}}
	require.NoError(t, SetCompletionValidator(request, func(response *Response, completion *Completion) error {
		require.NotNil(t, response)
		require.Nil(t, completion)
		return errors.New("generated decoder rejected payload")
	}))
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Content[0].Parts[0] = TextPart{Text: `{}`}

	_, err = contract.ValidateResponse(response)
	require.ErrorContains(t, err, "generated decoder rejected payload")
}

func canonicalTextResponse() *Response {
	return &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "ok"}},
		}},
		StopReason: "end_turn",
	}
}

func advertisedTool(name string) *ToolDefinition {
	return &ToolDefinition{
		Name:  name,
		Input: AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
	}
}

func toolResponse(name string) *Response {
	return &Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:    "call-1",
				Name:  name,
				Input: rawjson.Message(`{}`),
			}},
		}},
		StopReason: "tool_use",
	}
}
