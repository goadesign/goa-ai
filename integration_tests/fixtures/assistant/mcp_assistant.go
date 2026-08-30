package assistantapi

import (
	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// NewMcpAssistant returns the MCP service backed by the user service.
func NewMcpAssistant() mcpassistant.Service {
	return mcpassistant.NewMCPAdapter(NewAssistant(), nil)
}
