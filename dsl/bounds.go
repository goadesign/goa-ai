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

// ContinueWith declares a sibling tool that accepts the cursor for the next
// page. It separates the initial semantic query from cursor-only continuation.
// ContinueWith must be used inside BoundedResult.
func ContinueWith(tool, cursorField string) {
	bounds, ok := eval.Current().(*agentsexpr.ToolBoundsExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if tool == "" {
		eval.ReportError("continuation tool name cannot be empty")
		return
	}
	if cursorField == "" {
		eval.ReportError("continuation cursor field name cannot be empty")
		return
	}
	if bounds.Paging == nil {
		bounds.Paging = &agentsexpr.ToolPagingExpr{}
	}
	bounds.Paging.ContinueTool = tool
	bounds.Paging.CursorField = cursorField
}

// NextCursor declares the canonical field name for the next-page cursor in the
// bounded paging contract. Providers return the actual cursor through
// planner.ToolResult.Bounds.NextCursor; codegen and runtimes then project that
// value into the model-visible result JSON using this field name. NextCursor
// must be used inside BoundedResult.
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
