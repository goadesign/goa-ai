// Package mcpcontract selects the Goa result fields that an MCP method may
// return. Catalogs and generated agent codecs use the same selected contract;
// a view never reconstructs fields that Goa omitted from the response.
package mcpcontract

import (
	"fmt"

	"goa.design/goa/v3/expr"
)

// Result returns the exact fields of a fixed Goa view. When the service chooses
// a view during execution, it returns a OneOf contract containing that view's
// name and selected fields. Ordinary results keep their authored contract.
func Result(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	result, ok := method.Result.Type.(*expr.ResultTypeExpr)
	if !ok {
		return method.Result, nil
	}
	if view, fixed := FixedView(method); fixed {
		return SelectView(method, view)
	}
	union := &expr.Union{TypeName: result.TypeName + "MCPViews"}
	for _, view := range result.Views {
		selected, err := SelectView(method, view.Name)
		if err != nil {
			return nil, err
		}
		union.Values = append(union.Values, &expr.NamedAttributeExpr{Name: view.Name, Attribute: selected})
	}
	return &expr.AttributeExpr{
		Type:        union,
		Description: "The view selected by the service and the fields returned through that view.",
	}, nil
}

// SelectView returns only fields and validation included in the named view.
// It retains nested type locations so generated codecs use the authored types.
func SelectView(method *expr.MethodExpr, view string) (*expr.AttributeExpr, error) {
	result, ok := method.Result.Type.(*expr.ResultTypeExpr)
	if !ok {
		return nil, fmt.Errorf("method %q does not return a Goa result view", method.Name)
	}
	projected, err := expr.Project(result, view)
	if err != nil {
		return nil, fmt.Errorf("select MCP result view for method %q: %w", method.Name, err)
	}
	attribute := expr.DupAtt(method.Result)
	attribute.Type = projected
	return attribute, nil
}

// FixedView resolves the view selected by the design. Multiple available views
// without a method selection leave the choice to the service during execution.
func FixedView(method *expr.MethodExpr) (string, bool) {
	result, ok := method.Result.Type.(*expr.ResultTypeExpr)
	if !ok {
		return "", false
	}
	if view, selected := method.Result.Meta.Last(expr.ViewMetaKey); selected {
		return view, true
	}
	if !result.HasMultipleViews() {
		return expr.DefaultView, true
	}
	return "", false
}
