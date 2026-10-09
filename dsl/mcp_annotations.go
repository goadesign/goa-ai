// Package dsl records service-authored MCP tool hints in the design. Generated
// catalogs advertise the hints; clients decide whether to trust the server.
package dsl

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
)

// ToolTitle sets the MCP display name inside a method's Tool block.
func ToolTitle(value string) {
	if annotations := currentMCPAnnotations(); annotations != nil {
		annotations.Title = &value
	}
}

// ReadOnlyHint declares whether an MCP tool changes its environment. Use it
// inside a method's Tool block. Omission means the tool may change its environment.
func ReadOnlyHint(value bool) {
	if annotations := currentMCPAnnotations(); annotations != nil {
		annotations.ReadOnlyHint = &value
	}
}

// DestructiveHint declares whether an MCP tool may remove or replace data. Use
// it inside a method's Tool block. Omission means the tool may be destructive.
func DestructiveHint(value bool) {
	if annotations := currentMCPAnnotations(); annotations != nil {
		annotations.DestructiveHint = &value
	}
}

// IdempotentHint declares whether repeating the same MCP tool arguments has no
// additional effects. Use it inside a method's Tool block. Omission means repeats
// may have additional effects; the service implementation must enforce any promise.
func IdempotentHint(value bool) {
	if annotations := currentMCPAnnotations(); annotations != nil {
		annotations.IdempotentHint = &value
	}
}

// OpenWorldHint declares whether an MCP tool interacts with external entities.
// Use it inside a method's Tool block. Omission means external interaction is possible.
func OpenWorldHint(value bool) {
	if annotations := currentMCPAnnotations(); annotations != nil {
		annotations.OpenWorldHint = &value
	}
}

// currentMCPAnnotations accepts only an MCP method's Tool block and creates the
// optional annotation object when an author declares its first hint.
func currentMCPAnnotations() *mcpexpr.ToolAnnotationsExpr {
	tool, ok := eval.Current().(*mcpexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	if tool.Annotations == nil {
		tool.Annotations = &mcpexpr.ToolAnnotationsExpr{}
	}
	return tool.Annotations
}
