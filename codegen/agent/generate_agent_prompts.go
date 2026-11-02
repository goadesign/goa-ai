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

	agentexpr "goa.design/goa-ai/expr/agent"
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
		AgentID      string
		IsService    bool
		Prompts      []*PromptData
		UserTypes    []*PromptUserTypeData
		ContextAlias string
		ErrorsAlias  string
		ReflectAlias string
		PlannerAlias string
		PromptAlias  string
	}

	// promptPackagePlan coordinates symbols, types, sum unions, and imports for prompts.go
	// in an agent or service package.
	promptPackagePlan struct {
		pkg               *codegen.GeneratedPackage
		imports           *codegen.GeneratedImportPlan
		decls             []plannedPromptDeclarations
		declaredTypes     map[localizedTypeKey]*codegen.TypeDeclaration
		allLocalizedTypes []*localizedType
		publicShapes      []*goaexpr.AttributeExpr
		unionDecls        map[codegen.UnionDeclarationID]*codegen.UnionDeclaration
		templateData      *promptFileTemplateData
		fileImports       []*codegen.ImportSpec
		unions            []*service.UnionTypeData
	}

	// plannedPromptDeclarations stores name reservations for one prompt.
	plannedPromptDeclarations struct {
		constDecl     *codegen.NameDeclaration
		specDecl      *codegen.NameDeclaration
		renderDecl    *codegen.NameDeclaration
		renderRegDecl *codegen.NameDeclaration
		argsTypeDecl  *codegen.NameDeclaration
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

// newPromptPackagePlan initializes prompt package planning for one generated Go package.
func newPromptPackagePlan(pkg *codegen.GeneratedPackage) *promptPackagePlan {
	return &promptPackagePlan{
		pkg:           pkg,
		imports:       codegen.NewGeneratedImportPlan(pkg),
		declaredTypes: make(map[localizedTypeKey]*codegen.TypeDeclaration),
		unionDecls:    make(map[codegen.UnionDeclarationID]*codegen.UnionDeclaration),
	}
}

// agentPromptsFile writes gen/<svc>/agents/<agent>/prompts.go containing canonical prompt IDs,
// precomputed PromptSpecs, PromptSpecs aggregator, typed Render helpers, and RegisterPrompts.
func agentPromptsFile(agent *AgentData) (*codegen.File, error) {
	if len(agent.Prompts) == 0 {
		return nil, nil
	}
	plan := agent.promptsPlan
	if plan == nil {
		return nil, fmt.Errorf("agent %q has prompts but no prompt package plan", agent.ID)
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" prompt specifications and helpers", agent.PackageName, plan.fileImports),
		{
			Name:    "agent-prompts",
			Source:  agentsTemplates.Read(agentPromptsFileT),
			Data:    plan.templateData,
			FuncMap: templateFuncMap(),
		},
	}
	for _, union := range plan.unions {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "prompt-union-type",
			Source: service.UnionTypeSource,
			Data:   union,
		})
	}

	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "prompts.go"),
		SectionTemplates: sections,
	}, nil
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
	plan := svc.promptsPlan
	if plan == nil {
		return nil, fmt.Errorf("service %q has prompts but no prompt package plan", svc.Service.Name)
	}

	dir := filepath.Join(codegen.Gendir, svc.Service.PathName, "prompts")
	sections := []*codegen.SectionTemplate{
		codegen.Header(svc.Service.Name+" service prompt specifications and helpers", "prompts", plan.fileImports),
		{
			Name:    "service-prompts",
			Source:  agentsTemplates.Read(agentPromptsFileT),
			Data:    plan.templateData,
			FuncMap: templateFuncMap(),
		},
	}
	for _, union := range plan.unions {
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

// declare reserves symbols, localized types, sum unions, and imports for prompts.go
// before Generation.Freeze runs.
func (p *promptPackagePlan) declare(prompts []*agentexpr.PromptExpr) error {
	if err := p.pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, "PromptSpecs")); err != nil {
		return err
	}
	if err := p.pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, "RegisterPrompts")); err != nil {
		return err
	}

	p.decls = make([]plannedPromptDeclarations, len(prompts))
	for i, pr := range prompts {
		goName := codegen.Goify(pr.Name, true)
		specFuncName := "PromptSpec" + goName
		renderFuncName := "Render" + goName + "Prompt"
		renderWithRegFuncName := "Render" + goName + "PromptWithRegistry"
		constantName := "Prompt" + goName + "ID"
		argsTypeName := goName + "PromptArgs"
		if pr.Name == systemPromptName {
			specFuncName = "PromptSpecSystem"
			renderFuncName = "RenderSystemPrompt"
			renderWithRegFuncName = "RenderSystemPromptWithRegistry"
			constantName = "SystemPromptID"
			argsTypeName = "SystemPromptArgs"
		}

		idKey := string(pr.ID())
		constDecl := codegen.NewPreferredName(codegen.NameConstant, constantName, codegen.ExportedName, specNameOrder{packagePath: p.pkg.ImportPath(), key: idKey + ":const"})
		if err := p.pkg.DeclareName(constDecl); err != nil {
			return err
		}
		specDecl := codegen.NewPreferredName(codegen.NameFunction, specFuncName, codegen.ExportedName, specNameOrder{packagePath: p.pkg.ImportPath(), key: idKey + ":spec"})
		if err := p.pkg.DeclareName(specDecl); err != nil {
			return err
		}
		renderDecl := codegen.NewPreferredName(codegen.NameFunction, renderFuncName, codegen.ExportedName, specNameOrder{packagePath: p.pkg.ImportPath(), key: idKey + ":render"})
		if err := p.pkg.DeclareName(renderDecl); err != nil {
			return err
		}
		renderRegDecl := codegen.NewPreferredName(codegen.NameFunction, renderWithRegFuncName, codegen.ExportedName, specNameOrder{packagePath: p.pkg.ImportPath(), key: idKey + ":render-with-registry"})
		if err := p.pkg.DeclareName(renderRegDecl); err != nil {
			return err
		}
		var argsTypeDecl *codegen.NameDeclaration
		if pr.Args != nil && pr.Args.Type != nil && pr.Args.Type != goaexpr.Empty {
			argsTypeDecl = codegen.NewPreferredName(codegen.NameType, argsTypeName, codegen.ExportedName, specNameOrder{packagePath: p.pkg.ImportPath(), key: idKey + ":args-type"})
			if err := p.pkg.DeclareName(argsTypeDecl); err != nil {
				return err
			}
		}
		p.decls[i] = plannedPromptDeclarations{
			constDecl:     constDecl,
			specDecl:      specDecl,
			renderDecl:    renderDecl,
			renderRegDecl: renderRegDecl,
			argsTypeDecl:  argsTypeDecl,
		}
	}

	p.publicShapes = make([]*goaexpr.AttributeExpr, len(prompts))
	for i, pr := range prompts {
		if pr.Args == nil || pr.Args.Type == nil || pr.Args.Type == goaexpr.Empty {
			continue
		}
		shape := pr.Args
		if ut, ok := shape.Type.(goaexpr.UserType); ok && ut != goaexpr.Empty {
			shape = stripStructPkgMeta(ut.Attribute())
		} else {
			shape = stripStructPkgMeta(shape)
		}
		public, publicTypes := localizeNestedTypes(shape, false, nil, specJSONModel)
		p.publicShapes[i] = public

		for _, localized := range publicTypes {
			key := localizedTypeKey{source: localized.source, jsonContract: localized.jsonContract}
			if result, ok := localized.source.(*goaexpr.ResultTypeExpr); ok {
				key.resultIdentifier = result.Identifier
				key.source = nil
			}
			if declaration := p.declaredTypes[key]; declaration != nil {
				if err := p.pkg.BindGeneratedType(localized.generated, declaration); err != nil {
					return err
				}
				localized.declaration = declaration
				continue
			}
			declaration, err := p.pkg.DeclareGeneratedType(
				localized.generated.TypeName,
				newLocalizedTypeNameOrder(p.pkg.ImportPath(), localized.source, localizedTypeDeclarationName, localized.jsonContract),
			)
			if err != nil {
				return err
			}
			if err := p.pkg.BindGeneratedType(localized.generated, declaration); err != nil {
				return err
			}
			p.declaredTypes[key] = declaration
			localized.declaration = declaration
			p.allLocalizedTypes = append(p.allLocalizedTypes, localized)
		}

		if err := declareAttributeUnions(p.pkg, p.unionDecls, public); err != nil {
			return fmt.Errorf("declare prompt union sum types: %w", err)
		}
	}

	requiredImports := []*codegen.ImportSpec{
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		codegen.SimpleImport("reflect"),
		codegen.SimpleImport(plannerImportPath),
		codegen.SimpleImport(promptImportPath),
	}
	if len(p.unionDecls) > 0 {
		requiredImports = append(requiredImports,
			codegen.SimpleImport("bytes"),
			codegen.SimpleImport("encoding/json"),
			codegen.SimpleImport("fmt"),
			codegen.GoaImport(""),
		)
	}
	if err := p.imports.Require(requiredImports...); err != nil {
		return err
	}

	for _, shape := range p.publicShapes {
		if shape != nil {
			if err := p.imports.AddTypeExpressions(shape); err != nil {
				return err
			}
		}
	}
	for _, localized := range p.allLocalizedTypes {
		if err := p.imports.AddTypeExpressions(localized.generated.AttributeExpr); err != nil {
			return err
		}
	}
	return nil
}

// link evaluates finalized types, sums, argument shapes, and import aliases
// after Generation.Freeze has run.
func (p *promptPackagePlan) link(prompts []*PromptData, agentID string, isService bool) error {
	if err := p.imports.Link(); err != nil {
		return fmt.Errorf("link prompt imports: %w", err)
	}

	for i, prompt := range prompts {
		if i >= len(p.decls) {
			continue
		}
		decl := p.decls[i]
		prompt.ConstantName = decl.constDecl.Name()
		prompt.SpecFuncName = decl.specDecl.Name()
		prompt.RenderFuncName = decl.renderDecl.Name()
		prompt.RenderWithRegistryFuncName = decl.renderRegDecl.Name()
		if prompt.HasArgs && decl.argsTypeDecl != nil {
			prompt.ArgsTypeName = decl.argsTypeDecl.Name()
		}
	}

	scope := p.pkg.Scope().Fork()

	userTypeData := make([]*PromptUserTypeData, 0, len(p.allLocalizedTypes))
	for _, localized := range p.allLocalizedTypes {
		userTypeData = append(userTypeData, &PromptUserTypeData{
			Name: localized.declaration.Name(),
			Def:  scope.GoTypeDef(localized.generated.AttributeExpr, false, true),
		})
	}
	sort.Slice(userTypeData, func(i, j int) bool {
		return userTypeData[i].Name < userTypeData[j].Name
	})

	unionMap := make(map[codegen.UnionDeclarationID]*unionTypeData)
	seenUnionTypes := make(map[goaexpr.UserType]struct{})
	for i, prompt := range prompts {
		if prompt.HasArgs && i < len(p.publicShapes) && p.publicShapes[i] != nil {
			collectUnionSumTypes(p.publicShapes[i], scope, p.pkg, unionMap, seenUnionTypes)
		}
	}
	unions := make([]*service.UnionTypeData, 0, len(unionMap))
	for _, u := range unionMap {
		unions = append(unions, u)
	}
	sort.Slice(unions, func(i, j int) bool {
		return unions[i].TypeDeclaration.Name() < unions[j].TypeDeclaration.Name()
	})

	for i, prompt := range prompts {
		if prompt.HasArgs && i < len(p.publicShapes) && p.publicShapes[i] != nil {
			prompt.ArgsDef = scope.GoTypeDef(p.publicShapes[i], false, true)
		}
	}

	p.templateData = &promptFileTemplateData{
		AgentID:      agentID,
		IsService:    isService,
		Prompts:      prompts,
		UserTypes:    userTypeData,
		ContextAlias: p.pkg.ImportName("context"),
		ErrorsAlias:  p.pkg.ImportName("errors"),
		ReflectAlias: p.pkg.ImportName("reflect"),
		PlannerAlias: p.pkg.ImportName(plannerImportPath),
		PromptAlias:  p.pkg.ImportName(promptImportPath),
	}
	p.fileImports = p.imports.Imports()
	p.unions = unions
	return nil
}
