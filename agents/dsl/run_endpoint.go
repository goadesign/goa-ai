package dsl

import (
	. "goa.design/goa/v3/dsl"
)

// ExposeRun adds a conventional Run method to the current service for the given agent.
func ExposeRun(agentName string, streaming bool) {
	Method("run", func() {
		Description("Invoke the " + agentName + " agent")
		Payload(AgentRunPayload)
		Result(AgentRunResult)
		if streaming {
			StreamingResult(AgentRunChunk)
		}
		HTTP(func() {
			POST("/agents/" + agentName + "/run")
			Response(StatusOK)
		})
		JSONRPC(func() {})
	})
}
