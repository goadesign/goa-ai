// These tests compare extra JSON members with the schema advertised to callers.
package jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	contract "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/expr"
)

func TestEmbeddedMapSchema(t *testing.T) {
	for _, naming := range []string{"inline", "named", "alias"} {
		t.Run(naming, func(t *testing.T) {
			for _, raw := range []bool{false, true} {
				t.Run(map[bool]string{false: "typed", true: "raw"}[raw], func(t *testing.T) {
					element := &expr.AttributeExpr{Type: expr.String}
					if raw {
						element = &expr.AttributeExpr{Type: expr.Bytes, Meta: expr.MetaExpr{"struct:field:type": {"json.RawMessage", "encoding/json"}}}
					}
					var mapType expr.DataType = &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: element}
					if naming != "inline" {
						mapType = &expr.UserTypeExpr{TypeName: "Extras", AttributeExpr: &expr.AttributeExpr{Type: mapType}}
					}
					if naming == "alias" {
						mapType = &expr.UserTypeExpr{TypeName: "ExtraAlias", AttributeExpr: &expr.AttributeExpr{Type: mapType}}
					}
					a := &expr.AttributeExpr{Type: &expr.Object{
						{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {"message,case:strict"}}}},
						{Name: "extras", Attribute: &expr.AttributeExpr{Type: mapType, Meta: expr.MetaExpr{"struct:tag:json": {",embed"}}}},
					}, Validation: &expr.ValidationExpr{Required: []string{"message"}}}
					api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
					encoded, err := Build(api, a, expr.UserTypeExampleIdentity(&expr.UserTypeExpr{TypeName: "OpenValue", AttributeExpr: a}))
					require.NoError(t, err)
					var document map[string]any
					require.NoError(t, json.Unmarshal(encoded, &document))
					properties := document["properties"].(map[string]any)
					assert.NotContains(t, properties, "extras")
					assert.Contains(t, properties, "message")
					compiled, err := contract.Compile(encoded)
					require.NoError(t, err)
					for _, tc := range []struct {
						input string
						valid bool
					}{
						{`{"message":"known"}`, true},
						{`{"message":"known","extra":"text"}`, true},
						{`{"message":"known","extra":{"integer":9007199254740993123456789}}`, raw},
						{`{"message":"known","extra":null}`, raw},
						{`{"extra":"text"}`, false},
						{`{"message":false}`, false},
					} {
						err := contract.Validate(compiled, []byte(tc.input))
						assert.Equal(t, tc.valid, err == nil, "%s: %v", tc.input, err)
					}
				})
			}
		})
	}
}

func TestEmbeddedSiblingSchemaNames(t *testing.T) {
	for _, name := range []string{"", "-", "'message'", `mes\\sage`, "説明"} {
		t.Run(name, func(t *testing.T) {
			a := &expr.AttributeExpr{Type: &expr.Object{
				{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {name + ",case:strict"}}}},
				{Name: "extras", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.String}}, Meta: expr.MetaExpr{"struct:tag:json": {",embed"}}}},
			}, Validation: &expr.ValidationExpr{Required: []string{"message"}}}
			api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
			encoded, err := Build(api, a, expr.UserTypeExampleIdentity(&expr.UserTypeExpr{TypeName: "NamedValue", AttributeExpr: a}))
			if name != "説明" {
				require.ErrorContains(t, err, "JSON name")
				return
			}
			require.NoError(t, err)
			compiled, err := contract.Compile(encoded)
			require.NoError(t, err)
			assert.NoError(t, contract.Validate(compiled, []byte(`{"説明":"known","MESSAGE":"extra"}`)))
			assert.Error(t, contract.Validate(compiled, []byte(`{"message":"known","MESSAGE":"extra"}`)))
		})
	}
}
