// These tests use real schema failures to distinguish independent field repairs
// from alternative requirements. Correction formatting never changes validation,
// rejected arguments, selected union branches, or the complete-example contract.
package model

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestMultipleFieldCorrectionIndependentFailures(t *testing.T) {
	const schema = `{"type":"object","properties":{"left":{"type":"object"},"right":{"type":"object"}},"required":["left","right"],"additionalProperties":false}`
	fields := testFieldMetadata(map[string]string{"$payload": "object", "left": "object", "right": "object"}, nil)
	for _, test := range []struct{ name, payload, want string }{
		{"missing fields and root extra", `{"submitted-name":"submitted-value"}`, "Field \"$payload\" contains an undeclared field.\nField \"left\" is required.\nField \"right\" is required."},
		{"multiple wrong types and root extra", `{"left":"submitted-value","right":[],"submitted-name":true}`, "Field \"$payload\" contains an undeclared field.\nField \"left\" must contain a JSON object.\nField \"right\" must contain a JSON object."},
		{"valid objects preserved", `{ "left": {}, "right": {} }`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for range 10 {
				rejected := checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), test.payload, test.want)
				assert.Equal(t, test.want == "", rejected == nil)
			}
		})
	}
}

func TestMultipleFieldCorrectionTraversesOnlyRequiredConstraints(t *testing.T) {
	const known = `Field "known" must contain a JSON string.`
	for _, test := range []struct{ name, rule, payload, want string }{
		{"allOf", `"allOf":[{"required":["left"]},{"required":["right"]}]`, `{"known":1}`, known + "\nField \"left\" is required.\nField \"right\" is required."},
		{"reference", `"$defs":{"pair":{"required":["left","right"]}},"$ref":"#/$defs/pair"`, `{"known":1}`, known + "\nField \"left\" is required.\nField \"right\" is required."},
		{"anyOf alternatives", `"anyOf":[{"required":["left"]},{"required":["right"]}]`, `{"known":1}`, known + " Other schema errors are not detailed here."},
		{"unselected oneOf alternatives", `"oneOf":[{"required":["left"]},{"required":["right"]}]`, `{"known":1}`, known + " Other schema errors are not detailed here."},
		{"property names", `"propertyNames":{"enum":["allowed"]}`, `{"known":1}`, known + " Other schema errors are not detailed here."},
		{"unsupported only", `"anyOf":[{"required":["left"]},{"required":["right"]}]`, `{"known":"valid"}`, advertisedToolInputCorrection},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := fmt.Sprintf(`{"type":"object","properties":{"known":{"type":"string"},"left":{"type":"string"},"right":{"type":"string"}},%s}`, test.rule)
			fields := testFieldMetadata(map[string]string{"$payload": "object", "known": "string", "left": "string", "right": "string"}, nil)
			require.NotNil(t, checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), test.payload, test.want))
		})
	}
	for _, rule := range []string{`"contains":{"type":"string"}`, `"contains":{"type":"string"},"minContains":2`} {
		schema := fmt.Sprintf(`{"type":"object","properties":{"known":{"type":"string"},"items":{"type":"array",%s}}}`, rule)
		fields := testFieldMetadata(map[string]string{"known": "string", "items.*": "string"}, nil)
		require.NotNil(t, checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), `{"known":1,"items":[1,2]}`, known+" Other schema errors are not detailed here."))
	}
}

func TestMultipleFieldCorrectionSelectedUnionWildcardAmbiguity(t *testing.T) {
	const schema = `{"type":"object","properties":{"known":{"type":"string"},"choices":{"type":"array","items":{"oneOf":[{"type":"object","properties":{"type":{"const":"short"},"value":{"type":"array","maxItems":1}},"required":["type","value"]},{"type":"object","properties":{"type":{"const":"long"},"value":{"type":"array","maxItems":2}},"required":["type","value"]}]}}}}`
	discriminator := []tools.FieldPathSegment{tools.FixedField("choices"), tools.DynamicField{}, tools.FixedField("type")}
	fields := make([]tools.FieldMetadata, 2, 4)
	fields[0] = tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("known")}, JSONType: "string"}
	fields[1] = tools.FieldMetadata{Path: discriminator, JSONType: "string", DiscriminatorValues: []string{"short", "long"}}
	for _, branch := range []string{"short", "long"} {
		fields = append(fields, tools.FieldMetadata{
			Path: []tools.FieldPathSegment{tools.FixedField("choices"), tools.DynamicField{}, tools.FixedField("value")}, JSONType: "array",
			Branches: []tools.UnionBranch{{Discriminator: discriminator, Value: branch}},
		})
	}
	for _, test := range []struct{ name, payload, want string }{
		{"identical displayed requirements", `{"known":1,"choices":[{"type":"short","value":[1,2]},{"type":"short","value":[3,4]}]}`, "Field \"choices.*.value\" must contain at most 1 items.\nField \"known\" must contain a JSON string."},
		{"conflicting displayed requirements", `{"known":1,"choices":[{"type":"short","value":[1,2]},{"type":"long","value":[3,4,5]}]}`, `Field "known" must contain a JSON string. Other schema errors are not detailed here.`},
		{"missing discriminator", `{"known":1,"choices":[{"value":[1,2,3]}]}`, `Field "known" must contain a JSON string. Other schema errors are not detailed here.`},
		{"unknown discriminator", `{"known":1,"choices":[{"type":"submitted-name","value":[1,2,3]}]}`, `Field "known" must contain a JSON string. Other schema errors are not detailed here.`},
		{"nonstring discriminator", `{"known":1,"choices":[{"type":7,"value":[1,2,3]}]}`, `Field "known" must contain a JSON string. Other schema errors are not detailed here.`},
		{"valid selected members", `{"known":"valid","choices":[{"type":"short","value":[1]},{"type":"long","value":[1,2]}]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), test.payload, test.want)
			assert.Equal(t, test.want == "", rejected == nil)
		})
	}
}

func TestMultipleFieldCorrectionKeepsWholeInstructionsWithinByteLimit(t *testing.T) {
	fields := []toolCorrectionCandidate{
		{field: tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("a")}}, constraint: "is required."},
		{field: tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("b")}}, constraint: "is required."},
	}
	const descriptionPrefix = ` Field description: "".`
	base := formatToolCorrections(fields)
	remaining := correction.MaxBytes - len(base) - len(descriptionPrefix)
	fields[0].field.Description = "é" + strings.Repeat("x", remaining-len("é"))
	atLimit := formatToolCorrections(fields)
	assert.Len(t, atLimit, correction.MaxBytes)
	assert.True(t, utf8.ValidString(atLimit))
	assert.NotContains(t, atLimit, "not detailed")
	fields[0].field.Description += "x"
	assert.Equal(t, `Field "b" is required. Other schema errors are not detailed here.`, formatToolCorrections(fields))
	// Both metadata descriptions and schema enum values are indivisible. A large
	// first instruction must not erase the short useful instruction after it.
	fields[0].field.Description = ""
	fields[0].constraint = fmt.Sprintf("must contain one of these JSON values: [%q].", strings.Repeat("é", correction.MaxBytes))
	assert.Equal(t, `Field "b" is required. Other schema errors are not detailed here.`, formatToolCorrections(fields))
	assert.Equal(t, advertisedToolInputCorrection, formatToolCorrections(fields[:1]))
	many := make([]toolCorrectionCandidate, 0, 200)
	for i := range 200 {
		many = append(many, toolCorrectionCandidate{field: tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField(fmt.Sprintf("field_%03d", i))}}, constraint: "is required."})
	}
	bounded := formatToolCorrections(many)
	assert.LessOrEqual(t, len(bounded), correction.MaxBytes)
	assert.True(t, utf8.ValidString(bounded))
	assert.Contains(t, bounded, "Other schema errors are not detailed here.")
	assert.Contains(t, bounded, `Field "field_000" is required.`)
	assert.NotContains(t, bounded, "field_199")
	slices.Reverse(many)
	assert.Equal(t, bounded, formatToolCorrections(many), "validator iteration order cannot choose which instructions fit")
}

func TestMultipleFieldCorrectionPreservesWholeExampleBudget(t *testing.T) {
	const schema = `{"type":"object","properties":{"left":{"type":"string"},"right":{"type":"string"}},"required":["left","right"]}`
	fields := testFieldMetadata(map[string]string{"left": "string", "right": "string"}, nil)
	get := func(example string) string {
		definition := exampleCorrectionDefinition(schema, example, fields)
		contract, err := NewRequestContract(&Request{Tools: []*ToolDefinition{definition}})
		require.NoError(t, err)
		_, err = contract.ValidateResponse(responseWithToolCall(ToolCall{Name: "catalog.lookup", ID: "rejected", Payload: rawjson.Message(`{}`)}))
		var rejected *OutputValidationError
		require.ErrorAs(t, err, &rejected)
		return rejected.RecoveryCorrection()
	}
	base := get("")
	const example = `{"left":"é","right":"chosen"}`
	remaining := correction.MaxBytes - len(get(example))
	atLimit := strings.Replace(example, "é", "é"+strings.Repeat("x", remaining), 1)
	result := get(atLimit)
	assert.Len(t, result, correction.MaxBytes)
	assert.True(t, utf8.ValidString(result))
	assert.True(t, strings.HasSuffix(result, atLimit))
	assert.Equal(t, base, get(strings.Replace(atLimit, "é", "éx", 1)))
}
