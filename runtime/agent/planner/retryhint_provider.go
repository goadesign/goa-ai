package planner

import "goa.design/goa-ai/runtime/agent/tools"

// RetryHintProvider can be implemented by domain-specific errors that want
// to surface structured retry guidance to the runtime. Service executors
// detect this interface and attach the provided RetryHint to ToolResult
// so policies and UIs can react without relying on string parsing.
type RetryHintProvider interface {
	RetryHint(tool tools.Ident) *RetryHint
}



