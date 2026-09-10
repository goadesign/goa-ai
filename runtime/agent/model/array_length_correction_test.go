// Array correction tests validate real schema failures through unary and stream
// contracts. Only feedback changes: accepted bytes and rejected errors survive.
package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestArrayLengthCorrectionInclusiveBounds(t *testing.T) {
	for _, test := range []struct {
		name, rule, payload, want string
	}{
		{"below minimum", `"minItems":2`, `{"items":[1]}`, `Field "items" must contain at least 2 items.`},
		{"at minimum", `"minItems":2`, `{ "items" : [1, 2] }`, ""},
		{"above minimum", `"minItems":2`, `{"items":[1,2,3]}`, ""},
		{"below maximum", `"maxItems":2`, `{"items":[1]}`, ""},
		{"at maximum", `"maxItems":2`, `{ "items" : [1, 2] }`, ""},
		{"above maximum", `"maxItems":2`, `{"items":[1,2,3]}`, `Field "items" must contain at most 2 items.`},
		{"empty optional minimum zero", `"minItems":0`, `{"items":[]}`, ""},
		{"absent optional maximum zero", `"maxItems":0`, `{}`, ""},
		{"empty optional maximum zero", `"maxItems":0`, `{"items":[]}`, ""},
		{"above maximum zero", `"maxItems":0`, `{"items":[1]}`, `Field "items" must contain at most 0 items.`},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"integer"},%s}},"additionalProperties":false}`, test.rule)
			definition := arrayCorrectionDefinition(schema, []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "array"}})
			rejected := checkArrayCorrection(t, definition, test.payload, test.want)
			if rejected != nil {
				var schemaErr *jsonschema.ValidationError
				require.ErrorAs(t, rejected, &schemaErr)
				pending := []*jsonschema.ValidationError{schemaErr}
				found := false
				for len(pending) > 0 {
					leaf := pending[0]
					pending = append(pending[1:], leaf.Causes...)
					switch bound := leaf.ErrorKind.(type) {
					case *kind.MinItems:
						assert.Equal(t, 2, bound.Want)
						assert.Equal(t, 1, bound.Got)
						found = true
					case *kind.MaxItems:
						expected := 2
						if test.rule == `"maxItems":0` {
							expected = 0
						}
						assert.Equal(t, expected, bound.Want)
						assert.Equal(t, expected+1, bound.Got)
						found = true
					}
				}
				assert.True(t, found, "the original typed validator bound remains inspectable")
			}
		})
	}
}

func TestArrayLengthCorrectionGeneratedPathsAndAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name, schema, payload, want string
		fields                      []tools.FieldMetadata
	}{
		{name: "independent array limits", schema: `{"type":"object","properties":{"left":{"type":"array","maxItems":1},"right":{"type":"array","maxItems":3}}}`, payload: `{"left":[1],"right":[1,2,3,4]}`, fields: testFieldMetadata(map[string]string{"left": "array", "right": "array"}, nil), want: `Field "right" must contain at most 3 items.`},
		{name: "no combined array limit", schema: `{"type":"object","properties":{"left":{"type":"array","maxItems":3},"right":{"type":"array","maxItems":3}}}`, payload: `{"left":[1,2,3],"right":[1,2,3]}`, fields: testFieldMetadata(map[string]string{"left": "array", "right": "array"}, nil)},
		{name: "nested arrays deduplicate", schema: `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"array","maxItems":1}}}}`, payload: `{"rows":[[1,2],[3,4]]}`, fields: testFieldMetadata(map[string]string{"rows": "array", "rows.*": "array"}, nil), want: `Field "rows.*" must contain at most 1 items.`},
		{name: "map wildcard", schema: `{"type":"object","properties":{"groups":{"type":"object","additionalProperties":{"type":"array","minItems":2}}}}`, payload: `{"groups":{"submitted-key":[]}}`, fields: testFieldMetadata(map[string]string{"groups.*": "array"}, nil), want: `Field "groups.*" must contain at least 2 items.`},
		{name: "literal special name", schema: `{"type":"object","properties":{"a.b":{"type":"array","maxItems":0}}}`, payload: `{"a.b":[1]}`, fields: []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("a.b")}, JSONType: "array"}}, want: `Field "[\"a.b\"]" must contain at most 0 items.`},
		{name: "independent fields", schema: `{"type":"object","properties":{"left":{"type":"array","maxItems":1},"right":{"type":"array","maxItems":3}}}`, payload: `{"left":[1,2],"right":[1,2,3,4]}`, fields: testFieldMetadata(map[string]string{"left": "array", "right": "array"}, nil), want: "Field \"left\" must contain at most 1 items.\nField \"right\" must contain at most 3 items."},
		{name: "competing bounds", schema: `{"type":"object","properties":{"items":{"type":"array","allOf":[{"maxItems":1},{"maxItems":2}]}}}`, payload: `{"items":[1,2,3]}`, fields: testFieldMetadata(map[string]string{"items": "array"}, nil), want: advertisedToolInputCorrection},
		{name: "identical bounds", schema: `{"type":"object","properties":{"items":{"type":"array","allOf":[{"maxItems":1},{"maxItems":1}]}}}`, payload: `{"items":[1,2,3]}`, fields: testFieldMetadata(map[string]string{"items": "array"}, nil), want: `Field "items" must contain at most 1 items.`},
		{name: "unsupported peer", schema: `{"type":"object","properties":{"items":{"type":"array","maxItems":1,"uniqueItems":true}}}`, payload: `{"items":[1,1]}`, fields: testFieldMetadata(map[string]string{"items": "array"}, nil), want: `Field "items" must contain at most 1 items. Other schema errors are not detailed here.`},
		{name: "absent metadata", schema: `{"type":"object","properties":{"items":{"type":"array","maxItems":1}}}`, payload: `{"items":[1,2]}`, want: advertisedToolInputCorrection},
		{name: "wrong metadata type", schema: `{"type":"object","properties":{"items":{"type":"array","maxItems":1}}}`, payload: `{"items":[1,2]}`, fields: testFieldMetadata(map[string]string{"items": "object"}, nil), want: advertisedToolInputCorrection},
		{name: "conflicting metadata", schema: `{"type":"object","properties":{"items":{"type":"array","maxItems":1}}}`, payload: `{"items":[1,2]}`, fields: []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "array"}, {Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "object"}}, want: advertisedToolInputCorrection},
		{name: "array and item problems both reported", schema: `{"type":"object","properties":{"items":{"type":"array","maxItems":1,"items":{"type":"integer"}}}}`, payload: `{"items":[1,"submitted-value"]}`, fields: testFieldMetadata(map[string]string{"items": "array", "items.*": "integer"}, nil), want: "Field \"items\" must contain at most 1 items.\nField \"items.*\" must contain a JSON integer."},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, arrayCorrectionDefinition(test.schema, test.fields), test.payload, test.want)
			assert.Equal(t, test.want == "", rejected == nil)
		})
	}
}

func TestArrayLengthCorrectionSelectedUnionAndSize(t *testing.T) {
	schema := `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object","properties":{"type":{"const":"small"},"value":{"type":"array","maxItems":1}},"required":["type","value"]},{"type":"object","properties":{"type":{"const":"large"},"value":{"type":"array","maxItems":3}},"required":["type","value"]}]}}}`
	discriminator := []tools.FieldPathSegment{tools.FixedField("choice"), tools.FixedField("type")}
	fields := make([]tools.FieldMetadata, 1, 3)
	fields[0] = tools.FieldMetadata{Path: discriminator, JSONType: "string", DiscriminatorValues: []string{"small", "large"}}
	for _, branch := range []string{"small", "large"} {
		fields = append(fields, tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("choice"), tools.FixedField("value")}, JSONType: "array", Branches: []tools.UnionBranch{{Discriminator: discriminator, Value: branch}}})
	}
	for _, test := range []struct{ name, payload, want string }{
		{"selected small", `{"choice":{"type":"small","value":[1,2]}}`, `Field "choice.value" must contain at most 1 items.`},
		{"selected large", `{"choice":{"type":"large","value":[1,2,3,4]}}`, `Field "choice.value" must contain at most 3 items.`},
		{"valid other branch", `{"choice":{"type":"large","value":[1,2]}}`, ""},
		{"unselected", `{"choice":{"type":"unknown","value":[1,2,3,4]}}`, `Field "choice.type" must be one of these JSON strings: ["small","large"]. Other schema errors are not detailed here.`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), test.payload, test.want)
			assert.Equal(t, test.want == "", rejected == nil)
		})
	}
	base := `Field "items" must contain at most 1 items. Field description: "".`
	for _, size := range []int{correction.MaxBytes - 1, correction.MaxBytes, correction.MaxBytes + 1} {
		t.Run(fmt.Sprintf("correction bytes %d", size), func(t *testing.T) {
			description := strings.Repeat("x", size-len(base))
			field := tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "array", Description: description}
			want := fmt.Sprintf(`Field "items" must contain at most 1 items. Field description: %q.`, description)
			if size > correction.MaxBytes {
				want = advertisedToolInputCorrection
			}
			require.NotNil(t, checkArrayCorrection(t, arrayCorrectionDefinition(`{"type":"object","properties":{"items":{"type":"array","maxItems":1}}}`, []tools.FieldMetadata{field}), `{"items":[1,2]}`, want))
		})
	}
}

// arrayCorrectionDefinition gives the real request validator an advertised
// schema, generated-shape field metadata and its ordinary JSON decoder.
func arrayCorrectionDefinition(schema string, fields []tools.FieldMetadata) *ToolDefinition {
	return ToolDefinitionFromSpec(tools.ToolSpec{Name: "catalog.batch", Payload: tools.TypeSpec{
		Name: "Batch", Schema: rawjson.Message(schema), Fields: fields, Codec: tools.AnyJSONCodec,
	}})
}

// checkArrayCorrection compares unary and streamed validation, preserving exact
// accepted arguments and the original rejection. An empty expectation is valid.
func checkArrayCorrection(t *testing.T, definition *ToolDefinition, payload, want string) *OutputValidationError {
	t.Helper()
	contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
	require.NoError(t, err)
	call := ToolCall{ID: "call-1", Name: tools.Ident(definition.Name), Payload: rawjson.Message(payload)}
	response := responseWithToolCall(call)
	validated, err := contract.ValidateResponse(response)
	if want == "" {
		require.NoError(t, err)
		require.NotNil(t, validated)
		assert.Equal(t, payload, string(validated.ToolCalls()[0].Payload))
		return nil
	}
	require.Nil(t, validated)
	var rejected *OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, want, rejected.RecoveryCorrection())
	assert.LessOrEqual(t, len(rejected.RecoveryCorrection()), correction.MaxBytes)
	var schemaErr *jsonschema.ValidationError
	require.ErrorAs(t, err, &schemaErr)
	assert.NotEmpty(t, schemaErr.Error())
	saved, err := rejected.RejectedResponse()
	require.NoError(t, err)
	assert.Equal(t, payload, string(saved.ToolCalls()[0].Payload))
	stream, err := contract.ValidateStream(&validatedStreamFixture{chunks: []Chunk{ToolCallChunk{ToolCall: call}, StopChunk{Reason: "tool_use"}}, response: response})
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	var streamed *OutputValidationError
	require.ErrorAs(t, err, &streamed)
	assert.Equal(t, rejected.RecoveryCorrection(), streamed.RecoveryCorrection())
	require.NoError(t, stream.Close())
	return rejected
}
