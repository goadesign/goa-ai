package assistantapi

import (
	genassistant "example.com/assistant/gen/assistant"
	genmcpassistant "example.com/assistant/gen/mcp_assistant"
)

// NewMcpAssistant returns the MCP service backed by the user service.
func NewMcpAssistant() genmcpassistant.Service {
	return genmcpassistant.NewMCPAdapter(genassistant.NewEndpoints(NewAssistant()), nil)
}
