// Package codegen writes agent packages, prompt specifications, and prompt schemas
// from the evaluated Goa design.
//
// generate_agent_prompts.go emits the generated prompt artifacts in the agent package
// (gen/<svc>/agents/<agent>/prompts.go) and service package (gen/<svc>/prompts/prompts.go),
// including canonical Ident constants, precomputed PromptSpec factories, typed Args structs,
// typed Render* helpers, and JSON schema catalog documents.
package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
)

type (
	// promptFileTemplateData contains the data passed to agent_prompts.go.tpl.
	promptFileTemplateData struct {
		AgentID string
		Prompts []*PromptData
	}

	// promptSchemaRecord represents one prompt in specs/prompt_schemas.json.
	promptSchemaRecord struct {
		ID          string           `json:"id"`
		Name        string           `json:"name"`
		Service     string           `json:"service"`
		Agent       string           `json:"agent,omitempty"`
		Role        string           `json:"role"`
		Description string           `json:"description,omitempty"`
		Template    string           `json:"template"`
		Version     string           `json:"version"`
		Args        *agentTypeSchema `json:"args,omitempty"`
	}

	// agentPromptSchemasDoc is the top-level document structure for specs/prompt_schemas.json.
	agentPromptSchemasDoc struct {
		AgentID string               `json:"agent_id"`
		Prompts []promptSchemaRecord `json:"prompts"`
	}

	// servicePromptSchemasDoc is the top-level document for service-level prompt schemas.
	servicePromptSchemasDoc struct {
		Service string               `json:"service"`
		Prompts []promptSchemaRecord `json:"prompts"`
	}
)

// agentPromptsFile writes gen/<svc>/agents/<agent>/prompts.go containing canonical prompt IDs,
// precomputed PromptSpecs, PromptSpecs aggregator, and typed Render helpers.
func agentPromptsFile(agent *AgentData) *codegen.File {
	if len(agent.Prompts) == 0 {
		return nil
	}

	hasFields := false
	for _, p := range agent.Prompts {
		if len(p.Fields) > 0 {
			hasFields = true
			break
		}
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
		{Path: "goa.design/goa-ai/runtime/agent/prompt"},
	}
	if hasFields {
		imports = append(imports, &codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"})
	}

	data := &promptFileTemplateData{
		AgentID: agent.ID,
		Prompts: agent.Prompts,
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" prompt specifications and helpers", agent.PackageName, imports),
		{
			Name:    "agent-prompts",
			Source:  agentsTemplates.Read(agentPromptsFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "prompts.go"),
		SectionTemplates: sections,
	}
}

// agentPromptSchemasJSONFile writes gen/<svc>/agents/<agent>/specs/prompt_schemas.json
// with every prompt ID, name, role, template, version, and args schema.
func agentPromptSchemasJSONFile(agent *AgentData) (*codegen.File, error) {
	if len(agent.Prompts) == 0 {
		return nil, nil
	}

	doc := agentPromptSchemasDoc{
		AgentID: agent.ID,
		Prompts: make([]promptSchemaRecord, 0, len(agent.Prompts)),
	}

	for _, p := range agent.Prompts {
		record := promptSchemaRecord{
			ID:          p.ID,
			Name:        p.Name,
			Service:     agent.Service.Name,
			Agent:       agent.Name,
			Role:        p.Role,
			Description: p.Description,
			Template:    p.Template,
			Version:     p.Version,
		}
		if p.HasArgs && len(p.SchemaJSON) > 0 {
			record.Args = &agentTypeSchema{
				Name:   p.ArgsTypeName,
				Schema: json.RawMessage(p.SchemaJSON),
			}
		}
		doc.Prompts = append(doc.Prompts, record)
	}

	sort.Slice(doc.Prompts, func(i, j int) bool {
		return doc.Prompts[i].ID < doc.Prompts[j].ID
	})

	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode prompt schemas for agent %q: %w", agent.Name, err)
	}
	payload = append(payload, '\n')

	sections := []*codegen.SectionTemplate{
		{
			Name:   "prompt-schemas-json",
			Source: "{{ . }}",
			Data:   string(payload),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "specs", "prompt_schemas.json"),
		SectionTemplates: sections,
	}, nil
}

// servicePromptsFiles writes gen/<svc>/prompts/prompts.go and specs/prompt_schemas.json
// for service-level prompts.
func servicePromptsFiles(svc *ServiceAgentsData) ([]*codegen.File, error) {
	if len(svc.Prompts) == 0 {
		return nil, nil
	}

	hasFields := false
	for _, p := range svc.Prompts {
		if len(p.Fields) > 0 {
			hasFields = true
			break
		}
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
		{Path: "goa.design/goa-ai/runtime/agent/prompt"},
	}
	if hasFields {
		imports = append(imports, &codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"})
	}

	data := &promptFileTemplateData{
		AgentID: "",
		Prompts: svc.Prompts,
	}

	dir := filepath.Join(codegen.Gendir, svc.Service.PathName, "prompts")
	promptsFile := &codegen.File{
		Path: filepath.Join(dir, "prompts.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(svc.Service.Name+" service prompt specifications and helpers", "prompts", imports),
			{
				Name:    "service-prompts",
				Source:  agentsTemplates.Read(agentPromptsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		},
	}

	doc := servicePromptSchemasDoc{
		Service: svc.Service.Name,
		Prompts: make([]promptSchemaRecord, 0, len(svc.Prompts)),
	}
	for _, p := range svc.Prompts {
		record := promptSchemaRecord{
			ID:          p.ID,
			Name:        p.Name,
			Service:     svc.Service.Name,
			Role:        p.Role,
			Description: p.Description,
			Template:    p.Template,
			Version:     p.Version,
		}
		if p.HasArgs && len(p.SchemaJSON) > 0 {
			record.Args = &agentTypeSchema{
				Name:   p.ArgsTypeName,
				Schema: json.RawMessage(p.SchemaJSON),
			}
		}
		doc.Prompts = append(doc.Prompts, record)
	}

	sort.Slice(doc.Prompts, func(i, j int) bool {
		return doc.Prompts[i].ID < doc.Prompts[j].ID
	})

	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode prompt schemas for service %q: %w", svc.Service.Name, err)
	}
	payload = append(payload, '\n')

	jsonFile := &codegen.File{
		Path: filepath.Join(dir, "specs", "prompt_schemas.json"),
		SectionTemplates: []*codegen.SectionTemplate{
			{
				Name:   "service-prompt-schemas-json",
				Source: "{{ . }}",
				Data:   string(payload),
			},
		},
	}

	return []*codegen.File{promptsFile, jsonFile}, nil
}
