// Package mcpcontract separates authored tool content from structured results.
// One field selected by the design becomes MCP content; every consumer receives
// the same remaining fields and view selection as its structured JSON contract.
package mcpcontract

import (
	"fmt"
	"maps"
	"slices"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

// ToolResult returns the structured result advertised by one tool. The selected
// content field is absent from each selected view, so presentation metadata and
// user-only content cannot enter the model through ordinary result JSON.
func ToolResult(tool *mcpexpr.ToolExpr) (*expr.AttributeExpr, error) {
	result, err := Result(tool.Method)
	if err != nil || tool.ContentField == "" {
		return result, err
	}
	if union := expr.AsUnion(result.Type); union != nil {
		selected := *union
		selected.Values = make([]*expr.NamedAttributeExpr, len(union.Values))
		for index, branch := range union.Values {
			value, err := WithoutContent(branch.Attribute, tool.ContentField)
			if err != nil {
				return nil, err
			}
			selected.Values[index] = &expr.NamedAttributeExpr{Name: branch.Name, Attribute: value}
		}
		attribute := *result
		attribute.Type = &selected
		return &attribute, nil
	}
	selected, err := WithoutContent(result, tool.ContentField)
	if err != nil {
		return nil, err
	}
	if len(*expr.AsObject(selected.Type)) == 0 {
		return &expr.AttributeExpr{Type: expr.Empty}, nil
	}
	return selected, nil
}

// WithoutContent retains authored type locations and constraints while excluding
// one presentation field. It never mutates the service or its selected view.
func WithoutContent(result *expr.AttributeExpr, field string) (*expr.AttributeExpr, error) {
	selected := *result
	if named, ok := result.Type.(expr.UserType); ok {
		attribute, err := WithoutContent(named.Attribute(), field)
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
			return nil, fmt.Errorf("ToolContent requires an object result")
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
			return nil, fmt.Errorf("ToolContent result example must be an object, got %T", example.Value)
		}
		selected.UserExamples[index] = &projected
	}
	if selected.Validation != nil {
		selected.Validation = selected.Validation.Dup()
		selected.Validation.Required = slices.DeleteFunc(selected.Validation.Required, func(name string) bool { return name == field })
	}
	return &selected, nil
}
