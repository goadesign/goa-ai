// Package mcpcontract selects fields without changing authored service types.
// Tool content and extension metadata retain type locations, examples and
// required-field rules for the fields copied by ordinary Goa transforms.
package mcpcontract

import (
	"fmt"
	"maps"
	"slices"

	"goa.design/goa/v3/expr"
)

// WithoutField retains authored type locations and constraints while excluding
// one named field. Tool content and encoded extension metadata use this same
// projection without changing the service or its selected view.
func WithoutField(result *expr.AttributeExpr, field string) (*expr.AttributeExpr, error) {
	selected := *result
	if named, ok := result.Type.(expr.UserType); ok {
		attribute, err := WithoutField(named.Attribute(), field)
		if err != nil {
			return nil, err
		}
		selected.Type = named.Dup(attribute)
		if resultType, ok := selected.Type.(*expr.ResultTypeExpr); ok {
			resultType.Views = []*expr.ViewExpr{{Name: expr.DefaultView, Parent: resultType, AttributeExpr: expr.DupAtt(attribute)}}
		}
	} else {
		object := expr.AsObject(result.Type)
		if object == nil {
			return nil, fmt.Errorf("field projection requires an object value")
		}
		fields := make(expr.Object, 0, len(*object))
		for _, attribute := range *object {
			if attribute.Name != field {
				fields = append(fields, attribute)
			}
		}
		selected.Type = &fields
	}
	selected.UserExamples = make([]*expr.ExampleExpr, len(result.UserExamples))
	for index, example := range result.UserExamples {
		projected := *example
		switch value := example.Value.(type) {
		case expr.Val:
			object := maps.Clone(value)
			delete(object, field)
			projected.Value = object
		case map[string]any:
			object := maps.Clone(value)
			delete(object, field)
			projected.Value = object
		default:
			return nil, fmt.Errorf("field projection example must be an object, got %T", example.Value)
		}
		selected.UserExamples[index] = &projected
	}
	if selected.Validation != nil {
		selected.Validation = selected.Validation.Dup()
		selected.Validation.Required = slices.DeleteFunc(selected.Validation.Required, func(name string) bool { return name == field })
	}
	return &selected, nil
}
