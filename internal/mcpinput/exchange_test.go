// These tests treat authored Goa attributes as the design boundary. They prove
// that input requests and typed answers retain their identity and that lossy
// or ambiguous declarations fail before any generated application can run.
package mcpinput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestInputExchangePreservesAuthoredTypes(t *testing.T) {
	method := exchangeMethod()
	mapping, err := InputExchange(method)
	require.NoError(t, err)
	require.Len(t, mapping.Questions, 2)
	assert.Equal(t, "traveler", mapping.Questions[0].Name)
	assert.Equal(t, "payment", mapping.Questions[1].Name)
	assert.NotNil(t, mapping.Questions[0].Content)
	assert.Nil(t, mapping.Questions[1].Content)
	assert.Same(t, method.Payload.Find("continuation"), mapping.Continuation)
	choice := expr.AsUnion(method.Result.Find("outcome").Type)
	assert.Same(t, choice.Values[0].Attribute, mapping.Complete)
	assert.Same(t, choice.Values[1].Attribute, mapping.Pending)
	complete, err := CompleteResult(method)
	require.NoError(t, err)
	assert.Same(t, mapping.Complete, complete)
	assert.Equal(t, "example.local/contracts", expr.AsObject(mapping.Questions[0].Content.Type).Attribute("name").Meta["struct:pkg:path"][0])
}

func TestInputExchangeAllowsStateOnlyAndIndependentCalls(t *testing.T) {
	method := exchangeMethod()
	pending := expr.AsUnion(method.Result.Find("outcome").Type).Values[1].Attribute
	method.Payload.Find("continuation").Type = &expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}}
	pending.Type = &expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}}
	mapping, err := InputExchange(method)
	require.NoError(t, err)
	assert.Empty(t, mapping.Questions)
	assert.Nil(t, mapping.Requests)
	assert.Nil(t, mapping.Responses)
	ordinary := &expr.MethodExpr{Result: &expr.AttributeExpr{Type: expr.String}}
	mapping, err = InputExchange(ordinary)
	require.NoError(t, err)
	assert.Nil(t, mapping)
	complete, err := CompleteResult(ordinary)
	require.NoError(t, err)
	assert.Same(t, ordinary.Result, complete)
}

func TestInputExchangeRejectsInvalidAuthoring(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*expr.MethodExpr)
		want   string
	}{
		{"missing field names", func(m *expr.MethodExpr) { m.Meta[ExchangeMetaKey] = nil }, "field names"},
		{"required continuation", func(m *expr.MethodExpr) {
			m.Payload.Validation.Required = append(m.Payload.Validation.Required, "continuation")
		}, "optional payload"},
		{"unknown continuation field", func(m *expr.MethodExpr) {
			*expr.AsObject(m.Payload.Find("continuation").Type) = append(*expr.AsObject(m.Payload.Find("continuation").Type), &expr.NamedAttributeExpr{Name: "session", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}, "unknown field"},
		{"unreturned result field", func(m *expr.MethodExpr) {
			*expr.AsObject(m.Result.Type) = append(*expr.AsObject(m.Result.Type), &expr.NamedAttributeExpr{Name: "lost", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}, "only required outcome"},
		{"unknown outcome", func(m *expr.MethodExpr) { expr.AsUnion(m.Result.Find("outcome").Type).Values[1].Name = "later" }, "unknown branch"},
		{"state cannot be echoed", func(m *expr.MethodExpr) {
			*expr.AsObject(m.Payload.Find("continuation").Type) = (*expr.AsObject(m.Payload.Find("continuation").Type))[1:]
		}, "both pending"},
		{"missing answer identifier", func(m *expr.MethodExpr) {
			expr.AsObject(m.Payload.Find("continuation").Find("responses").Type).Attribute("traveler").Type = expr.String
		}, "answer OneOf"},
		{"mismatched identifiers", func(m *expr.MethodExpr) {
			(*expr.AsObject(m.Payload.Find("continuation").Find("responses").Type))[0].Name = "other"
		}, "matching request"},
		{"required question", func(m *expr.MethodExpr) {
			exchangePending(m).Find("requests").Validation = &expr.ValidationExpr{Required: []string{"traveler"}}
		}, "optional matching"},
		{"empty message declaration", func(m *expr.MethodExpr) { exchangePending(m).Find("requests").Find("traveler").Validation = nil }, "message string"},
		{"unexpected request data", func(m *expr.MethodExpr) {
			*expr.AsObject(exchangePending(m).Find("requests").Find("traveler").Type) = append(*expr.AsObject(exchangePending(m).Find("requests").Find("traveler").Type), &expr.NamedAttributeExpr{Name: "secret", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}, "only required message"},
		{"URL without format", func(m *expr.MethodExpr) {
			exchangePending(m).Find("requests").Find("payment").Find("url").Validation = nil
		}, "FormatURI"},
		{"unknown action", func(m *expr.MethodExpr) { exchangeAnswer(m, "traveler").Values[2].Name = "dismiss" }, "unknown action"},
		{"decline with data", func(m *expr.MethodExpr) {
			exchangeAnswer(m, "traveler").Values[1].Attribute.Type = &expr.Object{{Name: "content", Attribute: &expr.AttributeExpr{Type: expr.String}}}
		}, "decline"},
		{"URL with data", func(m *expr.MethodExpr) {
			exchangeAnswer(m, "payment").Values[0].Attribute.Type = &expr.Object{{Name: "content", Attribute: &expr.AttributeExpr{Type: expr.String}}}
		}, "cannot contain form data"},
		{"nested form", func(m *expr.MethodExpr) {
			exchangeAnswer(m, "traveler").Values[0].Attribute.Find("content").Type = &expr.Object{{Name: "nested", Attribute: &expr.AttributeExpr{Type: &expr.Object{}}}}
		}, "primitive"},
		{"nullable selections", func(m *expr.MethodExpr) {
			exchangeAnswer(m, "traveler").Values[0].Attribute.Find("content").Find("preferences").Type.(*expr.Array).NonNullableElems = false
		}, "non-null string-selection"},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := exchangeMethod()
			test.change(method)
			_, err := InputExchange(method)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// exchangeMethod supplies two independent typed questions and an opaque state.
// The located field proves that resolution keeps the original authored graph.
func exchangeMethod() *expr.MethodExpr {
	traveler := exchangeObject(expr.Object{
		{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:pkg:path": []string{"example.local/contracts"}}}},
		{Name: "preferences", Attribute: &expr.AttributeExpr{Type: &expr.Array{NonNullableElems: true, ElemType: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Values: []any{"aisle", "window"}}}}}},
	}, "name")
	responses := exchangeObject(expr.Object{
		{Name: "traveler", Attribute: exchangeResponse(traveler)},
		{Name: "payment", Attribute: exchangeResponse(nil)},
	})
	requests := exchangeObject(expr.Object{
		{Name: "traveler", Attribute: exchangeObject(expr.Object{{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "message")},
		{Name: "payment", Attribute: exchangeObject(expr.Object{{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "url", Attribute: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Format: expr.FormatURI}}}}, "message", "url")},
	})
	complete := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "Receipt", AttributeExpr: exchangeObject(expr.Object{{Name: "id", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "id")}}
	pending := exchangeObject(expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "requests", Attribute: requests}})
	return &expr.MethodExpr{
		Meta:    expr.MetaExpr{ExchangeMetaKey: []string{"continuation", "outcome"}},
		Payload: exchangeObject(expr.Object{{Name: "destination", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "continuation", Attribute: exchangeObject(expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "responses", Attribute: responses}})}}, "destination"),
		Result:  exchangeObject(expr.Object{{Name: "outcome", Attribute: &expr.AttributeExpr{Type: &expr.Union{TypeName: "BookingOutcome", Values: []*expr.NamedAttributeExpr{{Name: "complete", Attribute: complete}, {Name: "input_required", Attribute: pending}}}}}}, "outcome"),
	}
}

func exchangeObject(fields expr.Object, required ...string) *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &fields, Validation: &expr.ValidationExpr{Required: required}}
}

func exchangeResponse(content *expr.AttributeExpr) *expr.AttributeExpr {
	accepted := exchangeObject(expr.Object{})
	if content != nil {
		accepted = exchangeObject(expr.Object{{Name: "content", Attribute: content}}, "content")
	}
	return exchangeObject(expr.Object{{Name: "answer", Attribute: &expr.AttributeExpr{Type: &expr.Union{TypeName: "InputAnswer", Values: []*expr.NamedAttributeExpr{{Name: "accept", Attribute: accepted}, {Name: "decline", Attribute: exchangeObject(expr.Object{})}, {Name: "cancel", Attribute: exchangeObject(expr.Object{})}}}}}}, "answer")
}

func exchangePending(method *expr.MethodExpr) *expr.AttributeExpr {
	return expr.AsUnion(method.Result.Find("outcome").Type).Values[1].Attribute
}

func exchangeAnswer(method *expr.MethodExpr, name string) *expr.Union {
	return expr.AsUnion(method.Payload.Find("continuation").Find("responses").Find(name).Find("answer").Type)
}
