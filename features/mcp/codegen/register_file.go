package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

func registerFile(data *AdapterData) *codegen.File {
	if data == nil || data.Register == nil {
		return nil
	}
	svcPkg := "mcp_" + codegen.SnakeCase(data.ServiceName)
	path := filepath.Join(codegen.Gendir, svcPkg, "register.go")
	sections := []*codegen.SectionTemplate{
		{
			Name:   "mcp-register",
			Source: mcpTemplates.Read("mcp_register"),
			Data:   data,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}
