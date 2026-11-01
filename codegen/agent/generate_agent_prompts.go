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
func agentPromptsFile(agent *AgentData) (*codegen.File, error) {
	if len(agent.Prompts) == 0 {
		return nil, nil
	}

	data, imports, unions, err := buildPromptFileTemplateData(agent.Prompts, agent.ID, false, agent.ImportPath, agent.Genpkg)
	if err != nil {
		return nil, err
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

	svcImportPath := path.Join(svc.Genpkg, svc.Service.PathName, "prompts")
	data, imports, unions, err := buildPromptFileTemplateData(svc.Prompts, "", true, svcImportPath, svc.Genpkg)
	if err != nil {
		return nil, err
	}

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
// prompt template data using package generation machinery and symbol reservations.
func buildPromptFileTemplateData(prompts []*PromptData, agentID string, isService bool, pkgImportPath, genpkg string) (*promptFileTemplateData, []*codegen.ImportSpec, []*service.UnionTypeData, error) {
	genRoot := genpkg
	if genRoot == "" {
		genRoot = "goa.design/goa-ai/gen"
	}
	generation, err := codegen.NewGeneration(genRoot, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create generation for prompt templates: %w", err)
	}
	if pkgImportPath == "" {
		pkgImportPath = path.Join(genRoot, "prompts")
	}
	pkg, err := generation.ClaimPackage(pkgImportPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("claim package %q for prompt templates: %w", pkgImportPath, err)
	}

	// Declare fixed names for prompt functions.
	if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, "PromptSpecs")); err != nil {
		return nil, nil, nil, err
	}
	if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, "RegisterPrompts")); err != nil {
		return nil, nil, nil, err
	}

	// For agent packages, also declare the fixed agent package names to prevent collision.
	if !isService {
		fixedConstants := []string{
			"AgentID",
			"WorkflowName",
			"DefaultTaskQueue",
			"PlanActivity",
			"ResumeActivity",
			"ExecuteToolActivity",
		}
		for _, name := range fixedConstants {
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameConstant, name)); err != nil {
				return nil, nil, nil, err
			}
		}
		fixedFunctions := []string{
			"Definition",
			"NewClient",
			"RegisterUsedToolsets",
		}
		for _, name := range fixedFunctions {
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameFunction, name)); err != nil {
				return nil, nil, nil, err
			}
		}
		fixedVariables := []string{
			"agentDefinition",
			"usedToolsetRegistrationOptions",
		}
		for _, name := range fixedVariables {
			if err := pkg.DeclareName(codegen.NewExactName(codegen.NameVariable, name)); err != nil {
				return nil, nil, nil, err
			}
		}
	}

	// Declare preferred names for each prompt.
	type promptDeclarations struct {
		constDecl     *codegen.NameDeclaration
		specDecl      *codegen.NameDeclaration
		renderDecl    *codegen.NameDeclaration
		renderRegDecl *codegen.NameDeclaration
		argsTypeDecl  *codegen.NameDeclaration
	}
	decls := make([]promptDeclarations, len(prompts))
	for i, p := range prompts {
		constDecl := codegen.NewPreferredName(codegen.NameConstant, p.ConstantName, codegen.ExportedName, specNameOrder{packagePath: pkg.ImportPath(), key: p.ID + ":const"})
		if err := pkg.DeclareName(constDecl); err != nil {
			return nil, nil, nil, err
		}
		specDecl := codegen.NewPreferredName(codegen.NameFunction, p.SpecFuncName, codegen.ExportedName, specNameOrder{packagePath: pkg.ImportPath(), key: p.ID + ":spec"})
		if err := pkg.DeclareName(specDecl); err != nil {
			return nil, nil, nil, err
		}
		renderDecl := codegen.NewPreferredName(codegen.NameFunction, p.RenderFuncName, codegen.ExportedName, specNameOrder{packagePath: pkg.ImportPath(), key: p.ID + ":render"})
		if err := pkg.DeclareName(renderDecl); err != nil {
			return nil, nil, nil, err
		}
		renderRegDecl := codegen.NewPreferredName(codegen.NameFunction, p.RenderWithRegistryFuncName, codegen.ExportedName, specNameOrder{packagePath: pkg.ImportPath(), key: p.ID + ":render-with-registry"})
		if err := pkg.DeclareName(renderRegDecl); err != nil {
			return nil, nil, nil, err
		}
		var argsTypeDecl *codegen.NameDeclaration
		if p.HasArgs {
			argsTypeDecl = codegen.NewPreferredName(codegen.NameType, p.ArgsTypeName, codegen.ExportedName, specNameOrder{packagePath: pkg.ImportPath(), key: p.ID + ":args-type"})
			if err := pkg.DeclareName(argsTypeDecl); err != nil {
				return nil, nil, nil, err
			}
		}
		decls[i] = promptDeclarations{
			constDecl:     constDecl,
			specDecl:      specDecl,
			renderDecl:    renderDecl,
			renderRegDecl: renderRegDecl,
			argsTypeDecl:  argsTypeDecl,
		}
	}

	declaredTypes := make(map[localizedTypeKey]*codegen.TypeDeclaration)
	var allLocalizedTypes []*localizedType
	publicShapes := make([]*goaexpr.AttributeExpr, len(prompts))
	unionDecls := make(map[codegen.UnionDeclarationID]*codegen.UnionDeclaration)

	for i, p := range prompts {
		if !p.HasArgs || p.Expr == nil || p.Expr.Args == nil {
			continue
		}
		shape := p.Expr.Args
		if ut, ok := shape.Type.(goaexpr.UserType); ok && ut != goaexpr.Empty {
			shape = stripStructPkgMeta(ut.Attribute())
		} else {
			shape = stripStructPkgMeta(shape)
		}
		public, publicTypes := localizeNestedTypes(shape, false, nil, specJSONModel)
		publicShapes[i] = public

		for _, localized := range publicTypes {
			key := localizedTypeKey{source: localized.source, jsonContract: localized.jsonContract}
			if result, ok := localized.source.(*goaexpr.ResultTypeExpr); ok {
				key.resultIdentifier = result.Identifier
				key.source = nil
			}
			if declaration := declaredTypes[key]; declaration != nil {
				if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
					return nil, nil, nil, err
				}
				localized.declaration = declaration
				continue
			}
			declaration, err := pkg.DeclareGeneratedType(
				localized.generated.TypeName,
				newLocalizedTypeNameOrder(pkg.ImportPath(), localized.source, localizedTypeDeclarationName, localized.jsonContract),
			)
			if err != nil {
				return nil, nil, nil, err
			}
			if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
				return nil, nil, nil, err
			}
			declaredTypes[key] = declaration
			localized.declaration = declaration
			allLocalizedTypes = append(allLocalizedTypes, localized)
		}

		if err := declareAttributeUnions(pkg, unionDecls, public); err != nil {
			return nil, nil, nil, fmt.Errorf("declare prompt union sum types: %w", err)
		}
	}

	if err := generation.Freeze(); err != nil {
		return nil, nil, nil, fmt.Errorf("freeze generation for prompt templates: %w", err)
	}

	scope := pkg.Scope().Fork()

	for i, p := range prompts {
		p.ConstantName = decls[i].constDecl.Name()
		p.SpecFuncName = decls[i].specDecl.Name()
		p.RenderFuncName = decls[i].renderDecl.Name()
		p.RenderWithRegistryFuncName = decls[i].renderRegDecl.Name()
		if p.HasArgs && decls[i].argsTypeDecl != nil {
			p.ArgsTypeName = decls[i].argsTypeDecl.Name()
		}
	}

	userTypeData := make([]*PromptUserTypeData, 0, len(allLocalizedTypes))
	for _, localized := range allLocalizedTypes {
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
	for i, p := range prompts {
		if p.HasArgs && publicShapes[i] != nil {
			collectUnionSumTypes(publicShapes[i], scope, pkg, unionMap, seenUnionTypes)
		}
	}
	unions := make([]*service.UnionTypeData, 0, len(unionMap))
	for _, u := range unionMap {
		unions = append(unions, u)
	}
	sort.Slice(unions, func(i, j int) bool {
		return unions[i].TypeDeclaration.Name() < unions[j].TypeDeclaration.Name()
	})

	for i, p := range prompts {
		if p.HasArgs && publicShapes[i] != nil {
			p.ArgsDef = scope.GoTypeDef(publicShapes[i], false, true)
		}
	}

	seenImports := make(map[string]*codegen.ImportSpec)
	seenTypes := make(map[string]bool)
	var collectExternalImports func(att *goaexpr.AttributeExpr)
	collectExternalImports = func(att *goaexpr.AttributeExpr) {
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
				collectExternalImports(ut.Attribute())
			}
			return
		}
		if obj := goaexpr.AsObject(att.Type); obj != nil {
			for _, nat := range *obj {
				if nat != nil {
					collectExternalImports(nat.Attribute)
				}
			}
			return
		}
		if arr := goaexpr.AsArray(att.Type); arr != nil {
			collectExternalImports(arr.ElemType)
			return
		}
		if mp := goaexpr.AsMap(att.Type); mp != nil {
			collectExternalImports(mp.KeyType)
			collectExternalImports(mp.ElemType)
			return
		}
		if un := goaexpr.AsUnion(att.Type); un != nil {
			for _, nat := range un.Values {
				if nat != nil {
					collectExternalImports(nat.Attribute)
				}
			}
			return
		}
	}
	for i, p := range prompts {
		if p.HasArgs && publicShapes[i] != nil {
			collectExternalImports(publicShapes[i])
		}
	}
	for _, localized := range allLocalizedTypes {
		collectExternalImports(localized.generated.AttributeExpr)
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "reflect"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
		{Path: "goa.design/goa-ai/runtime/agent/prompt"},
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

	return tplData, imports, unions, nil
}
