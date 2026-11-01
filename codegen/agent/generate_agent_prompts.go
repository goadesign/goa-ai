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
	"path"
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// PromptUserTypeData describes one user type definition emitted in prompts.go.
	PromptUserTypeData struct {
		Name string
		Def  string
	}

	// promptFileTemplateData contains the data passed to agent_prompts.go.tpl.
	promptFileTemplateData struct {
		AgentID   string
		IsService bool
		Prompts   []*PromptData
		UserTypes []*PromptUserTypeData
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
// precomputed PromptSpecs, PromptSpecs aggregator, typed Render helpers, and RegisterPrompts.
func agentPromptsFile(agent *AgentData) *codegen.File {
	if len(agent.Prompts) == 0 {
		return nil
	}

	data, imports, unions := buildPromptFileTemplateData(agent.Prompts, agent.ID, false, agent.ImportPath, agent.Genpkg)

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" prompt specifications and helpers", agent.PackageName, imports),
		{
			Name:    "agent-prompts",
			Source:  agentsTemplates.Read(agentPromptsFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	for _, union := range unions {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "prompt-union-type",
			Source: service.UnionTypeSource,
			Data:   union,
		})
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

	svcImportPath := path.Join(svc.Genpkg, svc.Service.PathName, "prompts")
	data, imports, unions := buildPromptFileTemplateData(svc.Prompts, "", true, svcImportPath, svc.Genpkg)

	dir := filepath.Join(codegen.Gendir, svc.Service.PathName, "prompts")
	sections := []*codegen.SectionTemplate{
		codegen.Header(svc.Service.Name+" service prompt specifications and helpers", "prompts", imports),
		{
			Name:    "service-prompts",
			Source:  agentsTemplates.Read(agentPromptsFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	for _, union := range unions {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "prompt-union-type",
			Source: service.UnionTypeSource,
			Data:   union,
		})
	}
	promptsFile := &codegen.File{
		Path:             filepath.Join(dir, "prompts.go"),
		SectionTemplates: sections,
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

// buildPromptFileTemplateData collects nested user types, resolves imports, and plans
// prompt template data using a single shared NameScope.
func buildPromptFileTemplateData(prompts []*PromptData, agentID string, isService bool, pkgImportPath, genpkg string) (*promptFileTemplateData, []*codegen.ImportSpec, []*service.UnionTypeData) {
	genRoot := genpkg
	if genRoot == "" {
		genRoot = "goa.design/goa-ai/gen"
	}
	generation, err := codegen.NewGeneration(genRoot, nil)
	if err != nil {
		panic(fmt.Errorf("create generation for prompt templates: %w", err))
	}
	if pkgImportPath == "" {
		pkgImportPath = path.Join(genRoot, "prompts")
	}
	pkg, err := generation.ClaimPackage(pkgImportPath)
	if err != nil {
		panic(fmt.Errorf("claim package %q for prompt templates: %w", pkgImportPath, err))
	}

	unionDecls := make(map[codegen.UnionDeclarationID]*codegen.UnionDeclaration)
	for _, p := range prompts {
		if p.Expr != nil && p.Expr.Args != nil {
			if err := declareAttributeUnions(pkg, unionDecls, p.Expr.Args); err != nil {
				panic(fmt.Errorf("declare prompt union sum types: %w", err))
			}
		}
	}
	if err := generation.Freeze(); err != nil {
		panic(fmt.Errorf("freeze generation for prompt templates: %w", err))
	}

	scope := pkg.Scope().Fork()
	for _, p := range prompts {
		scope.Unique(p.ArgsTypeName)
	}

	var (
		userTypes   []goaexpr.UserType
		seenTypes   = make(map[string]bool)
		seenImports = make(map[string]*codegen.ImportSpec)
	)

	var collectTypes func(att *goaexpr.AttributeExpr)
	collectTypes = func(att *goaexpr.AttributeExpr) {
		if att == nil || att.Type == nil {
			return
		}
		for _, imp := range codegen.GetMetaTypeImports(att) {
			if imp != nil && imp.Path != "" {
				seenImports[imp.Path] = imp
			}
		}
		if ut, ok := att.Type.(goaexpr.UserType); ok {
			loc := codegen.UserTypeLocation(ut)
			if loc != nil {
				importPath := path.Join(genpkg, loc.RelImportPath)
				seenImports[importPath] = &codegen.ImportSpec{
					Path: importPath,
					Name: loc.PackageName(),
				}
				return
			}
			id := ut.ID()
			if !seenTypes[id] {
				seenTypes[id] = true
				collectTypes(ut.Attribute())
				userTypes = append(userTypes, ut)
			}
			return
		}
		if obj := goaexpr.AsObject(att.Type); obj != nil {
			for _, nat := range *obj {
				if nat != nil {
					collectTypes(nat.Attribute)
				}
			}
			return
		}
		if arr := goaexpr.AsArray(att.Type); arr != nil {
			collectTypes(arr.ElemType)
			return
		}
		if mp := goaexpr.AsMap(att.Type); mp != nil {
			collectTypes(mp.KeyType)
			collectTypes(mp.ElemType)
			return
		}
		if un := goaexpr.AsUnion(att.Type); un != nil {
			for _, nat := range un.Values {
				if nat != nil {
					collectTypes(nat.Attribute)
				}
			}
			return
		}
	}

	for _, p := range prompts {
		if p.Expr != nil && p.Expr.Args != nil {
			collectTypes(p.Expr.Args)
		}
	}

	userTypeData := make([]*PromptUserTypeData, 0, len(userTypes))
	for _, ut := range userTypes {
		typeName := scope.GoTypeName(&goaexpr.AttributeExpr{Type: ut})
		def := scope.GoTypeDef(ut.Attribute(), false, true)
		userTypeData = append(userTypeData, &PromptUserTypeData{
			Name: typeName,
			Def:  def,
		})
	}

	unionMap := make(map[codegen.UnionDeclarationID]*unionTypeData)
	seenUnionTypes := make(map[goaexpr.UserType]struct{})
	for _, p := range prompts {
		if p.Expr != nil && p.Expr.Args != nil {
			collectUnionSumTypes(p.Expr.Args, scope, pkg, unionMap, seenUnionTypes)
		}
	}
	unions := make([]*service.UnionTypeData, 0, len(unionMap))
	for _, u := range unionMap {
		unions = append(unions, u)
	}
	sort.Slice(unions, func(i, j int) bool {
		return unions[i].TypeDeclaration.Name() < unions[j].TypeDeclaration.Name()
	})

	for _, p := range prompts {
		if p.HasArgs && p.Expr != nil && p.Expr.Args != nil {
			p.ArgsDef = scope.GoTypeDef(p.Expr.Args, false, true)
		}
	}

	hasFields := false
	for _, p := range prompts {
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
	if len(unions) > 0 {
		seenImports["bytes"] = &codegen.ImportSpec{Path: "bytes"}
		seenImports["encoding/json"] = &codegen.ImportSpec{Path: "encoding/json"}
		seenImports["fmt"] = &codegen.ImportSpec{Path: "fmt"}
		seenImports["goa.design/goa/v3/pkg"] = &codegen.ImportSpec{Path: "goa.design/goa/v3/pkg", Name: "goa"}
	}

	importPaths := make([]string, 0, len(seenImports))
	for p := range seenImports {
		importPaths = append(importPaths, p)
	}
	sort.Strings(importPaths)
	for _, p := range importPaths {
		imports = append(imports, seenImports[p])
	}

	tplData := &promptFileTemplateData{
		AgentID:   agentID,
		IsService: isService,
		Prompts:   prompts,
		UserTypes: userTypeData,
	}

	return tplData, imports, unions
}
