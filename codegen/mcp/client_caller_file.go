package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

func clientCallerFile(data *AdapterData, svcName string) *codegen.File {
	if data == nil || data.ClientCaller == nil {
		return nil
	}
	path := filepath.Join(codegen.Gendir, "jsonrpc", "mcp_"+svcName, "client", "caller.go")
	sections := []*codegen.SectionTemplate{
		{
			Name:   "mcp-client-caller",
			Source: mcpTemplates.Read("mcp_client_caller"),
			Data:   data.ClientCaller,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}
