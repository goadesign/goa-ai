package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

func clientCallerFile(data *AdapterData) *codegen.File {
	if data == nil || data.ClientCaller == nil {
		return nil
	}
	path := filepath.Join(codegen.Gendir, "jsonrpc", data.mcpPathName, "client", "caller.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("MCP runtime caller", "client", data.ClientCaller.imports),
		{
			Name:   "mcp-client-caller",
			Source: mcpTemplates.Read("mcp_client_caller"),
			Data:   data.ClientCaller,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}
