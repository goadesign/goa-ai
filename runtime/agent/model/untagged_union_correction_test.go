// These tests send untagged union arguments through the model request validator.
// Correction guidance uses the submitted JSON kind and the matching array index,
// so a different branch cannot supply its field type or description.
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUntaggedUnionCorrectionSelectsJSONKindInsideArray(t *testing.T) {
	path := []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}}
	fields := []tools.FieldMetadata{
		{
			Path:     append([]tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}}, tools.FixedField("label")),
			JSONType: "string", Description: "Object label",
			Branches: []tools.UnionBranch{tools.UntaggedUnionBranch{Path: path, JSONKind: "object"}},
		},
		{
			Path:     append([]tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}}, tools.DynamicField{}),
			JSONType: "integer", Description: "Whole item",
			Branches: []tools.UnionBranch{tools.UntaggedUnionBranch{Path: path, JSONKind: "array", Index: 1}},
		},
	}
	schema := `{"type":"object","properties":{"items":{"type":"array","items":{"oneOf":[{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false},{"type":"array","items":{"type":"integer"}}]}}},"required":["items"],"additionalProperties":false}`
	definition := arrayCorrectionDefinition(schema, fields)
	for _, test := range []struct{ name, payload, want string }{
		{"object field", `{"items":[{"label":7}]}`, `Field "items.*.label" must contain a JSON string. Field description: "Object label".`},
		{"missing object field", `{"items":[{}]}`, `Field "items.*.label" is required. Field description: "Object label".`},
		{"array item", `{"items":[["wrong"]]}`, `Field "items.*.*" must contain a JSON integer. Field description: "Whole item".`},
		{"independent array selections", `{"items":[{"label":"valid"},["wrong"]]}`, `Field "items.*.*" must contain a JSON integer. Field description: "Whole item".`},
		{"valid distinct branches", `{"items":[{"label":"valid"},[1,2]]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, definition, test.payload, test.want)
			assert.Equal(t, test.want == "", rejected == nil)
		})
	}
}
