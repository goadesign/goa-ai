// These tests read inherited contracts before Goa finalizes the authored design.
// They check complete fields and constraints without changing the source graph.
package mcpinput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestResolvedKeepsOrdinaryAttributes(t *testing.T) {
	attribute := exchangeObject(expr.Object{{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "value")
	assert.Same(t, attribute, Resolved(attribute))
}

func TestResolvedReadsCompleteInheritanceGraph(t *testing.T) {
	for _, test := range []struct {
		name string
		wrap func(*expr.AttributeExpr) *expr.AttributeExpr
	}{
		{"root", func(a *expr.AttributeExpr) *expr.AttributeExpr { return a }},
		{"array", func(a *expr.AttributeExpr) *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: &expr.Array{ElemType: a, NonNullableElems: true}}
		}},
		{"map", func(a *expr.AttributeExpr) *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: a}}
		}},
		{"union", func(a *expr.AttributeExpr) *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: &expr.Union{TypeName: "Result", Values: []*expr.NamedAttributeExpr{{Name: "value", Attribute: a}}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:pkg:path": {"example.local/contracts"}}}
			base := &expr.UserTypeExpr{TypeName: "Base", AttributeExpr: exchangeObject(expr.Object{{Name: "value", Attribute: field}}, "value")}
			middle := &expr.UserTypeExpr{TypeName: "Middle", AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{}, Bases: []expr.DataType{base}}}
			derived := &expr.AttributeExpr{Type: &expr.Object{}, Bases: []expr.DataType{middle}}
			root := test.wrap(derived)
			resolved := Resolved(root)
			switch test.name {
			case "array":
				resolved = expr.AsArray(resolved.Type).ElemType
			case "map":
				resolved = expr.AsMap(resolved.Type).ElemType
			case "union":
				resolved = expr.AsUnion(resolved.Type).Values[0].Attribute
			}
			require.NotNil(t, resolved.Find("value"))
			assert.True(t, resolved.IsRequired("value"))
			assert.Equal(t, field.Meta, resolved.Find("value").Meta)
			assert.Empty(t, *expr.AsObject(derived.Type))
			assert.Len(t, derived.Bases, 1)
			assert.Empty(t, *expr.AsObject(middle.Type))
			assert.Len(t, middle.Bases, 1)
		})
	}
}

func TestResolvedAppliesReferenceRequiredFieldsWithoutChangingSources(t *testing.T) {
	base := &expr.UserTypeExpr{TypeName: "Reference", AttributeExpr: exchangeObject(expr.Object{{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Format: expr.FormatURI}}}}, "value")}
	field := &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Format: expr.FormatURI}}
	attribute := &expr.AttributeExpr{Type: &expr.Object{{Name: "value", Attribute: field}}, References: []expr.DataType{base}}
	resolved := Resolved(attribute)
	require.NotNil(t, resolved.Find("value"))
	assert.Equal(t, expr.String, resolved.Find("value").Type)
	assert.Equal(t, expr.ValidationFormat(expr.FormatURI), resolved.Find("value").Validation.Format)
	assert.True(t, resolved.IsRequired("value"))
	assert.Equal(t, expr.String, field.Type)
	assert.Equal(t, expr.ValidationFormat(expr.FormatURI), field.Validation.Format)
	assert.Nil(t, attribute.Validation)
}

func TestInputExchangeReadsInheritedShapesAndKeepsAuthoredFields(t *testing.T) {
	method := exchangeMethod()
	continuation := method.Payload.Find("continuation")
	pending := exchangePending(method)
	complete := expr.AsUnion(method.Result.Find("outcome").Type).Values[0].Attribute
	method.Payload = inheritedForTest("Payload", method.Payload)
	method.Result = inheritedForTest("Result", method.Result)
	responses := continuation.Find("responses")
	continuation.Type = &expr.Object{}
	continuation.Bases = []expr.DataType{&expr.UserTypeExpr{TypeName: "ContinuationFields", AttributeExpr: exchangeObject(expr.Object{{Name: "state", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "responses", Attribute: responses}})}}
	mapping, err := InputExchange(method)
	require.NoError(t, err)
	assert.Same(t, continuation, mapping.Continuation)
	assert.Same(t, pending, mapping.Pending)
	assert.Same(t, complete, mapping.Complete)
	assert.Len(t, mapping.Questions, 2)
	assert.Empty(t, *expr.AsObject(method.Payload.Type))
	assert.Empty(t, *expr.AsObject(method.Result.Type))
}

func TestTaskExchangeReadsInheritedRolesAndObservation(t *testing.T) {
	creator := taskCreatorForTest()
	observation := creator.Result.Type.(expr.UserType)
	metadata := creator.Result.Find("task")
	metadataBase := *metadata
	metadata.Type, metadata.Validation = &expr.Object{}, nil
	metadata.Bases = []expr.DataType{&expr.UserTypeExpr{TypeName: "MetadataFields", AttributeExpr: &metadataBase}}
	original := observation.Attribute()
	observation.SetAttribute(inheritedForTest("ObservationFields", original))
	for _, name := range []string{"read", "answer", "cancel"} {
		method := creator.Service.Method(name)
		method.Payload = inheritedForTest(name+"Input", method.Payload)
	}
	binding, err := TaskExchange(creator)
	require.NoError(t, err)
	assert.Same(t, creator.Result, binding.Observation)
	assert.Same(t, creator.Service.Method("read"), binding.Read)
	assert.Same(t, metadata, binding.Metadata)
	assert.Len(t, binding.Questions, 1)
	assert.Empty(t, *expr.AsObject(observation.Attribute().Type))
	assert.Empty(t, *expr.AsObject(metadata.Type))
}

func TestInputExchangeRejectsInvalidInheritedFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*expr.MethodExpr)
		want   string
	}{
		{"required continuation", func(m *expr.MethodExpr) {
			m.Payload.Validation.Required = append(m.Payload.Validation.Required, "continuation")
		}, "optional payload"},
		{"extra result", func(m *expr.MethodExpr) {
			*expr.AsObject(m.Result.Type) = append(*expr.AsObject(m.Result.Type), &expr.NamedAttributeExpr{Name: "extra", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}, "only required outcome"},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := exchangeMethod()
			test.change(method)
			method.Payload = inheritedForTest("Payload", method.Payload)
			method.Result = inheritedForTest("Result", method.Result)
			_, err := InputExchange(method)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestResolvedKeepsRecursiveTypeLinks(t *testing.T) {
	base := &expr.UserTypeExpr{TypeName: "Base", AttributeExpr: exchangeObject(expr.Object{{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "value")}
	node := &expr.UserTypeExpr{TypeName: "Node", AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{}, Bases: []expr.DataType{base}}}
	node.Type.(*expr.Object).Set("next", &expr.AttributeExpr{Type: node})
	resolved := Resolved(&expr.AttributeExpr{Type: node})
	assert.True(t, resolved.IsRequired("value"))
	assert.Same(t, resolved.Type, resolved.Find("next").Type)
	assert.Len(t, node.Bases, 1)
	assert.Len(t, *expr.AsObject(node.Type), 1)
}

func TestResolvedKeepsDifferentDeclarationsWithOneTypeOrigin(t *testing.T) {
	origin := &expr.UserTypeExpr{TypeName: "Shared", AttributeExpr: exchangeObject(expr.Object{})}
	first := origin.Dup(inheritedForTest("FirstFields", exchangeObject(expr.Object{{Name: "first", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "first")))
	second := origin.Dup(inheritedForTest("SecondFields", exchangeObject(expr.Object{{Name: "second", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "second")))
	resolved := Resolved(exchangeObject(expr.Object{{Name: "first", Attribute: &expr.AttributeExpr{Type: first}}, {Name: "second", Attribute: &expr.AttributeExpr{Type: second}}}))
	assert.NotSame(t, resolved.Find("first").Type, resolved.Find("second").Type)
	assert.True(t, resolved.Find("first").IsRequired("first"))
	assert.True(t, resolved.Find("second").IsRequired("second"))
	assert.Nil(t, resolved.Find("first").Find("second"))
	assert.Nil(t, resolved.Find("second").Find("first"))
}

func inheritedForTest(name string, attribute *expr.AttributeExpr) *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{}, Bases: []expr.DataType{&expr.UserTypeExpr{TypeName: name, AttributeExpr: attribute}}}
}
