package dsl

import (
	"goa.design/goa/v3/eval"

	agentsexpr "goa.design/goa-ai/expr/agent"
)

// Cursor declares which optional String field on the tool payload carries the
// cursor for cursor-based pagination. Cursor must be used inside BoundedResult.
func Cursor(field string) {
	bounds, ok := eval.Current().(*agentsexpr.ToolBoundsExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if field == "" {
		eval.ReportError("Cursor field name cannot be empty")
		return
	}
	if bounds.Paging == nil {
		bounds.Paging = &agentsexpr.ToolPagingExpr{}
	}
	bounds.Paging.CursorField = field
}

// NextCursor declares the canonical field name for the next-page reference in
// the bounded paging contract. Providers return their cursor through
// planner.ToolResult.Bounds.NextCursor; the runtime retains that cursor and
// projects a run-, session-, and tool-bound reference into model-visible JSON.
// NextCursor must be used inside BoundedResult.
func NextCursor(field string) {
	bounds, ok := eval.Current().(*agentsexpr.ToolBoundsExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if field == "" {
		eval.ReportError("NextCursor field name cannot be empty")
		return
	}
	if bounds.Paging == nil {
		bounds.Paging = &agentsexpr.ToolPagingExpr{}
	}
	bounds.Paging.NextCursorField = field
}
