// These tests check that selecting fields for a recursive result view keeps
// recursive references inside that selection and leaves Goa's source type intact.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa/v3/expr"
)

func TestSelectResultFieldsPreservesRecursiveView(t *testing.T) {
	sourceType := &expr.UserTypeExpr{TypeName: "RecordView", UID: "source-record-view", AttributeExpr: &expr.AttributeExpr{}}
	sourceFields := &expr.Object{
		{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "secret", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "next", Attribute: &expr.AttributeExpr{Type: sourceType}},
	}
	sourceType.Type = sourceFields
	contractType := &expr.UserTypeExpr{TypeName: "SelectedRecord", UID: "selected-record-view", AttributeExpr: &expr.AttributeExpr{
		Validation: &expr.ValidationExpr{Required: []string{"name"}},
	}}
	contractType.Type = &expr.Object{
		{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "next", Attribute: &expr.AttributeExpr{Type: contractType}},
	}
	source := &expr.AttributeExpr{Type: sourceType}
	require.NoError(t, selectResultFields(source, &expr.AttributeExpr{Type: contractType}, make(map[resultTypePair]expr.DataType)))
	selected := source.Type.(expr.UserType)
	fields := expr.AsObject(selected.Attribute().Type)
	assert.Nil(t, fields.Attribute("secret"))
	assert.Same(t, selected, fields.Attribute("next").Type)
	assert.Equal(t, []string{"name"}, selected.Attribute().Validation.Required)
	assert.Same(t, sourceType, selected.Origin())
	assert.NotNil(t, sourceFields.Attribute("secret"))
	assert.Same(t, sourceType, sourceFields.Attribute("next").Type)
}
