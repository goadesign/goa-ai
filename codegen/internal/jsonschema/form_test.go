// These tests keep authored constraints intact across MCP form generation.
// Unsupported rules fail, and ordinary tool schemas retain closed objects.
package jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contract "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/expr"
)

func TestBuildFormPreservesAuthoredContract(t *testing.T) {
	minimum, maximum := float64(1), float64(3)
	minLength, maxLength := 2, 5
	name := &expr.UserTypeExpr{TypeName: "FormName", AttributeExpr: &expr.AttributeExpr{
		Type: expr.String, Validation: &expr.ValidationExpr{MinLength: &minLength, MaxLength: &maxLength},
	}}
	attribute := &expr.AttributeExpr{Type: &expr.Object{
		{Name: "name", Attribute: &expr.AttributeExpr{Type: name, Description: "Your public name", DefaultValue: "Ada", Meta: expr.MetaExpr{"struct:tag:json": {"displayName,omitempty"}}}},
		{Name: "quantity", Attribute: &expr.AttributeExpr{Type: expr.Int, Validation: &expr.ValidationExpr{Minimum: &minimum, Maximum: &maximum}}},
		{Name: "options", Attribute: &expr.AttributeExpr{Type: &expr.Array{NonNullableElems: true, ElemType: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Values: []any{"red", "blue"}}}}, Validation: &expr.ValidationExpr{MinLength: &minLength, MaxLength: &maxLength}}},
	}, Description: "Accepted values documented by the service", Validation: &expr.ValidationExpr{Required: []string{"name", "quantity"}}}
	api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
	identity := expr.UserTypeExampleIdentity(name)
	named := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "AcceptedForm", AttributeExpr: attribute}}
	encoded, err := BuildForm(api, named, identity)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"$ref"`)
	assert.NotContains(t, string(encoded), `"$defs"`)
	assert.NotContains(t, string(encoded), `"additionalProperties"`)
	assert.Contains(t, string(encoded), `"default":"Ada"`)
	assert.Contains(t, string(encoded), `"description":"Your public name"`)
	compiled, err := contract.Compile(encoded)
	require.NoError(t, err)
	for _, test := range []struct {
		name, value string
		valid       bool
	}{
		{"below", `{"displayName":"Ada","quantity":0}`, false},
		{"minimum", `{"displayName":"Ada","quantity":1}`, true},
		{"maximum", `{"displayName":"Ada","quantity":3}`, true},
		{"integral notation", `{"displayName":"Ada","quantity":3.0}`, true},
		{"above", `{"displayName":"Ada","quantity":4}`, false},
		{"fraction", `{"displayName":"Ada","quantity":1.5}`, false},
		{"short", `{"displayName":"A","quantity":1}`, false},
		{"long", `{"displayName":"longer","quantity":1}`, false},
		{"selection", `{"displayName":"Ada","quantity":1,"options":["red","blue"]}`, true},
		{"unknown selection", `{"displayName":"Ada","quantity":1,"options":["red","green"]}`, false},
		{"short selection", `{"displayName":"Ada","quantity":1,"options":["red"]}`, false},
		{"missing", `{"name":"Ada","quantity":1}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := contract.Validate(compiled, []byte(test.value))
			assert.Equal(t, test.valid, err == nil, "validation: %v", err)
		})
	}
	tool, err := Build(api, attribute, identity)
	require.NoError(t, err)
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(tool, &document))
	assert.JSONEq(t, `false`, string(document["additionalProperties"]))
	assert.Equal(t, 2, *name.Validation.MinLength)
	assert.Equal(t, "Accepted values documented by the service", attribute.Description)
}

func TestBuildFormRejectsUnsupportedRules(t *testing.T) {
	bound := float64(1)
	for _, test := range []struct {
		name  string
		field *expr.AttributeExpr
	}{
		{"hidden JSON field", &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {"-"}}}},
		{"stringified number", &expr.AttributeExpr{Type: expr.Int, Meta: expr.MetaExpr{"struct:tag:json": {"field,string"}}}},
		{"pattern", &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Pattern: "^x"}}},
		{"unsupported format", &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Format: expr.FormatUUID}}},
		{"exclusive bound", &expr.AttributeExpr{Type: expr.Int, Validation: &expr.ValidationExpr{ExclusiveMinimum: &bound}}},
		{"numeric enum", &expr.AttributeExpr{Type: expr.Int, Validation: &expr.ValidationExpr{Values: []any{1, 2}}}},
		{"nested object", &expr.AttributeExpr{Type: &expr.Object{}}},
		{"arbitrary array", &expr.AttributeExpr{Type: &expr.Array{NonNullableElems: true, ElemType: &expr.AttributeExpr{Type: expr.String}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			attribute := &expr.AttributeExpr{Type: &expr.Object{{Name: "field", Attribute: test.field}}}
			api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
			identity := expr.UserTypeExampleIdentity(&expr.UserTypeExpr{TypeName: "UnsupportedForm"})
			_, err := BuildForm(api, attribute, identity)
			assert.ErrorContains(t, err, "unsupported form schema")
		})
	}
}

// An authored form without fields still has a properties object on the MCP wire.
func TestBuildFormEmptyObject(t *testing.T) {
	attribute := &expr.AttributeExpr{Type: &expr.Object{}}
	api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
	identity := expr.UserTypeExampleIdentity(&expr.UserTypeExpr{TypeName: "EmptyForm"})
	encoded, err := BuildForm(api, attribute, identity)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{}}`, string(encoded))
	compiled, err := contract.Compile(encoded)
	require.NoError(t, err)
	assert.NoError(t, contract.Validate(compiled, []byte(`{}`)))
}

// Swapped JSON names must retain each field's constraints rather than overwrite
// another field's schema while the original properties map is being read.
func TestBuildFormSwappedJSONNames(t *testing.T) {
	attribute := &expr.AttributeExpr{Type: &expr.Object{
		{Name: "first", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {"second"}}}},
		{Name: "second", Attribute: &expr.AttributeExpr{Type: expr.Int, Meta: expr.MetaExpr{"struct:tag:json:name": {"first"}}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"first", "second"}}}
	api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
	encoded, err := BuildForm(api, attribute, expr.UserTypeExampleIdentity(&expr.UserTypeExpr{TypeName: "Swapped"}))
	require.NoError(t, err)
	compiled, err := contract.Compile(encoded)
	require.NoError(t, err)
	assert.NoError(t, contract.Validate(compiled, []byte(`{"second":"label","first":3}`)))
	assert.Error(t, contract.Validate(compiled, []byte(`{"second":3,"first":"label"}`)))
}
