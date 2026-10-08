// Package dsl attaches browser resources and caller permissions to MCP tools.
// Resource and ResourceReader still declare the service methods serving HTML;
// Apps declarations add the metadata hosts need to display and call those tools.
package dsl

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
)

// ToolUI links an MCP method's Tool to a ui:// HTML resource in the same server.
// Declare its contents with Resource or ResourceReader and the MIME type
// text/html;profile=mcp-app. The tool still returns meaningful ordinary content
// so a host without browser support can use its result.
func ToolUI(uri string) {
	tool, ok := eval.Current().(*mcpexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if uri == "" {
		eval.ReportError("ToolUI requires a nonempty ui:// resource URI")
		return
	}
	tool.UIResourceURI = uri
}

// ToolVisibility permits "model", "app", or both inside an MCP method's Tool.
// The default permits both. App-only tools stay out of generated model catalogs;
// hosts reject app calls to model-only tools before invoking their endpoints.
func ToolVisibility(callers ...string) {
	tool, ok := eval.Current().(*mcpexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	var model, app bool
	for _, caller := range callers {
		switch caller {
		case "model":
			if model {
				eval.ReportError("ToolVisibility must not repeat a caller")
				return
			}
			model = true
		case "app":
			if app {
				eval.ReportError("ToolVisibility must not repeat a caller")
				return
			}
			app = true
		default:
			eval.ReportError("ToolVisibility accepts only model and app")
			return
		}
	}
	switch {
	case model && app:
		tool.Visibility = mcpexpr.ModelAndAppVisibility
	case model:
		tool.Visibility = mcpexpr.ModelVisibility
	case app:
		tool.Visibility = mcpexpr.AppVisibility
	default:
		eval.ReportError("ToolVisibility requires at least one caller")
	}
}
