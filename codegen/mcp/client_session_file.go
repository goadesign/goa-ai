// Package codegen places MCP initialization beside Goa's generated JSON-RPC
// client so every higher-level generated client uses the same session setup.
package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

// clientSessionFile returns the shared initialization helper for one generated
// MCP JSON-RPC client package.
func clientSessionFile(data *AdapterData) *codegen.File {
	if data == nil || data.ClientSession == nil {
		return nil
	}
	return &codegen.File{
		Path: filepath.Join(codegen.Gendir, "jsonrpc", data.mcpPathName, "client", "session.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("MCP client session initialization", "client", data.ClientSession.imports),
			{
				Name:   "mcp-client-session",
				Source: mcpTemplates.Read("mcp_client_session"),
				Data:   data.ClientSession,
			},
		},
	}
}
