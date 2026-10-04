// Package dsl binds a Goa method's typed content array to the MCP response.
// The marked field supplies presentation content; the remaining result fields
// supply the tool's structured JSON contract.
package dsl

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
)

// ToolContent selects a top-level method result field inside its MCP Tool block.
// The field must use ArrayOfRequired with an object containing one required
// content OneOf. Its branches are text, image, audio, resource_link or resource. Binary data uses Bytes. The generated
// adapter validates the returned content and writes it to MCP content, excluding
// that field from output schemas and structuredContent. Goa views control which
// fields are returned; a view omitting this field returns no content.
//
// An optional array may be empty. A required array must declare MinLength(1).
// Other result fields keep their existing names, types and validation. If there
// are no other fields in a fixed result, the tool returns content alone.
func ToolContent(field string) {
	tool, ok := eval.Current().(*mcpexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if field == "" {
		eval.ReportError("ToolContent requires a non-empty result field name")
		return
	}
	if tool.ContentField != "" {
		eval.ReportError("ToolContent may be declared only once for a tool")
		return
	}
	tool.ContentField = field
}
