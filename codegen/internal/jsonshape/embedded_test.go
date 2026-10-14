// These tests check which authored maps have one unambiguous JSON object shape.
package jsonshape

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestEmbeddedMapShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*expr.AttributeExpr)
		want   string
	}{
		{name: "optional string values"},
		{name: "unsafe case matching", change: func(a *expr.AttributeExpr) { a.Find("message").Meta = nil }, want: "explicit JSON name"},
		{name: "empty strict name", change: func(a *expr.AttributeExpr) { a.Find("message").Meta["struct:tag:json"] = []string{",case:strict"} }, want: "explicit JSON name"},
		{name: "ignored sibling", change: func(a *expr.AttributeExpr) { a.Find("message").Meta["struct:tag:json"] = []string{"-"} }},
		{name: "dash with options", change: func(a *expr.AttributeExpr) { a.Find("message").Meta["struct:tag:json"] = []string{"-,case:strict"} }, want: "literal JSON name"},
		{name: "conflicting case matching", change: func(a *expr.AttributeExpr) {
			a.Find("message").Meta["struct:tag:json"] = []string{"message,case:strict,case:ignore"}
		}, want: "requires case:strict"},
		{name: "required", change: func(a *expr.AttributeExpr) { a.Validation = &expr.ValidationExpr{Required: []string{"extras"}} }, want: "cannot be required"},
		{name: "minimum", change: func(a *expr.AttributeExpr) { n := 1; a.Find("extras").Validation = &expr.ValidationExpr{MinLength: &n} }, want: "length bounds"},
		{name: "maximum", change: func(a *expr.AttributeExpr) { n := 2; a.Find("extras").Validation = &expr.ValidationExpr{MaxLength: &n} }, want: "length bounds"},
		{name: "multiple", change: func(a *expr.AttributeExpr) {
			o := expr.AsObject(a.Type)
			*o = append(*o, &expr.NamedAttributeExpr{Name: "other", Attribute: expr.DupAtt(a.Find("extras"))})
		}, want: "multiple embedded maps"},
		{name: "integer key", change: func(a *expr.AttributeExpr) { expr.AsMap(a.Find("extras").Type).KeyType.Type = expr.Int }, want: "string keys"},
		{name: "object field", change: func(a *expr.AttributeExpr) { a.Find("extras").Type = &expr.Object{} }, want: "string-key map"},
		{name: "extra option", change: func(a *expr.AttributeExpr) { a.Find("extras").Meta["struct:tag:json"] = []string{",embed,omitempty"} }, want: "only the embed option"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := &expr.AttributeExpr{Type: &expr.Object{
				{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:tag:json": {"message,case:strict"}}}},
				{Name: "extras", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.String}}, Meta: expr.MetaExpr{"struct:tag:json": {",embed"}}}},
			}}
			if test.change != nil {
				test.change(a)
			}
			shape, err := Build(a)
			if test.want != "" {
				assert.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			if test.name == "ignored sibling" {
				require.Empty(t, shape.Fields)
				return
			}
			require.Len(t, shape.Fields, 1)
			assert.Equal(t, "message", shape.Fields[0].Name)
			require.NotNil(t, shape.Element)
			assert.Equal(t, expr.String, shape.Element.Primitive)
		})
	}
}

func TestNamedEmbedTagKeepsNamedMap(t *testing.T) {
	a := &expr.AttributeExpr{Type: &expr.Object{{Name: "extras", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.String}}, Meta: expr.MetaExpr{"struct:tag:json": {"named,embed"}}}}}}
	shape, err := Build(a)
	require.NoError(t, err)
	require.Len(t, shape.Fields, 1)
	assert.Equal(t, "named", shape.Fields[0].Name)
	assert.Nil(t, shape.Element)
}
