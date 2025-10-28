package design

import (
	agentsdsl "goa.design/goa-ai/agents/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("orchestrator", func() {
	Description("Agent service consuming the assistant MCP toolset")

	HTTP(func() {
		Path("/orchestrator")
	})

	agentsdsl.Agent("chat", "Chat orchestrator", func() {
		agentsdsl.Uses(func() {
			agentsdsl.UseMCPToolset("assistant", "assistant-mcp")
		})
	})

	agentsdsl.ExposeRun("chat", false)
})
