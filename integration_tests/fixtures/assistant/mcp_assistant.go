package assistantapi

import (
	genassistant "example.com/assistant/gen/assistant"
	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// NewMcpAssistant returns the MCP service backed by configured Goa endpoints.
func NewMcpAssistant() mcpassistant.Service {
	return mcpassistant.NewMCPAdapter(genassistant.NewEndpoints(NewAssistant()), nil)
}
