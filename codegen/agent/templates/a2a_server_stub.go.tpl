package {{ .Package }}

import (
	a2a "goa.design/goa-ai/runtime/a2a"
	agentruntime "goa.design/goa-ai/runtime/agent"
)

// NewA2AServer creates an A2A server for the {{ .AgentName }} agent.
func NewA2AServer(rt agentruntime.Client, baseURL string) (*a2a.Server, error) {
	return a2a.NewServer(rt, baseURL, {{ .AgentGoName }}ServerConfig)
}




