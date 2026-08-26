// Package assistantapi connects the assistant fixture to its generated MCP service.
package assistantapi

import mcpassistant "example.com/assistant/gen/mcp_assistant"

// NewMcpAssistant returns an MCP server implementation for the assistant
// service used by the integration test fixture.
func NewMcpAssistant() mcpassistant.Service {
	return mcpassistant.NewMCPAdapter(NewAssistant(), nil)
}
