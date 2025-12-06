package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

// a2aClientFiles generates the A2A client files for agents that have
// exported toolsets. The client enables invoking external A2A agents
// discovered through registries.
func a2aClientFiles(agent *AgentData, security *A2ASecurityData) []*codegen.File {
	// Only generate A2A client if the agent has exported toolsets
	// (agents that export are likely to also consume external A2A agents)
	if len(agent.ExportedToolsets) == 0 {
		return nil
	}

	data := buildA2AClientData(agent, security)
	if data == nil {
		return nil
	}

	var files []*codegen.File

	// Generate client.go
	clientFile := a2aClientFile(agent, data)
	if clientFile != nil {
		files = append(files, clientFile)
	}

	// Generate auth.go with typed auth providers
	authFile := a2aAuthFile(agent, data)
	if authFile != nil {
		files = append(files, authFile)
	}

	return files
}

// A2AClientData holds the template-ready data for generating an A2A client.
type A2AClientData struct {
	// Agent contains the agent metadata.
	Agent *AgentData
	// Security contains the security scheme data.
	Security *A2ASecurityData
}

// buildA2AClientData transforms agent data into A2A client template data.
func buildA2AClientData(agent *AgentData, security *A2ASecurityData) *A2AClientData {
	return &A2AClientData{
		Agent:    agent,
		Security: security,
	}
}

// HasSecuritySchemes returns true if security schemes are present.
func (d *A2AClientData) HasSecuritySchemes() bool {
	return d.Security != nil && d.Security.HasSecuritySchemes
}

// SecuritySchemes returns the security scheme definitions.
func (d *A2AClientData) SecuritySchemes() []*A2ASecuritySchemeData {
	if d.Security == nil {
		return nil
	}
	return d.Security.SecuritySchemes
}

// a2aClientFile generates the client.go file for A2A client functionality.
func a2aClientFile(agent *AgentData, data *A2AClientData) *codegen.File {
	// Output path: gen/<service>/agents/<agent>/a2a/client.go
	dir := filepath.Join(agent.Dir, a2aPkgName)

	imports := []*codegen.ImportSpec{
		{Path: "bufio"},
		{Path: "bytes"},
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "io"},
		{Path: "net/http"},
		{Path: "time"},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.GoName+" A2A client", a2aPkgName, imports),
		{
			Name:    "a2a-client",
			Source:  agentsTemplates.Read(a2aClientFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(dir, "client.go"),
		SectionTemplates: sections,
	}
}

// a2aAuthFile generates the auth.go file with typed auth providers.
func a2aAuthFile(agent *AgentData, data *A2AClientData) *codegen.File {
	// Only generate if there are security schemes
	if !data.HasSecuritySchemes() {
		return nil
	}

	// Output path: gen/<service>/agents/<agent>/a2a/auth.go
	dir := filepath.Join(agent.Dir, a2aPkgName)

	imports := []*codegen.ImportSpec{
		{Path: "net/http"},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.GoName+" A2A auth providers", a2aPkgName, imports),
		{
			Name:    "a2a-auth",
			Source:  agentsTemplates.Read(a2aAuthFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(dir, "auth.go"),
		SectionTemplates: sections,
	}
}
