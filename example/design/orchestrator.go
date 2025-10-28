package design

import (
	. "goa.design/goa-ai/agents/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("orchestrator", func() {
	Description("Agent service consuming the assistant MCP toolset")

	JSONRPC(func() {
		POST("/orchestrator")
	})

	Agent("chat", "Chat orchestrator", func() {
		Uses(func() {
			UseMCPToolset("assistant", "assistant-mcp")
		})
	})

	Method("run", func() {
		Description("Invoke the chat agent")
		Payload(AgentRunPayload)
		StreamingResult(AgentRunChunk)
		JSONRPC(func() {
			ServerSentEvents(func() {})
		})
	})
})
