// Package mcpcontract separates authored tool content from structured results.
// One field selected by the design becomes MCP content; every consumer receives
// the same remaining fields and view selection as its structured JSON contract.
package mcpcontract

import (
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
			value, err := WithoutField(branch.Attribute, tool.ContentField)
			if err != nil {
				return nil, err
			}
			selected.Values[index] = &expr.NamedAttributeExpr{Name: branch.Name, Attribute: value}
		}
		attribute := *result
		attribute.Type = &selected
		return &attribute, nil
	}
	selected, err := WithoutField(result, tool.ContentField)
	if err != nil {
		return nil, err
	}
	if len(*expr.AsObject(selected.Type)) == 0 {
		return &expr.AttributeExpr{Type: expr.Empty}, nil
	}
	return selected, nil
}
