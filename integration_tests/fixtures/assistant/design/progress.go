// Package design declares a unary progress producer for independent protocol
// checks. The service reports work through its context; generated transports
// own the client's token and notification framing.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// refereeProgress binds the ordinary service method to the frozen referee tool.
func refereeProgress() {
	Method("report_work", func() {
		Description("Perform synthetic work and report completion before returning its result")
		Result(String)
		Tool("test_tool_with_progress", "Perform synthetic work with progress updates")
	})
}
