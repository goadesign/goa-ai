package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

func registerFile(data *AdapterData) *codegen.File {
	if data == nil || data.Register == nil {
		return nil
	}
	path := filepath.Join(codegen.Gendir, data.mcpPathName, "register.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("MCP tool registration helpers", data.MCPPackage, data.registerImports),
		{
			Name:   "mcp-register",
			Source: mcpTemplates.Read("mcp_register"),
			Data:   data,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}
