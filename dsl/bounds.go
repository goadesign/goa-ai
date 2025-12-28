package dsl

import (
	"goa.design/goa/v3/eval"

	agentsexpr "goa.design/goa-ai/expr/agent"
)

// Cursor declares which optional String field on the tool payload carries the
// cursor for cursor-based pagination. Cursor must be used inside BoundedResult.
func Cursor(field string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if field == "" {
		eval.ReportError("Cursor field name cannot be empty")
		return
	}
	if tool.Paging == nil {
		tool.Paging = &agentsexpr.ToolPagingExpr{}
	}
	tool.Paging.CursorField = field
}

// NextCursor declares which optional String field on the tool result carries the
// cursor for the next page of cursor-based pagination. NextCursor must be used
// inside BoundedResult.
func NextCursor(field string) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if field == "" {
		eval.ReportError("NextCursor field name cannot be empty")
		return
	}
	if tool.Paging == nil {
		tool.Paging = &agentsexpr.ToolPagingExpr{}
	}
	tool.Paging.NextCursorField = field
}


