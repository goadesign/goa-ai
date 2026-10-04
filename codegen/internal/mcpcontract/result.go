// Package mcpcontract selects the Goa result fields that an MCP method may
// return. Catalogs and generated agent codecs use the same selected contract;
// a fixed view never reconstructs fields that Goa omitted from the response.
package mcpcontract

import (
	"fmt"

	"goa.design/goa/v3/expr"
)

// Result returns the fields selected by a fixed Goa view, retaining its field
// validation. Ordinary results and results with an execution-selected view keep
// their authored type; execution-selected views require a separate wire contract.
func Result(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	result, ok := method.Result.Type.(*expr.ResultTypeExpr)
	if !ok {
		return method.Result, nil
	}
	view, fixed := FixedView(method)
	if !fixed {
		return method.Result, nil
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
