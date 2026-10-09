// Package mcpcontract separates authored presentation and host metadata from
// structured results. Every model consumer receives the same remaining fields
// and view selection as its structured JSON contract.
package mcpcontract

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

// ToolResult returns the structured result advertised by one tool. The selected
// content and host metadata fields are absent from each selected view, so
// private values cannot enter the model through ordinary result JSON.
func ToolResult(tool *mcpexpr.ToolExpr) (*expr.AttributeExpr, error) {
	result, err := Result(tool.Method)
	if err != nil || tool.ContentField == "" && tool.MetadataField == "" {
		return result, err
	}
	if union := expr.AsUnion(result.Type); union != nil {
		selected := *union
		selected.Values = make([]*expr.NamedAttributeExpr, len(union.Values))
		for index, branch := range union.Values {
			value, err := StructuredResult(tool, branch.Attribute)
			if err != nil {
				return nil, err
			}
			selected.Values[index] = &expr.NamedAttributeExpr{Name: branch.Name, Attribute: value}
		}
		attribute := *result
		attribute.Type = &selected
		return &attribute, nil
	}
	selected, err := StructuredResult(tool, result)
	if err != nil {
		return nil, err
	}
	if len(*expr.AsObject(selected.Type)) == 0 {
		return &expr.AttributeExpr{Type: expr.Empty}, nil
	}
	return selected, nil
}

// StructuredResult removes the authored presentation and host metadata fields
// from one completed result or selected view. All model consumers use these
// same remaining fields, examples, constraints and original type locations.
func StructuredResult(tool *mcpexpr.ToolExpr, result *expr.AttributeExpr) (*expr.AttributeExpr, error) {
	selected := result
	for _, field := range []string{tool.ContentField, tool.MetadataField} {
		if field == "" {
			continue
		}
		var err error
		selected, err = WithoutField(selected, field)
		if err != nil {
			return nil, err
		}
	}
	return selected, nil
}
