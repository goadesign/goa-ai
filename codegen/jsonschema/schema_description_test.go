// These checks keep a field's authored instructions separate from the general
// description of its named type, including types reused by several fields.
package jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contract "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestBuildPreservesNamedFieldDescriptions(t *testing.T) {
	for _, shape := range []expr.DataType{expr.String, &expr.Object{
		{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}} {
		t.Run(shape.Name(), func(t *testing.T) {
			shared := &expr.UserTypeExpr{TypeName: "SharedValue", AttributeExpr: &expr.AttributeExpr{
				Type: shape, Description: "General value documentation.",
			}}
			attribute := &expr.AttributeExpr{Type: &expr.Object{
				{Name: "first", Attribute: &expr.AttributeExpr{Type: shared, Description: "Select the first resource."}},
				{Name: "second", Attribute: &expr.AttributeExpr{Type: shared, Description: "Select the second resource."}},
				{Name: "ordinary", Attribute: &expr.AttributeExpr{Type: shared}},
			}}
			api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
			encoded, err := Build(api, attribute, expr.UserTypeExampleIdentity(shared))
			require.NoError(t, err)
			var document struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
				Definitions map[string]struct {
					Description string `json:"description"`
				} `json:"$defs"`
			}
			require.NoError(t, json.Unmarshal(encoded, &document))
			assert.Equal(t, "Select the first resource.", document.Properties["first"].Description)
			assert.Equal(t, "Select the second resource.", document.Properties["second"].Description)
			if shape == expr.String {
				assert.Equal(t, "General value documentation.", document.Properties["ordinary"].Description)
			} else {
				assert.Empty(t, document.Properties["ordinary"].Description)
				assert.Equal(t, "General value documentation.", document.Definitions["SharedValue"].Description)
			}
			assert.Equal(t, "General value documentation.", shared.Description)
		})
	}
}

func TestBuildRetainsSharedRequiredFieldsFromEvaluatedDSL(t *testing.T) {
	var value expr.UserType
	root := codegen.RunDSL(t, func() {
		dsl.API("field descriptions", func() {})
		shared := dsl.Type("SharedValue", func() {
			dsl.Description("General value documentation.")
			dsl.Attribute("value", dsl.String, "Selected resource.")
		})
		value = dsl.Type("Selection", func() {
			dsl.Attribute("first", shared, "Select the first resource.", func() { dsl.Required("value") })
			dsl.Attribute("second", shared, "Select the second resource.")
			dsl.Required("first", "second")
		})
		dsl.Service("catalog", func() {
			dsl.Method("select", func() { dsl.Payload(value) })
		})
	})
	// Required updates the named type during DSL execution. Both references
	// must keep that requirement while retaining their own field instructions.
	assert.True(t, value.Attribute().Find("first").IsRequired("value"))
	assert.True(t, value.Attribute().Find("second").IsRequired("value"))
	encoded, err := Build(root.API, value.Attribute(), expr.UserTypeExampleIdentity(value))
	require.NoError(t, err)
	compiled, err := contract.Compile(encoded)
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		input string
		valid bool
	}{
		{"both complete", `{"first":{"value":"first"},"second":{"value":"second"}}`, true},
		{"first incomplete", `{"first":{},"second":{"value":"second"}}`, false},
		{"second incomplete", `{"first":{"value":"first"},"second":{}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := contract.Validate(compiled, []byte(test.input))
			assert.Equal(t, test.valid, err == nil, "validation: %v", err)
		})
	}
}
