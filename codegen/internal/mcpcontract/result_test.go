// These tests advertise only a completed operation's selected fields. Native
// service outcomes and private continuation data remain outside that contract.
package mcpcontract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/expr"
)

func TestResultSelectsCompletedBranchView(t *testing.T) {
	receipt := &expr.ResultTypeExpr{UserTypeExpr: &expr.UserTypeExpr{
		TypeName: "Receipt", AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "reference", Attribute: &expr.AttributeExpr{Type: expr.String}},
				{Name: "internal", Attribute: &expr.AttributeExpr{Type: expr.String}},
			}, Validation: &expr.ValidationExpr{Required: []string{"reference", "internal"}},
		},
	}, Identifier: "application/vnd.exchange.receipt"}
	receipt.Views = []*expr.ViewExpr{
		{Name: "public", Parent: receipt, AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{{Name: "reference", Attribute: &expr.AttributeExpr{Type: expr.String}}}}},
		{Name: "default", Parent: receipt, AttributeExpr: expr.DupAtt(receipt.AttributeExpr)},
	}
	complete := &expr.AttributeExpr{Type: receipt, Meta: expr.MetaExpr{expr.ViewMetaKey: {"public"}}}
	state := &expr.AttributeExpr{Type: &expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}}}
	outcome := &expr.AttributeExpr{Type: &expr.Union{TypeName: "Outcome", Values: []*expr.NamedAttributeExpr{
		{Name: "complete", Attribute: complete},
		{Name: "input_required", Attribute: state},
	}}}
	method := &expr.MethodExpr{Name: "read", Meta: expr.MetaExpr{mcpinput.ExchangeMetaKey: {"continuation", "outcome"}},
		Payload: &expr.AttributeExpr{Type: &expr.Object{{Name: "continuation", Attribute: expr.DupAtt(state)}}},
		Result:  &expr.AttributeExpr{Type: &expr.Object{{Name: "outcome", Attribute: outcome}}, Validation: &expr.ValidationExpr{Required: []string{"outcome"}}},
	}
	selected, err := Result(method)
	require.NoError(t, err)
	assert.NotNil(t, selected.Find("reference"))
	assert.Nil(t, selected.Find("internal"))
	assert.Nil(t, selected.Find("outcome"))
	assert.Equal(t, []string{"reference"}, selected.AllRequired())
	assert.NotNil(t, receipt.Find("internal"))
	assert.Same(t, outcome, method.Result.Find("outcome"))

	t.Run("nested default stays fixed", func(t *testing.T) {
		copied := *method
		copied.Result = expr.DupAtt(method.Result)
		branch := expr.AsUnion(copied.Result.Find("outcome").Type).Values[0]
		branch.Attribute.Meta = nil
		selected, err := Result(&copied)
		require.NoError(t, err)
		assert.Nil(t, expr.AsUnion(selected.Type))
		assert.NotNil(t, selected.Find("reference"))
		assert.NotNil(t, selected.Find("internal"))
	})

	t.Run("service-selected outer views", func(t *testing.T) {
		copied := *method
		envelope := &expr.ResultTypeExpr{UserTypeExpr: &expr.UserTypeExpr{
			TypeName: "Envelope", AttributeExpr: expr.DupAtt(method.Result),
		}, Identifier: "application/vnd.exchange.envelope"}
		for _, name := range []string{"default", "other"} {
			envelope.Views = append(envelope.Views, &expr.ViewExpr{
				Name: name, Parent: envelope, AttributeExpr: expr.DupAtt(envelope.AttributeExpr),
			})
		}
		copied.Result = &expr.AttributeExpr{Type: envelope}
		selected, err := Result(&copied)
		require.NoError(t, err)
		choices := expr.AsUnion(selected.Type)
		require.NotNil(t, choices)
		require.Len(t, choices.Values, 2)
		for index, name := range []string{"default", "other"} {
			assert.Equal(t, name, choices.Values[index].Name)
			assert.NotNil(t, choices.Values[index].Attribute.Find("reference"))
			assert.Nil(t, choices.Values[index].Attribute.Find("internal"))
			assert.Nil(t, choices.Values[index].Attribute.Find("outcome"))
		}
	})
}
