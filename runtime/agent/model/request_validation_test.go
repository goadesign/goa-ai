package model

// This file verifies that request-owned output rules remain immutable across
// provider calls. Providers may report the concrete model they selected, but
// they cannot replace the caller's logical model class.

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// mustAdvertisedToolInput compiles a static test schema.
func mustAdvertisedToolInput(schema rawjson.Message) ToolInput {
	input, err := AdvertisedToolInputFromSchema(schema)
	if err != nil {
		panic(err)
	}
	return input
}

func TestCallerAuthoredToolSchemaRequiresOneObjectDocument(t *testing.T) {
	tests := []struct {
		name   string
		schema rawjson.Message
	}{
		{name: "scalar root", schema: rawjson.Message(`{"type":"string"}`)},
		{name: "implicit root", schema: rawjson.Message(`{"properties":{}}`)},
		{name: "trailing document", schema: rawjson.Message(`{"type":"object"} {}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AdvertisedToolInputFromSchema(tt.schema)
			require.Error(t, err)
		})
	}
}

func TestCallerAuthoredToolPayloadRejectsTrailingDocument(t *testing.T) {
	input := mustAdvertisedToolInput(rawjson.Message(`{"type":"object"}`))
	err := input.validate(rawjson.Message(`{} {}`))
	require.ErrorContains(t, err, "multiple JSON values")
}

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

func TestRequestContractPreservesMissingProviderUsageModel(t *testing.T) {
	request := &Request{Model: "requested-model", ModelClass: ModelClassSmall}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Usage = TokenUsage{TotalTokens: 3}

	validated, err := contract.ValidateResponse(response)

	require.NoError(t, err)
	require.Empty(t, validated.Usage.Model)
	require.Equal(t, ModelClassSmall, validated.Usage.ModelClass)
	require.Empty(t, response.Usage.Model)
}

func TestRequestContractRejectsMalformedProviderUsageModel(t *testing.T) {
	contract, err := NewRequestContract(&Request{ModelClass: ModelClassSmall})
	require.NoError(t, err)
	response := canonicalTextResponse()
	response.Usage = TokenUsage{Model: string([]byte{0xff}), TotalTokens: 3}

	_, err = contract.ValidateResponse(response)

	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, OutputValidationUsage, validationErr.Kind())
	require.ErrorContains(t, outputValidationCause(t, err), "not valid UTF-8")
	rejected := contract.RejectProviderOutput(
		OutputValidationUsage,
		&response.Usage,
		errors.New("translation failed"),
	)
	require.Empty(t, rejected.Usage().Model)
	require.Equal(t, 3, rejected.Usage().TotalTokens)
	require.Equal(t, ModelClassSmall, rejected.Usage().ModelClass)
}

func TestRequestContractDistinguishesResponseShapeFromOutputBounds(t *testing.T) {
	tests := []struct {
		name     string
		response *Response
		kind     OutputValidationKind
	}{
		{
			name: "unsupported assistant part",
			response: &Response{Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{ImagePart{}},
			}}},
			kind: OutputValidationResponseShape,
		},
		{
			name: "invalid UTF-8",
			response: &Response{Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{TextPart{
					Text: string([]byte{0xff}),
				}},
			}}},
			kind: OutputValidationResponseShape,
		},
		{
			name: "byte limit",
			response: &Response{Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{TextPart{
					Text: strings.Repeat("x", maxDynamicValueBytes+1),
				}},
			}}},
			kind: OutputValidationOutputBounds,
		},
	}
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := contract.ValidateResponse(test.response)

			requireOutputValidationKind(t, err, test.kind)
		})
	}
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
	require.Equal(t, OutputValidationResponseShape, validationErr.Kind())
	require.True(t, validationErr.Evidence().Present)
	require.NotEmpty(t, validationErr.Evidence().SHA256)
	require.EqualError(t, validationErr, "model output does not meet its request contract")
	require.ErrorContains(t, errors.Unwrap(validationErr), "stop reason")
	rejected.Content[0].Parts[0] = TextPart{Text: "provider-mutated"}

	first, err := validationErr.RejectedResponse()
	require.NoError(t, err)
	first.Content[0].Parts[0] = TextPart{Text: "mutated"}
	second, err := validationErr.RejectedResponse()
	require.NoError(t, err)
	require.Equal(t, "ok", second.Content[0].Parts[0].(TextPart).Text)
}

func TestRequestContractRejectsContradictoryNoArgumentTool(t *testing.T) {
	_, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:        "continue",
		Description: "Continue the operation.",
		Input: mustAdvertisedToolInput(rawjson.Message(
			`{"type":"object","properties":{"cursor":{"type":"string"}},"required":["cursor"]}`,
		)),
		NoArguments: true,
	}}})

	require.ErrorContains(t, err, `tool "continue" declares no arguments but its schema declares fields`)

	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:        "continue",
		Description: "Continue the operation.",
		Input:       mustAdvertisedToolInput(rawjson.Message(`{"type":"object","additionalProperties":false}`)),
		NoArguments: true,
	}}})
	require.NoError(t, err)
	_, err = contract.ValidateResponse(toolResponse("continue"))
	require.NoError(t, err)

	response := toolResponse("continue")
	response.Content[0].Parts[0] = ToolUsePart{
		Name:  "continue",
		Input: rawjson.Message(`{ }`),
		ID:    "call-1",
	}
	_, err = contract.ValidateResponse(response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, OutputValidationToolArguments, validationErr.Kind())
	require.ErrorContains(t, outputValidationCause(t, err), `payload is not the canonical empty object`)
}

// outputValidationCause requires the public model-output category before a
// test inspects the private validation rule that produced it.
func outputValidationCause(t *testing.T, err error) error {
	t.Helper()
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	return errors.Unwrap(validationErr)
}

func requireOutputValidationKind(t *testing.T, err error, want OutputValidationKind) {
	t.Helper()
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, want, validationErr.Kind())
}

func TestToolDefinitionAcceptsEmptyObject(t *testing.T) {
	tests := []struct {
		name   string
		schema rawjson.Message
		want   bool
	}{
		{
			name:   "no fields",
			schema: rawjson.Message(`{"type":"object","additionalProperties":false}`),
			want:   true,
		},
		{
			name: "optional field",
			schema: rawjson.Message(
				`{"type":"object","properties":{"cursor":{"type":"string"}},"additionalProperties":false}`,
			),
			want: true,
		},
		{
			name: "required field",
			schema: rawjson.Message(
				`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"],"additionalProperties":false}`,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &ToolDefinition{
				Name:  "lookup",
				Input: mustAdvertisedToolInput(test.schema),
			}

			require.Equal(t, test.want, definition.AcceptsEmptyObject())
		})
	}
}

func TestRequestContractNoArgumentToolUsesModelFacingContract(t *testing.T) {
	definition := ToolDefinitionFromSpec(tools.ToolSpec{
		Name:        "continue",
		Description: "Continue the operation.",
		Payload: tools.TypeSpec{
			Name:           "ContinuePayload",
			Schema:         rawjson.Message(`{"type":"object"}`),
			FieldJSONTypes: map[string]string{"$payload": "object"},
			Codec: tools.JSONCodec[any]{
				FromJSON: func([]byte) (any, error) {
					return nil, errors.New("injected cursor is required")
				},
			},
		},
	})
	definition.NoArguments = true

	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	_, err = contract.ValidateResponse(toolResponse("continue"))
	require.NoError(t, err)
}

func TestGeneratedToolValidationProducesSafeRecoveryCorrection(t *testing.T) {
	tests := []struct {
		name       string
		issue      *tools.FieldIssue
		want       string
		notContain []string
	}{
		{
			name: "wrong known field type",
			issue: &tools.FieldIssue{
				Field:            "query",
				Constraint:       "invalid_field_type",
				ExpectedJSONType: "string",
				ActualJSONType:   "number",
			},
			want:       `Field "query" must contain a JSON string.`,
			notContain: []string{"number", "secret-value"},
		},
		{
			name: "missing required field",
			issue: &tools.FieldIssue{
				Field:      "query",
				Constraint: "missing_field",
			},
			want: `Field "query" is required and must contain a JSON string.`,
		},
		{
			name: "invalid enum with generated legal values",
			issue: &tools.FieldIssue{
				Field:      "type",
				Constraint: "invalid_enum_value",
				Allowed:    []string{"report", "lookup"},
			},
			want:       `Field "type" must be one of ["lookup" "report"].`,
			notContain: []string{"secret-value"},
		},
		{
			name: "invalid enum without generated legal values",
			issue: &tools.FieldIssue{
				Field:      "type",
				Constraint: "invalid_enum_value",
			},
			want:       `Field "type" must use one of the enum values declared by the generated input schema.`,
			notContain: []string{"one of []", "secret-value"},
		},
		{
			name: "missing union discriminator",
			issue: &tools.FieldIssue{
				Field:      "type",
				Constraint: "missing_field",
				Allowed:    []string{"report", "lookup"},
			},
			want: `Field "type" is required and must be one of ["lookup" "report"].`,
		},
		{
			name: "unknown field remains private",
			issue: &tools.FieldIssue{
				Field:      "privateSecret",
				Constraint: "unknown_field",
				Allowed:    []string{"query", "type"},
			},
			want:       unknownFieldCorrection,
			notContain: []string{"privateSecret", "secret-value"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := generatedRejectingTool(test.issue)
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
			require.NoError(t, err)
			response := toolResponse("catalog.lookup")
			response.Content[0].Parts[0] = ToolUsePart{
				ID:    "call-1",
				Name:  "catalog.lookup",
				Input: rawjson.Message(`{"query":"secret-value"}`),
			}

			owned, err := contract.ValidateResponse(response)

			require.Nil(t, owned)
			var validationErr *OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			correction := validationErr.RecoveryCorrection()
			require.Contains(t, correction, test.want)
			require.Contains(t, correction, replacementInstruction)
			require.NotContains(t, err.Error(), "secret-value")
			var codecErr *tools.ValidationError
			require.NotErrorAs(t, err, &codecErr)
			rejected, cloneErr := validationErr.RejectedResponse()
			require.NoError(t, cloneErr)
			require.Nil(t, rejected)
			for _, value := range test.notContain {
				require.NotContains(t, correction, value)
			}
		})
	}
}

func TestGeneratedToolRecoveryCorrectionIsDeterministicAndBounded(t *testing.T) {
	fieldTypes := map[string]string{"$payload": "object"}
	issues := make([]*tools.FieldIssue, 0, 300)
	for index := range 300 {
		field := fmt.Sprintf("field_%03d", index)
		fieldTypes[field] = "string"
		issues = append(issues, &tools.FieldIssue{
			Field:      field,
			Constraint: "missing_field",
		})
	}
	reversed := slices.Clone(issues)
	slices.Reverse(reversed)

	first := generatedToolCallCorrection(
		"catalog.lookup",
		fieldTypes,
		tools.NewValidationError("rejected values", issues, nil),
	)
	second := generatedToolCallCorrection(
		"catalog.lookup",
		fieldTypes,
		tools.NewValidationError("different rejected values", reversed, nil),
	)

	require.Equal(t, first, second)
	require.LessOrEqual(t, len(first), correction.MaxBytes)
	require.NotContains(t, first, "rejected values")
	require.NotContains(t, first, "different rejected values")
}

func TestGeneratedToolRecoveryCorrectionUsesSchemaOwnedFieldPaths(t *testing.T) {
	tests := []struct {
		name       string
		fieldTypes map[string]string
		issueField string
		wantField  string
		notContain string
	}{
		{
			name:       "JSON Pointer",
			fieldTypes: map[string]string{"query.value": "string"},
			issueField: "/query/value",
			wantField:  `Field "query.value"`,
		},
		{
			name:       "dynamic map key",
			fieldTypes: map[string]string{"labels.*": "string"},
			issueField: "labels.submitted-secret",
			wantField:  `Field "labels.*"`,
			notContain: "submitted-secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correction := generatedToolCallCorrection(
				"catalog.lookup",
				test.fieldTypes,
				tools.NewValidationError("rejected", []*tools.FieldIssue{{
					Field:      test.issueField,
					Constraint: "invalid_field_type",
				}}, nil),
			)

			require.Contains(t, correction, test.wantField)
			if test.notContain != "" {
				require.NotContains(t, correction, test.notContain)
			}
		})
	}
}

func TestGeneratedToolStreamValidationProducesSafeRecoveryCorrection(t *testing.T) {
	definition := generatedRejectingTool(&tools.FieldIssue{
		Field:            "query",
		Constraint:       "invalid_field_type",
		ExpectedJSONType: "string",
		ActualJSONType:   "number",
	})
	call := ToolCall{
		ID:      "call-1",
		Name:    "catalog.lookup",
		Payload: rawjson.Message(`{"query":42}`),
	}
	stream := mustValidateStream(t, &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: call},
			StopChunk{Reason: "tool_use"},
		},
		response: responseWithToolCall(call),
	}, &Request{Tools: []*ToolDefinition{definition}})

	chunk, err := stream.Recv()

	require.Nil(t, chunk)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Contains(t, validationErr.RecoveryCorrection(), `Field "query" must contain a JSON string.`)
	rejected, cloneErr := validationErr.RejectedResponse()
	require.NoError(t, cloneErr)
	require.Nil(t, rejected)
}

func TestGeneratedToolValidationWithoutStructuredIssuesRemainsTerminal(t *testing.T) {
	definition := ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "catalog.lookup",
		Payload: tools.TypeSpec{
			Name:           "LookupPayload",
			Schema:         rawjson.Message(`{"type":"object"}`),
			FieldJSONTypes: map[string]string{"$payload": "object", "query": "string"},
			Codec: tools.JSONCodec[any]{
				FromJSON: func([]byte) (any, error) {
					return nil, errors.New("unstructured validation failure")
				},
			},
		},
	})
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)

	_, err = contract.ValidateResponse(toolResponse("catalog.lookup"))

	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Empty(t, validationErr.RecoveryCorrection())

	restored, restoreErr := RestoreOutputValidationError(
		validationErr.Kind(),
		errors.Unwrap(validationErr),
		validationErr.Evidence(),
		validationErr.Usage(),
	)
	require.NoError(t, restoreErr)
	require.Empty(t, restored.RecoveryCorrection())
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
			require.Equal(t, OutputValidationToolChoice, validationErr.Kind())
			require.ErrorContains(t, outputValidationCause(t, err), test.wantErr)
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
	require.ErrorContains(t, outputValidationCause(t, err), "tool choice none")
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
	require.ErrorContains(t, outputValidationCause(t, err), "tool choice any")
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
		StructuredOutput: &StructuredOutput{
			Name:   "answer",
			Schema: rawjson.Message(`{"type":"object"}`),
		},
	})

	require.NoError(t, err)
	require.NotNil(t, contract)
}

func TestNewRequestContractRejectsStructuredOutputWithoutSchema(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		StructuredOutput: &StructuredOutput{Name: "answer"},
	})

	require.Nil(t, contract)
	require.EqualError(t, err, "model request structured output schema is required")
}

func TestNewRequestContractRejectsStructuredOutputWithoutName(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		StructuredOutput: &StructuredOutput{Schema: rawjson.Message(`{"type":"object"}`)},
	})

	require.Nil(t, contract)
	require.EqualError(t, err, "model request structured output name is required")
}

func TestNewRequestContractRejectsMalformedRequestValues(t *testing.T) {
	tests := []struct {
		name    string
		request *Request
		wantErr string
	}{
		{
			name:    "non-finite temperature",
			request: &Request{Temperature: float32(math.NaN())},
			wantErr: "temperature must be finite",
		},
		{
			name: "unknown message role",
			request: &Request{Messages: []*Message{{
				Role:  ConversationRole("operator"),
				Parts: []Part{TextPart{Text: "hello"}},
			}}},
			wantErr: "unsupported role",
		},
		{
			name: "empty message",
			request: &Request{Messages: []*Message{{
				Role: ConversationRoleUser,
			}}},
			wantErr: "message has no parts",
		},
		{
			name: "malformed tool input",
			request: &Request{Messages: []*Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "lookup",
					Input: rawjson.Message(`{"broken"`),
				}},
			}}},
			wantErr: "input to be a JSON object",
		},
		{
			name: "malformed structured output schema",
			request: &Request{StructuredOutput: &StructuredOutput{
				Name:   "answer",
				Schema: rawjson.Message(`{"broken"`),
			}},
			wantErr: "schema is not valid JSON",
		},
		{
			name: "malformed structured output example",
			request: &Request{StructuredOutput: &StructuredOutput{
				Name:        "answer",
				Schema:      rawjson.Message(`{"type":"object"}`),
				ExampleJSON: rawjson.Message(`{"broken"`),
			}},
			wantErr: "example is not valid JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRequestContract(test.request)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestNewRequestContractRejectsToolChoiceForStructuredOutput(t *testing.T) {
	for _, mode := range []ToolChoiceMode{
		ToolChoiceModeAuto,
		ToolChoiceModeNone,
		ToolChoiceModeAny,
		ToolChoiceModeTool,
	} {
		t.Run(string(mode), func(t *testing.T) {
			choice := &ToolChoice{Mode: mode}
			if mode == ToolChoiceModeTool {
				choice.Name = "lookup"
			}
			request := &Request{
				StructuredOutput: &StructuredOutput{
					Name:   "answer",
					Schema: rawjson.Message(`{"type":"object"}`),
				},
				ToolChoice: choice,
			}
			if mode == ToolChoiceModeAny || mode == ToolChoiceModeTool {
				request.Tools = []*ToolDefinition{advertisedTool("lookup")}
			}
			_, err := NewRequestContract(request)
			require.ErrorContains(t, err, "structured output cannot")
		})
	}
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
	require.Equal(t, OutputValidationStructuredOutput, validationErr.Kind())
	require.ErrorContains(t, outputValidationCause(t, err), "decode candidate JSON")
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
	require.ErrorContains(t, outputValidationCause(t, err), "generated decoder rejected payload")
}

func TestCallerAuthoredToolSchemaRejectsInvalidPayload(t *testing.T) {
	request := &Request{Tools: []*ToolDefinition{{
		Name: "lookup",
		Input: mustAdvertisedToolInput(rawjson.Message(
			`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`,
		)),
	}}}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	response := toolResponse("lookup")
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-1",
		Name:  "lookup",
		Input: rawjson.Message(`{"unexpected":true}`),
	}

	validated, err := contract.ValidateResponse(response)

	require.Nil(t, validated)
	require.ErrorContains(t, outputValidationCause(t, err), `model tool "lookup" payload failed its request contract`)
	require.ErrorContains(t, outputValidationCause(t, err), "validate JSON Schema")
}

func TestRequestContractValidatesTransportedToolPayload(t *testing.T) {
	input, err := ToolInputFromContract("lookup", ToolInputContract{
		Schema: rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
	})
	require.NoError(t, err)

	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{{
		Name:  "lookup",
		Input: input,
	}}})

	require.NoError(t, err)
	response := toolResponse("lookup")
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-1",
		Name:  "lookup",
		Input: rawjson.Message(`{"unexpected":true}`),
	}
	validated, err := contract.ValidateResponse(response)
	require.Nil(t, validated)
	require.ErrorContains(t, outputValidationCause(t, err), `model tool "lookup" payload failed its request contract`)
}

func TestNewRequestContractPreflightsToolCount(t *testing.T) {
	_, err := NewRequestContract(&Request{
		Tools: make([]*ToolDefinition, maxDynamicValueVisits+1),
	})

	require.ErrorContains(t, err, "model request tools")
	require.ErrorContains(t, err, "exceeds maximum visited values")
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
		Input: mustAdvertisedToolInput(rawjson.Message(`{"type":"object"}`)),
	}
}

func generatedRejectingTool(issue *tools.FieldIssue) *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{
		Name:        "catalog.lookup",
		Description: "Looks up one synthetic record.",
		Payload: tools.TypeSpec{
			Name:   "LookupPayload",
			Schema: rawjson.Message(`{"type":"object"}`),
			FieldJSONTypes: map[string]string{
				"$payload": "object",
				"query":    "string",
				"type":     "string",
			},
			Codec: tools.JSONCodec[any]{
				FromJSON: func([]byte) (any, error) {
					return nil, tools.NewValidationError(
						"rejected provider value secret-value",
						[]*tools.FieldIssue{issue},
						nil,
					)
				},
			},
		},
	})
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
