// Package codegen emits a model caller beside Goa's typed protocol client.
// Authored tool visibility becomes fixed guards, while Goa owns request routes
// and the shared MCP transport owns protocol metadata and result decoding.
package codegen

import (
	"path/filepath"
	"strconv"

	"goa.design/goa/v3/codegen"
)

// clientCallerFile writes the caller using Goa's linked payload and route names.
// Services without tools receive no model caller file.
func clientCallerFile(data *AdapterData) *codegen.File {
	if data == nil || data.ClientCaller == nil {
		return nil
	}
	path := filepath.Join(codegen.Gendir, "jsonrpc", data.mcpPathName, "client", "caller.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("MCP runtime caller", "client", data.ClientCaller.imports),
		{
			Name:    "mcp-client-caller",
			Source:  mcpTemplates.Read("mcp_client_caller"),
			Data:    data.ClientCaller,
			FuncMap: map[string]any{"quote": strconv.Quote},
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}
