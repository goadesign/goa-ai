// Package planner defines the contracts between planners and the runtime.
package planner

import "goa.design/goa-ai/runtime/agent/tools"

// ToolFailureProvider can be implemented by domain-specific errors that own a
// structured failure classification and recovery contract. Service executors
// attach the returned failure to ToolResult without parsing error strings.
type ToolFailureProvider interface {
	ToolFailure(tool tools.Ident) *ToolFailure
}
