// These tests keep schema and strict JSON checks aligned with Goa's authored
// field tags. Internal attribute names remain available for typed transforms.
package jsonshape

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestFieldNameUsesGoaTagPrecedence(t *testing.T) {
	for _, test := range []struct {
		name    string
		meta    expr.MetaExpr
		wire    string
		visible bool
	}{
		{"untagged", nil, "attribute", true},
		{"name only", expr.MetaExpr{"struct:tag:json:name": {"wire"}}, "wire", true},
		{"full tag", expr.MetaExpr{"struct:tag:json": {"wire,omitempty"}}, "wire", true},
		{"full override", expr.MetaExpr{"struct:tag:json": {"wire,omitempty"}, "struct:tag:json:name": {"other"}}, "wire", true},
		{"default name", expr.MetaExpr{"struct:tag:json": {",omitempty"}}, "attribute", true},
		{"hidden", expr.MetaExpr{"struct:tag:json": {"-"}}, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := &expr.NamedAttributeExpr{Name: "attribute", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: test.meta}}
			name, visible := FieldName(field)
			assert.Equal(t, test.wire, name)
			assert.Equal(t, test.visible, visible)
			node, err := Build(&expr.AttributeExpr{Type: &expr.Object{field}})
			require.NoError(t, err)
			if test.visible {
				require.Len(t, node.Fields, 1)
				assert.Equal(t, test.wire, node.Fields[0].Name)
			} else {
				assert.Empty(t, node.Fields)
			}
			assert.Equal(t, "attribute", field.Name)
		})
	}
}

func TestBuildRejectsDuplicateJSONNames(t *testing.T) {
	_, err := Build(&expr.AttributeExpr{Type: &expr.Object{
		{Name: "first", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {"same"}}}},
		{Name: "second", Attribute: &expr.AttributeExpr{Type: expr.Int, Meta: expr.MetaExpr{"struct:tag:json:name": {"same"}}}},
	}})
	assert.ErrorContains(t, err, `share the wire name "same"`)
}
