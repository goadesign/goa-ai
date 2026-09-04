package model

// This file verifies that request-owned output rules remain immutable across
// provider calls. Providers may report the concrete model they selected, but
// they cannot replace the caller's logical model class.

import (
	"encoding/json"
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

func TestRequestContractRequiresExplicitMalformedToolArgumentsMarker(t *testing.T) {
	contract, err := NewRequestContract(&Request{ModelClass: ModelClassDefault})
	require.NoError(t, err)
	usage := TokenUsage{
		ModelClass:   ModelClassDefault,
		InputTokens:  8,
		OutputTokens: 5,
		TotalTokens:  13,
	}
	privateCause := errors.New(`provider returned {"secret":`)

	terminal := contract.RejectProviderOutput(
		OutputValidationToolArguments,
		&usage,
		privateCause,
	)
	require.Empty(t, terminal.RecoveryCorrection())
	require.ErrorIs(t, outputValidationCause(t, terminal), privateCause)

	rejected := contract.RejectProviderOutput(
		OutputValidationToolArguments,
		&usage,
		NewMalformedToolArgumentsError(privateCause),
	)

	require.Equal(t, OutputValidationToolArguments, rejected.Kind())
	require.Equal(t, malformedToolArgumentsCorrection, rejected.RecoveryCorrection())
	require.Equal(t, usage, *rejected.Usage())
	require.True(t, rejected.Evidence().Present)
	require.NotContains(t, rejected.Error(), "secret")
	require.ErrorIs(t, outputValidationCause(t, rejected), privateCause)
	response, cloneErr := rejected.RejectedResponse()
	require.NoError(t, cloneErr)
	require.Nil(t, response)
}

func TestRecoveryCorrectionRequiresEveryErrorLeafToAgree(t *testing.T) {
	malformed := NewMalformedToolArgumentsError(errors.New("private malformed payload"))
	wrapped := fmt.Errorf("provider translation: %w", malformed)
	require.Equal(t, malformedToolArgumentsCorrection, recoveryCorrectionFromError(wrapped))
	require.Equal(t, malformedToolArgumentsCorrection, recoveryCorrectionFromError(errors.Join(
		malformed,
		NewMalformedToolArgumentsError(errors.New("second private malformed payload")),
	)))
	require.Empty(t, recoveryCorrectionFromError(errors.Join(
		malformed,
		errors.New("provider cleanup failed"),
	)))
	require.Empty(t, recoveryCorrectionFromError(errors.Join(
		malformed,
		&toolCallValidationError{correction: advertisedToolInputCorrection},
	)))
}

func TestNewMalformedToolArgumentsErrorKeepsCausePrivate(t *testing.T) {
	privateCause := errors.New(`provider returned {"secret":`)
	err := NewMalformedToolArgumentsError(privateCause)

	require.EqualError(t, err, "model tool arguments are not valid JSON")
	require.NotContains(t, err.Error(), "secret")
	require.ErrorIs(t, err, privateCause)
	require.PanicsWithValue(t, "model: malformed tool arguments require a cause", func() {
		require.NoError(t, NewMalformedToolArgumentsError(nil))
	})
}

func TestRequestContractCorrectsMalformedCanonicalToolArguments(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	response := toolResponse("lookup")
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-1",
		Name:  "lookup",
		Input: rawjson.Message(`{"query":`),
	}

	_, err = contract.ValidateResponse(response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, OutputValidationToolArguments, validationErr.Kind())
	require.Equal(t, malformedToolArgumentsCorrection, validationErr.RecoveryCorrection())
}

func TestRequestContractClassifiesUnadvertisedNonObjectArgumentsAsToolIdentity(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	response := toolResponse("lookup")
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-1",
		Name:  "lookup",
		Input: rawjson.Message(`[]`),
	}

	_, err = contract.ValidateResponse(response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, OutputValidationToolIdentity, validationErr.Kind())
	require.Empty(t, validationErr.RecoveryCorrection())
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
			Name:   "ContinuePayload",
			Schema: rawjson.Message(`{"type":"object"}`),
			Fields: []tools.FieldMetadata{{
				JSONType: "object",
			}},
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
	definition := generatedRejectingTool(&tools.FieldIssue{
		Field:      "privateSecret",
		Constraint: "unknown_field",
		Allowed:    []string{"query"},
	})
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
	require.Equal(t, advertisedToolInputCorrection, correction)
	require.NotContains(t, correction, "privateSecret")
	require.NotContains(t, correction, "query")
	require.NotContains(t, correction, "secret-value")
	var codecErr *tools.ValidationError
	require.NotErrorAs(t, err, &codecErr)
	rejected, cloneErr := validationErr.RejectedResponse()
	require.NoError(t, cloneErr)
	require.Nil(t, rejected)
}

func TestGeneratedToolSchemaRejectionsProduceActionableCorrections(t *testing.T) {
	tests := []struct {
		name         string
		schema       string
		input        string
		fieldTypes   map[string]string
		descriptions map[string]string
		want         string
	}{
		{
			name:   "required nested field",
			schema: `{"type":"object","properties":{"profile":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}},"required":["profile"],"additionalProperties":false}`,
			input:  `{"profile":{}}`,
			fieldTypes: map[string]string{
				"$payload":     "object",
				"profile":      "object",
				"profile.name": "string",
			},
			descriptions: map[string]string{"profile.name": "Stable display name"},
			want:         `Field "profile.name" is required. Field description: "Stable display name". Return a replacement tool call with valid arguments.`,
		},
		{
			name:   "array item type",
			schema: `{"type":"object","properties":{"steps":{"type":"array","items":{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"],"additionalProperties":false}}},"required":["steps"],"additionalProperties":false}`,
			input:  `{"steps":[{"amount":"private-submitted-value"}]}`,
			fieldTypes: map[string]string{
				"$payload":       "object",
				"steps":          "array",
				"steps.*":        "object",
				"steps.*.amount": "number",
			},
			descriptions: map[string]string{"steps.*.amount": "Amount for this step"},
			want:         `Field "steps.*.amount" must contain a JSON number. Field description: "Amount for this step". Return a replacement tool call with valid arguments.`,
		},
		{
			name:   "map value type",
			schema: `{"type":"object","properties":{"scores":{"type":"object","additionalProperties":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}}},"required":["scores"],"additionalProperties":false}`,
			input:  `{"scores":{"private-map-key":{"value":"private-submitted-value"}}}`,
			fieldTypes: map[string]string{
				"$payload":       "object",
				"scores":         "object",
				"scores.*":       "object",
				"scores.*.value": "integer",
			},
			want: `Field "scores.*.value" must contain a JSON integer. Return a replacement tool call with valid arguments.`,
		},
		{
			name:   "enum",
			schema: `{"type":"object","properties":{"mode":{"type":"string","enum":["daily","weekly"]}},"required":["mode"],"additionalProperties":false}`,
			input:  `{"mode":"private-submitted-value"}`,
			fieldTypes: map[string]string{
				"$payload": "object",
				"mode":     "string",
			},
			descriptions: map[string]string{"mode": "Report interval"},
			want:         `Field "mode" must contain one of these JSON values: ["daily","weekly"]. Field description: "Report interval". Return a replacement tool call with valid arguments.`,
		},
		{
			name:   "unknown field",
			schema: `{"type":"object","properties":{"profile":{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}},"additionalProperties":false}`,
			input:  `{"profile":{"private-undeclared-field":true}}`,
			fieldTypes: map[string]string{
				"$payload":     "object",
				"profile":      "object",
				"profile.name": "string",
			},
			descriptions: map[string]string{"profile": "Profile settings"},
			want:         `Field "profile" contains an undeclared field. Field description: "Profile settings". Return a replacement tool call with valid arguments.`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := generatedSchemaTool(
				test.schema,
				test.fieldTypes,
				test.descriptions,
			)
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
			require.NoError(t, err)
			response := toolResponse(definition.Name)
			response.Content[0].Parts[0] = ToolUsePart{
				ID:    "private-call-id",
				Name:  definition.Name,
				Input: rawjson.Message(test.input),
			}

			validated, err := contract.ValidateResponse(response)

			require.Nil(t, validated)
			var validationErr *OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Equal(t, test.want, validationErr.RecoveryCorrection())
			require.NotContains(t, validationErr.RecoveryCorrection(), "private-")
			require.LessOrEqual(t, len(validationErr.RecoveryCorrection()), correction.MaxBytes)
		})
	}
}

func TestGeneratedToolSchemaCorrectionStaysGenericWhenCauseIsAmbiguous(t *testing.T) {
	definition := generatedSchemaTool(
		`{"type":"object","properties":{"left":{"type":"string"},"right":{"type":"string"}},"required":["left","right"],"additionalProperties":false}`,
		map[string]string{
			"$payload": "object",
			"left":     "string",
			"right":    "string",
		},
		nil,
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call-id",
		Name:  definition.Name,
		Input: rawjson.Message(`{}`),
	}

	validated, err := contract.ValidateResponse(response)

	require.Nil(t, validated)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
	require.NotContains(t, validationErr.RecoveryCorrection(), "private-")
}

func TestToolSchemaCorrectionDistinguishesFixedAndDynamicPathSegments(t *testing.T) {
	fields := []tools.FieldMetadata{
		{JSONType: "object"},
		{Path: []tools.FieldPathSegment{tools.FixedField("a.b")}, JSONType: "string", Description: "Literal dotted name"},
		{Path: []tools.FieldPathSegment{tools.FixedField("a"), tools.FixedField("b")}, JSONType: "number", Description: "Nested name"},
		{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "object"},
		{Path: []tools.FieldPathSegment{tools.FixedField("items"), tools.FixedField("*")}, JSONType: "string", Description: "Literal star name"},
		{Path: []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}}, JSONType: "number", Description: "Dynamic item"},
	}
	definition := schemaToolWithFields(
		`{"type":"object","properties":{"a.b":{"type":"string"},"a":{"type":"object","properties":{"b":{"type":"number"}},"required":["b"],"additionalProperties":false},"items":{"type":"object","properties":{"*":{"type":"string"}},"additionalProperties":{"type":"number"}}},"required":["a.b","a","items"],"additionalProperties":false}`,
		fields,
	)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "literal dotted property", input: `{"a.b":7,"a":{"b":1},"items":{"*":"ok"}}`, want: `Field "[\"a.b\"]" must contain a JSON string. Field description: "Literal dotted name".`},
		{name: "nested property", input: `{"a.b":"ok","a":{"b":"bad"},"items":{"*":"ok"}}`, want: `Field "a.b" must contain a JSON number. Field description: "Nested name".`},
		{name: "literal star property", input: `{"a.b":"ok","a":{"b":1},"items":{"*":7}}`, want: `Field "items[\"*\"]" must contain a JSON string. Field description: "Literal star name".`},
		{name: "dynamic map key", input: `{"a.b":"ok","a":{"b":1},"items":{"private-key":"bad","*":"ok"}}`, want: `Field "items.*" must contain a JSON number. Field description: "Dynamic item".`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
			require.NoError(t, err)
			response := toolResponse(definition.Name)
			response.Content[0].Parts[0] = ToolUsePart{ID: "private-call", Name: definition.Name, Input: rawjson.Message(test.input)}

			_, err = contract.ValidateResponse(response)
			var validationErr *OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Contains(t, validationErr.RecoveryCorrection(), test.want)
			require.NotContains(t, validationErr.RecoveryCorrection(), "private-")
		})
	}
}

func TestToolSchemaCorrectionUsesSelectedUnionBranchInsideArray(t *testing.T) {
	fields := []tools.FieldMetadata{
		{JSONType: "object"},
		{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "array"},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("items"),
				tools.DynamicField{},
				tools.FixedField("type"),
			},
			JSONType:            "string",
			DiscriminatorValues: []string{"email", "count"},
		},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("items"),
				tools.DynamicField{},
				tools.FixedField("value"),
				tools.FixedField("address"),
			},
			JSONType:    "string",
			Description: "Email address",
			Branches: []tools.UnionBranch{{
				Discriminator: []tools.FieldPathSegment{
					tools.FixedField("items"),
					tools.DynamicField{},
					tools.FixedField("type"),
				},
				Value: "email",
			}},
		},
		{
			Path: []tools.FieldPathSegment{
				tools.FixedField("items"),
				tools.DynamicField{},
				tools.FixedField("value"),
				tools.FixedField("amount"),
			},
			JSONType:    "integer",
			Description: "Private count branch",
			Branches: []tools.UnionBranch{{
				Discriminator: []tools.FieldPathSegment{
					tools.FixedField("items"),
					tools.DynamicField{},
					tools.FixedField("type"),
				},
				Value: "count",
			}},
		},
	}
	definition := schemaToolWithFields(
		`{"type":"object","properties":{"items":{"type":"array","items":{"oneOf":[{"type":"object","properties":{"type":{"const":"email"},"value":{"type":"object","properties":{"address":{"type":"string"}},"required":["address"],"additionalProperties":false}},"required":["type","value"],"additionalProperties":false},{"type":"object","properties":{"type":{"const":"count"},"value":{"type":"object","properties":{"amount":{"type":"integer"}},"required":["amount"],"additionalProperties":false}},"required":["type","value"],"additionalProperties":false}]}}},"required":["items"],"additionalProperties":false}`,
		fields,
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call",
		Name:  definition.Name,
		Input: rawjson.Message(`{"items":[{"type":"email","value":{"address":7}}]}`),
	}

	_, err = contract.ValidateResponse(response)

	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(
		t,
		`Field "items.*.value.address" must contain a JSON string. Field description: "Email address". Return a replacement tool call with valid arguments.`,
		validationErr.RecoveryCorrection(),
	)
	require.NotContains(t, validationErr.RecoveryCorrection(), "0")
	require.NotContains(t, validationErr.RecoveryCorrection(), "Private count branch")
}

func TestToolSchemaCorrectionNamesRequiredFieldWithoutFixedJSONType(t *testing.T) {
	definition := schemaToolWithFields(
		`{"type":"object","properties":{"context":{}},"required":["context"],"additionalProperties":false}`,
		[]tools.FieldMetadata{
			{JSONType: "object"},
			{
				Path:        []tools.FieldPathSegment{tools.FixedField("context")},
				Description: "Required caller context",
			},
		},
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call",
		Name:  definition.Name,
		Input: rawjson.Message(`{}`),
	}

	_, err = contract.ValidateResponse(response)

	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(
		t,
		`Field "context" is required. Field description: "Required caller context". Return a replacement tool call with valid arguments.`,
		validationErr.RecoveryCorrection(),
	)
}

func TestToolSchemaCorrectionIncludesUnsupportedFailuresInAmbiguity(t *testing.T) {
	definition := schemaToolWithFields(
		`{"type":"object","properties":{"label":{"type":"string","minLength":3},"count":{"type":"integer"}},"required":["label","count"],"additionalProperties":false}`,
		[]tools.FieldMetadata{
			{JSONType: "object"},
			{Path: []tools.FieldPathSegment{tools.FixedField("label")}, JSONType: "string", Description: "Label"},
			{Path: []tools.FieldPathSegment{tools.FixedField("count")}, JSONType: "integer", Description: "Count"},
		},
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call",
		Name:  definition.Name,
		Input: rawjson.Message(`{"label":"x","count":"bad"}`),
	}

	_, err = contract.ValidateResponse(response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
}

func TestToolSchemaCorrectionIncludesUnmappedFailuresInAmbiguity(t *testing.T) {
	definition := schemaToolWithFields(
		`{"type":"object","properties":{"known":{"type":"string"},"unmapped":{"type":"integer"}},"required":["known","unmapped"],"additionalProperties":false}`,
		[]tools.FieldMetadata{
			{JSONType: "object"},
			{
				Path:        []tools.FieldPathSegment{tools.FixedField("known")},
				JSONType:    "string",
				Description: "Known field",
			},
		},
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call",
		Name:  definition.Name,
		Input: rawjson.Message(`{"known":7,"unmapped":"bad"}`),
	}

	_, err = contract.ValidateResponse(response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
}

func TestGeneratedToolSchemaCorrectionSnapshotsGeneratedMetadata(t *testing.T) {
	fieldTypes := map[string]string{
		"$payload": "object",
		"query":    "string",
	}
	descriptions := map[string]string{"query": "Original query"}
	definition := generatedSchemaTool(
		`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`,
		fieldTypes,
		descriptions,
	)
	fieldTypes["query"] = "number"
	descriptions["query"] = "Changed source description"
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	definition.Input.fields[1].JSONType = "boolean"
	definition.Input.fields[1].Description = "Changed request description"
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call-id",
		Name:  definition.Name,
		Input: rawjson.Message(`{"query":42}`),
	}

	validated, err := contract.ValidateResponse(response)

	require.Nil(t, validated)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(
		t,
		`Field "query" must contain a JSON string. Field description: "Original query". Return a replacement tool call with valid arguments.`,
		validationErr.RecoveryCorrection(),
	)
}

func TestGeneratedToolSchemaCorrectionKeepsSharedSizeLimit(t *testing.T) {
	definition := generatedSchemaTool(
		`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`,
		map[string]string{"$payload": "object", "query": "string"},
		map[string]string{"query": strings.Repeat("description", correction.MaxBytes)},
	)
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call-id",
		Name:  definition.Name,
		Input: rawjson.Message(`{"query":42}`),
	}

	validated, err := contract.ValidateResponse(response)

	require.Nil(t, validated)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
}

func TestCallerAuthoredToolMetadataProducesActionableCorrection(t *testing.T) {
	definition := ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "private.external_tool",
		Payload: tools.TypeSpec{
			Name:   "ExternalPayload",
			Schema: rawjson.Message(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"}},"required":["query"],"additionalProperties":false}`),
			Fields: []tools.FieldMetadata{{
				Path:        []tools.FieldPathSegment{tools.FixedField("query")},
				JSONType:    "string",
				Description: "Search query",
			}},
			Codec: tools.JSONCodec[any]{FromJSON: func([]byte) (any, error) {
				panic("schema-invalid input reached the codec")
			}},
		},
	})
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	response := toolResponse(definition.Name)
	response.Content[0].Parts[0] = ToolUsePart{
		ID:    "private-call-id",
		Name:  definition.Name,
		Input: rawjson.Message(`{"query":42}`),
	}

	validated, err := contract.ValidateResponse(response)

	require.Nil(t, validated)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, `Field "query" must contain a JSON string. Field description: "Search query". Return a replacement tool call with valid arguments.`, validationErr.RecoveryCorrection())
	require.NotContains(t, validationErr.RecoveryCorrection(), "private-")
}

func TestToolInputContractRejectionRequiresEveryErrorLeaf(t *testing.T) {
	validation := tools.NewValidationError(
		"generated rejection",
		[]*tools.FieldIssue{{Field: "query", Constraint: "missing_field"}},
		nil,
	)
	advertised := &advertisedInputValidationError{cause: errors.New("schema rejection")}

	require.True(t, isToolInputContractRejection(fmt.Errorf("decode payload: %w", validation)))
	require.True(t, isToolInputContractRejection(errors.Join(validation, advertised)))
	require.False(t, isToolInputContractRejection(errors.Join(
		validation,
		errors.New("codec implementation failed"),
	)))
	var nilValidation *tools.ValidationError
	var nilAdvertised *advertisedInputValidationError
	require.False(t, isToolInputContractRejection(nilValidation))
	require.False(t, isToolInputContractRejection(nilAdvertised))
}

func TestToolInputCorrectionDoesNotIncludeToolNames(t *testing.T) {
	for _, name := range []string{"lookup", strings.Repeat("private-tool-", 10)} {
		definition := generatedRejectingTool(&tools.FieldIssue{
			Field:      "privateSecret",
			Constraint: "invalid_field_type",
		})
		definition.Name = name
		contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
		require.NoError(t, err)
		_, err = contract.ValidateResponse(toolResponse(name))
		var validationErr *OutputValidationError
		require.ErrorAs(t, err, &validationErr)
		require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
		require.NotContains(t, validationErr.RecoveryCorrection(), name)
	}
	require.LessOrEqual(t, len(advertisedToolInputCorrection), correction.MaxBytes)
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
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
	rejected, cloneErr := validationErr.RejectedResponse()
	require.NoError(t, cloneErr)
	require.Nil(t, rejected)
}

func TestToolCodecFailureWithoutInputRejectionRemainsTerminal(t *testing.T) {
	definition := ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "catalog.lookup",
		Payload: tools.TypeSpec{
			Name:   "LookupPayload",
			Schema: rawjson.Message(`{"type":"object"}`),
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

func TestToolDefinitionFromSpecEnforcesAdvertisedSchemaBeforeCodec(t *testing.T) {
	type payload struct {
		Query string `json:"q"`
	}
	codecCalls := 0
	definition := ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "catalog.lookup",
		Payload: tools.TypeSpec{
			Name: "LookupPayload",
			Schema: rawjson.Message(
				`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}`,
			),
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					codecCalls++
					var decoded payload
					if err := json.Unmarshal(data, &decoded); err != nil {
						return nil, err
					}
					if decoded.Query == "codec-error" {
						return nil, errors.New("codec failed after schema validation")
					}
					return decoded, nil
				},
			},
		},
	})
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)

	for _, input := range []rawjson.Message{
		rawjson.Message(`{}`),
		rawjson.Message(`{"q":"valid","extra":true}`),
		rawjson.Message(`[]`),
		rawjson.Message(`null`),
		rawjson.Message(`"query"`),
	} {
		response := toolResponse("catalog.lookup")
		response.Content[0].Parts[0] = ToolUsePart{
			ID:    "call-1",
			Name:  "catalog.lookup",
			Input: input,
		}
		_, err := contract.ValidateResponse(response)
		var validationErr *OutputValidationError
		require.ErrorAs(t, err, &validationErr)
		require.NotEmpty(t, validationErr.RecoveryCorrection())
	}
	require.Zero(t, codecCalls)

	valid := toolResponse("catalog.lookup")
	valid.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-2",
		Name:  "catalog.lookup",
		Input: rawjson.Message(`{"q":"valid"}`),
	}
	_, err = contract.ValidateResponse(valid)
	require.NoError(t, err)
	require.Equal(t, 1, codecCalls)

	codecFailure := toolResponse("catalog.lookup")
	codecFailure.Content[0].Parts[0] = ToolUsePart{
		ID:    "call-3",
		Name:  "catalog.lookup",
		Input: rawjson.Message(`{"q":"codec-error"}`),
	}
	_, err = contract.ValidateResponse(codecFailure)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Empty(t, validationErr.RecoveryCorrection())
	require.Equal(t, 2, codecCalls)
}

func TestNewToolDefinitionFromSpecRejectsExampleOutsideCodec(t *testing.T) {
	_, err := NewToolDefinitionFromSpec(tools.ToolSpec{
		Name: "catalog.lookup",
		Payload: tools.TypeSpec{
			Name:                     "LookupPayload",
			Schema:                   rawjson.Message(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}`),
			SchemaWithoutRootExample: rawjson.Message(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}`),
			ExampleJSON:              rawjson.Message(`{"q":"rejected"}`),
			Codec: tools.JSONCodec[any]{
				FromJSON: func([]byte) (any, error) {
					return nil, errors.New("codec rejected example")
				},
			},
		},
	})
	require.ErrorContains(t, err, "example JSON does not satisfy its payload contract")
	require.ErrorContains(t, err, "codec rejected example")
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
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
	require.NotContains(t, validationErr.RecoveryCorrection(), "unexpected")
	require.NotContains(t, validationErr.RecoveryCorrection(), "validate JSON Schema")
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
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, advertisedToolInputCorrection, validationErr.RecoveryCorrection())
	require.NotContains(t, validationErr.RecoveryCorrection(), "unexpected")
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
			Fields: []tools.FieldMetadata{
				{JSONType: "object"},
				{Path: []tools.FieldPathSegment{tools.FixedField("query")}, JSONType: "string"},
				{Path: []tools.FieldPathSegment{tools.FixedField("type")}, JSONType: "string"},
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

func generatedSchemaTool(
	schema string,
	fieldTypes, descriptions map[string]string,
) *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "private.generated_tool",
		Payload: tools.TypeSpec{
			Name:   "GeneratedPayload",
			Schema: rawjson.Message(schema),
			Fields: testFieldMetadata(fieldTypes, descriptions),
			Codec: tools.JSONCodec[any]{
				FromJSON: func([]byte) (any, error) {
					panic("schema-invalid input reached the generated codec")
				},
			},
		},
	})
}

func schemaToolWithFields(schema string, fields []tools.FieldMetadata) *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{
		Name: "private.schema_tool",
		Payload: tools.TypeSpec{
			Name:   "SchemaPayload",
			Schema: rawjson.Message(schema),
			Fields: fields,
			Codec: tools.JSONCodec[any]{FromJSON: func([]byte) (any, error) {
				panic("schema-invalid input reached the codec")
			}},
		},
	})
}

func testFieldMetadata(fieldTypes, descriptions map[string]string) []tools.FieldMetadata {
	fields := make([]tools.FieldMetadata, 0, len(fieldTypes))
	for path, jsonType := range fieldTypes {
		var segments []tools.FieldPathSegment
		if path != "$payload" {
			for _, segment := range strings.Split(path, ".") {
				if segment == "*" {
					segments = append(segments, tools.DynamicField{})
					continue
				}
				segments = append(segments, tools.FixedField(segment))
			}
		}
		fields = append(fields, tools.FieldMetadata{
			Path:        segments,
			JSONType:    jsonType,
			Description: descriptions[path],
		})
	}
	slices.SortFunc(fields, func(a, b tools.FieldMetadata) int {
		return strings.Compare(tools.FieldPathString(a.Path), tools.FieldPathString(b.Path))
	})
	return fields
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
