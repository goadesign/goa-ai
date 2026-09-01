// This file verifies that Goa types become the JSON Schema shape used on the
// wire. The tests cover shapes whose JSON representation is not a direct copy
// of their Go type.
package shared

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestToJSONSchemaUnionDefaultKeys(t *testing.T) {
	union := &expr.Union{
		TypeName: "Choice",
		Values: []*expr.NamedAttributeExpr{
			{
				Name:      "name",
				Attribute: &expr.AttributeExpr{Type: expr.String},
			},
			{
				Name:      "empty",
				Attribute: &expr.AttributeExpr{Type: expr.Empty},
			},
		},
	}

	actual, err := ToJSONSchema(&expr.AttributeExpr{Type: union})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"type": "object",
		"anyOf": [
			{
				"type": "object",
				"additionalProperties": false,
				"required": ["type", "value"],
				"properties": {
					"type": {"type": "string", "enum": ["name"]},
					"value": {"type": "string"}
				}
			},
			{
				"type": "object",
				"additionalProperties": false,
				"required": ["type", "value"],
				"properties": {
					"type": {"type": "string", "enum": ["empty"]},
					"value": {
						"type": "object",
						"description": "Empty represents empty values",
						"additionalProperties": false
					}
				}
			}
		]
	}`, actual)
}

func TestToJSONSchemaUnionCustomKeysAndAnyValue(t *testing.T) {
	union := &expr.Union{
		TypeName: "Choice",
		TypeKey:  "kind",
		ValueKey: "payload",
		Values: []*expr.NamedAttributeExpr{
			{
				Name:      "count",
				Attribute: &expr.AttributeExpr{Type: expr.Int},
			},
			{
				Name:      "anything",
				Attribute: &expr.AttributeExpr{Type: expr.Any},
			},
		},
	}

	actual, err := ToJSONSchema(&expr.AttributeExpr{Type: union})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"type": "object",
		"anyOf": [
			{
				"type": "object",
				"additionalProperties": false,
				"required": ["kind", "payload"],
				"properties": {
					"kind": {"type": "string", "enum": ["count"]},
					"payload": {"type": "integer"}
				}
			},
			{
				"type": "object",
				"additionalProperties": false,
				"required": ["kind", "payload"],
				"properties": {
					"kind": {"type": "string", "enum": ["anything"]},
					"payload": {}
				}
			}
		]
	}`, actual)
}
