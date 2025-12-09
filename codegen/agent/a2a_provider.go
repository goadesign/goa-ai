package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

// a2aServerStubFile generates the server_stub.go file that wires the generated
// A2A service to the shared runtime/a2a.Server implementation via a helper
// constructor.
func a2aServerStubFile(data *A2AAdapterData) *codegen.File {
	if data == nil || data.Agent == nil || len(data.Skills) == 0 {
		return nil
	}

	regData := buildA2ARegisterData(data)
	if regData == nil {
		return nil
	}

	agentName := codegen.SnakeCase(data.Agent.Name)
	path := filepath.Join(codegen.Gendir, "a2a_"+agentName, "server_stub.go")

	sections := []*codegen.SectionTemplate{
		{
			Name:   "a2a-server-stub",
			Source: agentsTemplates.Read(a2aServerStubFileT),
			Data:   regData,
		},
	}

	return &codegen.File{
		Path:             path,
		SectionTemplates: sections,
	}
}
