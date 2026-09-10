package model

// Union correction tests use the real request and stream validators. Generated
// field metadata names the available choices; rejected arguments never choose
// a branch implicitly and remain available as the original diagnostic evidence.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/tools"
)

const correctionChoiceSchema = `{"type":"object","oneOf":[{"type":"object","properties":{"kind":{"const":"text"},"value":{"type":"string"}},"required":["kind","value"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"count"},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false}]}`

func TestUnionDiscriminatorCorrectionChoicesAndRejections(t *testing.T) {
	schema := fmt.Sprintf(`{"type":"object","properties":{"choice":%s},"required":["choice"],"additionalProperties":false}`, correctionChoiceSchema)
	definition := arrayCorrectionDefinition(schema, correctionChoiceFields([]tools.FieldPathSegment{tools.FixedField("choice")}, nil))
	for _, test := range []struct{ name, payload, want string }{
		{"missing", `{"choice":{}}`, `Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`},
		{"number", `{"choice":{"kind":7}}`, `Field "choice.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`},
		{"boolean", `{"choice":{"kind":true}}`, `Field "choice.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`},
		{"null", `{"choice":{"kind":null}}`, `Field "choice.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`},
		{"object", `{"choice":{"kind":{}}}`, `Field "choice.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`},
		{"array", `{"choice":{"kind":[]}}`, `Field "choice.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`},
		{"unknown", `{"choice":{"kind":"submitted-choice","value":7}}`, `Field "choice.kind" must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`},
		{"empty string", `{"choice":{"kind":""}}`, `Field "choice.kind" must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`},
		{"valid text", `{ "choice" : { "kind" : "text", "value" : "hello" } }`, ""},
		{"valid count", `{ "choice" : { "kind" : "count", "value" : 7 } }`, ""},
		{"selected text", `{"choice":{"kind":"text","value":7}}`, `Field "choice.value" must contain a JSON string.`},
		{"selected count", `{"choice":{"kind":"count","value":"submitted-value"}}`, `Field "choice.value" must contain a JSON integer.`},
		{"selected missing value", `{"choice":{"kind":"text"}}`, `Field "choice.value" is required.`},
		{"scalar container", `{"choice":7}`, `Field "choice" must contain a JSON object.`},
		{"null container", `{"choice":null}`, `Field "choice" must contain a JSON object.`},
		{"array container", `{"choice":[]}`, `Field "choice" must contain a JSON object.`},
		{"undeclared sibling does not select a branch", `{"extra":"submitted-intent","choice":{}}`, `Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, definition, test.payload, test.want)
			if rejected != nil {
				assert.NotContains(t, rejected.RecoveryCorrection(), "submitted-")
			}
		})
	}
}

func TestUnionDiscriminatorCorrectionCollections(t *testing.T) {
	for _, test := range []struct {
		name, schema, payload, want string
		path                        []tools.FieldPathSegment
	}{
		{
			name: "array item", schema: fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","items":%s}}}`, correctionChoiceSchema),
			payload: `{"items":[{"kind":"count","value":1},{}]}`,
			path:    []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}},
			want:    `Field "items.*.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`,
		},
		{
			name: "map item", schema: fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"object","additionalProperties":%s}}}`, correctionChoiceSchema),
			payload: `{"items":{"submitted-map-key":{"kind":7}}}`,
			path:    []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}},
			want:    `Field "items.*.kind" must contain a JSON string from these values: ["text","count"]. Other schema errors are not detailed here.`,
		},
		{
			name: "nested arrays and maps", schema: fmt.Sprintf(`{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","additionalProperties":%s}}}}`, correctionChoiceSchema),
			payload: `{"rows":[{"submitted-map-key":{"kind":"submitted-choice"}}]}`,
			path:    []tools.FieldPathSegment{tools.FixedField("rows"), tools.DynamicField{}, tools.DynamicField{}},
			want:    `Field "rows.*.*.kind" must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`,
		},
		{
			name: "identical item errors collapse", schema: fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","items":%s}}}`, correctionChoiceSchema),
			payload: `{"items":[{},{}]}`,
			path:    []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}},
			want:    `Field "items.*.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`,
		},
		{
			name: "different item errors conflict", schema: fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","items":%s}}}`, correctionChoiceSchema),
			payload: `{"items":[{},{"kind":7}]}`,
			path:    []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}},
			want:    advertisedToolInputCorrection,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, arrayCorrectionDefinition(test.schema, correctionChoiceFields(test.path, nil)), test.payload, test.want)
			require.NotNil(t, rejected)
			assert.NotContains(t, rejected.RecoveryCorrection(), "submitted-")
		})
	}
}

func TestUnionDiscriminatorCorrectionOuterBranchCorrelation(t *testing.T) {
	schema := fmt.Sprintf(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","oneOf":[{"properties":{"mode":{"const":"nested"},"value":%s},"required":["mode","value"]},{"properties":{"mode":{"const":"flat"},"value":{"type":"integer"}},"required":["mode","value"]}]}}}}`, correctionChoiceSchema)
	outer := []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}, tools.FixedField("mode")}
	inner := []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}, tools.FixedField("value")}
	fields := make([]tools.FieldMetadata, 0, 6)
	fields = append(fields, tools.FieldMetadata{Path: outer, JSONType: "string", DiscriminatorValues: []string{"nested", "flat"}})
	fields = append(fields, correctionChoiceFields(inner, []tools.UnionBranch{{Discriminator: outer, Value: "nested"}})...)
	fields = append(fields, tools.FieldMetadata{Path: inner, JSONType: "integer", Branches: []tools.UnionBranch{{Discriminator: outer, Value: "flat"}}})
	definition := arrayCorrectionDefinition(schema, fields)
	for _, test := range []struct{ name, payload, want string }{
		{"selected outer branch", `{"items":[{"mode":"flat","value":7},{"mode":"nested","value":{}}]}`, `Field "items.*.value.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`},
		{"nested selected branch", `{"items":[{"mode":"nested","value":{"kind":"count","value":"submitted-value"}}]}`, `Field "items.*.value.value" must contain a JSON integer.`},
		{"inactive inner union", `{"items":[{"mode":"flat","value":{}}]}`, `Field "items.*.value" must contain a JSON integer.`},
		{"missing outer selector", `{"items":[{"value":{}}]}`, `Field "items.*.mode" is required and must be one of these JSON strings: ["nested","flat"]. Other schema errors are not detailed here.`},
		{"valid nested", `{"items":[{"mode":"nested","value":{"kind":"text","value":"ok"}}]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, definition, test.payload, test.want)
			assert.Equal(t, test.want == "", rejected == nil)
		})
	}
}

func TestUnionDiscriminatorCorrectionMetadataAndCompetingErrors(t *testing.T) {
	choice := []tools.FieldPathSegment{tools.FixedField("choice")}
	baseFields := correctionChoiceFields(choice, nil)
	baseSchema := fmt.Sprintf(`{"type":"object","properties":{"choice":%s}}`, correctionChoiceSchema)
	for _, test := range []struct {
		name, schema, payload, want string
		fields                      []tools.FieldMetadata
	}{
		{"absent metadata", baseSchema, `{"choice":{}}`, advertisedToolInputCorrection, nil},
		{"identical metadata", baseSchema, `{"choice":{}}`, advertisedToolInputCorrection, append(slices.Clone(baseFields), baseFields[1])},
		{"conflicting choices", baseSchema, `{"choice":{}}`, advertisedToolInputCorrection, append(slices.Clone(baseFields), tools.FieldMetadata{Path: baseFields[1].Path, JSONType: "string", DiscriminatorValues: []string{"other"}})},
		{"conflicting type", baseSchema, `{"choice":{}}`, advertisedToolInputCorrection, append(slices.Clone(baseFields), tools.FieldMetadata{Path: baseFields[1].Path, JSONType: "integer", DiscriminatorValues: []string{"text", "count"}})},
		{"unmatched enclosing branch", baseSchema, `{"choice":{}}`, advertisedToolInputCorrection, correctionChoiceFields(choice, []tools.UnionBranch{{Discriminator: []tools.FieldPathSegment{tools.FixedField("mode")}, Value: "nested"}})},
		{
			"two union failures", fmt.Sprintf(`{"type":"object","properties":{"choice":%s,"other":%s}}`, correctionChoiceSchema, correctionChoiceSchema),
			`{"choice":{},"other":{}}`, "Field \"choice.kind\" is required and must be one of these JSON strings: [\"text\",\"count\"].\nField \"other.kind\" is required and must be one of these JSON strings: [\"text\",\"count\"]. Other schema errors are not detailed here.",
			append(slices.Clone(baseFields), correctionChoiceFields([]tools.FieldPathSegment{tools.FixedField("other")}, nil)...),
		},
		{
			"unsupported peer", fmt.Sprintf(`{"type":"object","properties":{"choice":%s,"peer":{"type":"object","properties":{"name":{"type":"string","minLength":3}}}}}`, correctionChoiceSchema),
			`{"choice":{},"peer":{"name":"x"}}`, `Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`,
			append(slices.Clone(baseFields), tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("peer"), tools.FixedField("name")}, JSONType: "string"}),
		},
		{
			"unsupported deeper failure", fmt.Sprintf(`{"type":"object","properties":{"choice":%s,"peer":{"type":"object","properties":{"nested":{"type":"object","properties":{"name":{"type":"string","minLength":3}}}}}}}`, correctionChoiceSchema),
			`{"choice":{},"peer":{"nested":{"name":"x"}}}`, `Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Other schema errors are not detailed here.`,
			append(slices.Clone(baseFields), tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("peer"), tools.FixedField("nested"), tools.FixedField("name")}, JSONType: "string"}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NotNil(t, checkArrayCorrection(t, arrayCorrectionDefinition(test.schema, test.fields), test.payload, test.want))
		})
	}
}

func TestUnionDiscriminatorCorrectionSizeBudget(t *testing.T) {
	base := `Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Field description: "". Other schema errors are not detailed here.`
	for _, size := range []int{correction.MaxBytes - 1, correction.MaxBytes, correction.MaxBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			fields := correctionChoiceFields([]tools.FieldPathSegment{tools.FixedField("choice")}, nil)
			fields[1].Description = strings.Repeat("x", size-len(base))
			want := fmt.Sprintf(`Field "choice.kind" is required and must be one of these JSON strings: ["text","count"]. Field description: %q. Other schema errors are not detailed here.`, fields[1].Description)
			if size > correction.MaxBytes {
				want = advertisedToolInputCorrection
			}
			schema := fmt.Sprintf(`{"type":"object","properties":{"choice":%s}}`, correctionChoiceSchema)
			require.NotNil(t, checkArrayCorrection(t, arrayCorrectionDefinition(schema, fields), `{"choice":{}}`, want))
		})
	}
}

// correctionChoiceFields mirrors generated metadata for a two-variant union,
// including any enclosing branch requirements at the current array or map item.
func correctionChoiceFields(path []tools.FieldPathSegment, branches []tools.UnionBranch) []tools.FieldMetadata {
	discriminator := append(slices.Clone(path), tools.FixedField("kind"))
	fields := make([]tools.FieldMetadata, 0, 4)
	fields = append(fields,
		tools.FieldMetadata{Path: slices.Clone(path), JSONType: "object", Branches: slices.Clone(branches)},
		tools.FieldMetadata{Path: discriminator, JSONType: "string", Branches: slices.Clone(branches), DiscriminatorValues: []string{"text", "count"}},
	)
	for index, name := range []string{"text", "count"} {
		fields = append(fields, tools.FieldMetadata{
			Path: append(slices.Clone(path), tools.FixedField("value")), JSONType: []string{"string", "integer"}[index],
			Branches: append(slices.Clone(branches), tools.UnionBranch{Discriminator: discriminator, Value: name}),
		})
	}
	return fields
}
