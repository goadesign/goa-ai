// Package ir builds the canonical generator-facing model from evaluated Goa and
// goa-ai roots.
//
// The builder performs ownership selection, output path derivation, and
// deterministic ordering exactly once so downstream generators can consume a
// stable design graph instead of rebuilding the same facts independently.
package ir

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type toolsetOwnerRefKind string

const (
	toolsetOwnerRefUsed        toolsetOwnerRefKind = "used"
	toolsetOwnerRefExported    toolsetOwnerRefKind = "exported"
	toolsetOwnerRefServiceExpo toolsetOwnerRefKind = "service_export"
)

type toolsetOwnerRef struct {
	kind toolsetOwnerRefKind

	serviceName     string
	servicePathName string

	agentName string
	agentSlug string
}

// Build constructs a Design IR from evaluated Goa roots.
func Build(genpkg string, roots []eval.Root) (*Design, error) {
	agentsRoot, err := findAgentsRoot(roots)
	if err != nil {
		return nil, err
	}
	goaRoot, err := findGoaRoot(roots)
	if err != nil {
		return nil, err
	}

	servicesData := service.NewServicesData(goaRoot)
	services := buildServices(goaRoot, servicesData)
	servicesByName := make(map[string]*Service, len(services))
	for _, svc := range services {
		servicesByName[svc.Name] = svc
	}

	agents, err := buildAgents(genpkg, agentsRoot, servicesByName)
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		agent.Service.Agents = append(agent.Service.Agents, agent)
	}
	for _, svc := range services {
		slices.SortFunc(svc.Agents, func(a, b *Agent) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	completions, err := buildCompletions(agentsRoot, servicesByName)
	if err != nil {
		return nil, err
	}
	for _, completion := range completions {
		completion.Service.Completions = append(completion.Service.Completions, completion)
	}
	for _, svc := range services {
		slices.SortFunc(svc.Completions, func(a, b *Completion) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	refsByToolset := collectToolsetOwnerRefs(agentsRoot, servicesByName)
	toolsets, toolsetsByName, err := buildToolsets(genpkg, agentsRoot, refsByToolset, servicesByName)
	if err != nil {
		return nil, err
	}
	if err := attachToolsetRefs(genpkg, servicesData, servicesByName, toolsetsByName, agents); err != nil {
		return nil, err
	}

	return &Design{
		Genpkg:      genpkg,
		GoaRoot:     goaRoot,
		AgentsRoot:  agentsRoot,
		Services:    services,
		Agents:      agents,
		Toolsets:    toolsets,
		Completions: completions,
	}, nil
}

func findAgentsRoot(roots []eval.Root) (*agentsExpr.RootExpr, error) {
	for _, root := range roots {
		if agentsRoot, ok := root.(*agentsExpr.RootExpr); ok {
			return agentsRoot, nil
		}
	}
	return nil, fmt.Errorf("agent root not found in eval roots")
}

func findGoaRoot(roots []eval.Root) (*goaexpr.RootExpr, error) {
	for _, root := range roots {
		if goaRoot, ok := root.(*goaexpr.RootExpr); ok {
			return goaRoot, nil
		}
	}
	return nil, fmt.Errorf("goa root not found in eval roots")
}

func buildServices(root *goaexpr.RootExpr, servicesData *service.ServicesData) []*Service {
	out := make([]*Service, 0, len(root.Services))
	for _, svc := range root.Services {
		if svc == nil {
			continue
		}
		sd := servicesData.Get(svc.Name)
		if sd == nil {
			continue
		}
		out = append(out, &Service{
			Name:     sd.Name,
			PathName: sd.PathName,
			Goa:      sd,
		})
	}
	slices.SortFunc(out, func(a, b *Service) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func buildAgents(genpkg string, root *agentsExpr.RootExpr, servicesByName map[string]*Service) ([]*Agent, error) {
	agents := make([]*Agent, 0, len(root.Agents))
	for _, expr := range root.Agents {
		if expr == nil || expr.Service == nil {
			continue
		}
		svc := servicesByName[expr.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for agent %q", expr.Service.Name, expr.Name)
		}
		agent, err := newAgent(genpkg, svc, expr)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	slices.SortFunc(agents, func(a, b *Agent) int {
		if delta := strings.Compare(a.Service.Name, b.Service.Name); delta != 0 {
			return delta
		}
		return strings.Compare(a.Name, b.Name)
	})
	return agents, nil
}

func newAgent(genpkg string, svc *Service, expr *agentsExpr.AgentExpr) (*Agent, error) {
	slug := naming.SanitizeToken(expr.Name, "")
	if slug == "" {
		return nil, fmt.Errorf("agent %q has no sanitized identifier", expr.Name)
	}
	goName := goacodegen.Goify(expr.Name, true)
	dir := filepath.Join(goacodegen.Gendir, svc.PathName, "agents", slug)
	importPath := path.Join(genpkg, svc.PathName, "agents", slug)
	return &Agent{
		Expr:                  expr,
		Name:                  expr.Name,
		Description:           expr.Description,
		Slug:                  slug,
		ID:                    naming.Identifier(svc.Name, expr.Name),
		Service:               svc,
		PackageName:           slug,
		PathName:              slug,
		Dir:                   dir,
		ImportPath:            importPath,
		ConfigType:            goName + "AgentConfig",
		StructName:            goName + "Agent",
		WorkflowFunc:          goName + "Workflow",
		WorkflowDefinitionVar: goName + "WorkflowDefinition",
		WorkflowName:          naming.Identifier(svc.Name, expr.Name, "workflow"),
		WorkflowQueue:         naming.QueueName(svc.PathName, slug, "workflow"),
		ToolSpecsPackage:      "specs",
		ToolSpecsImportPath:   path.Join(importPath, "specs"),
		ToolSpecsDir:          filepath.Join(dir, "specs"),
	}, nil
}

func buildCompletions(root *agentsExpr.RootExpr, servicesByName map[string]*Service) ([]*Completion, error) {
	completions := make([]*Completion, 0, len(root.Completions))
	for _, expr := range root.Completions {
		if expr == nil || expr.Service == nil {
			continue
		}
		svc := servicesByName[expr.Service.Name]
		if svc == nil {
			return nil, fmt.Errorf("service %q not found for completion %q", expr.Service.Name, expr.Name)
		}
		completions = append(completions, &Completion{
			Expr:        expr,
			Name:        expr.Name,
			Description: expr.Description,
			GoName:      goacodegen.Goify(expr.Name, true),
			Service:     svc,
		})
	}
	slices.SortFunc(completions, func(a, b *Completion) int {
		if delta := strings.Compare(a.Service.Name, b.Service.Name); delta != 0 {
			return delta
		}
		return strings.Compare(a.Name, b.Name)
	})
	return completions, nil
}

func collectToolsetOwnerRefs(root *agentsExpr.RootExpr, servicesByName map[string]*Service) map[string][]toolsetOwnerRef {
	refsByToolset := make(map[string][]toolsetOwnerRef)
	record := func(ts *agentsExpr.ToolsetExpr, kind toolsetOwnerRefKind, svc *Service, agent *Agent) {
		if ts == nil || svc == nil {
			return
		}
		def := ts
		if ts.Origin != nil {
			def = ts.Origin
		}
		if def == nil || def.Name == "" {
			return
		}
		ref := toolsetOwnerRef{
			kind:            kind,
			serviceName:     svc.Name,
			servicePathName: svc.PathName,
		}
		if agent != nil {
			ref.agentName = agent.Name
			ref.agentSlug = agent.Slug
		}
		refsByToolset[def.Name] = append(refsByToolset[def.Name], ref)
	}

	agentsByKey := make(map[string]*Agent)
	for _, svc := range servicesByName {
		for _, agent := range svc.Agents {
			agentsByKey[svc.Name+"|"+agent.Name] = agent
		}
	}
	for _, agentExpr := range root.Agents {
		if agentExpr == nil || agentExpr.Service == nil {
			continue
		}
		svc := servicesByName[agentExpr.Service.Name]
		if svc == nil {
			continue
		}
		agent := agentsByKey[svc.Name+"|"+agentExpr.Name]
		if agentExpr.Used != nil {
			for _, ts := range agentExpr.Used.Toolsets {
				record(ts, toolsetOwnerRefUsed, svc, agent)
			}
		}
		if agentExpr.Exported != nil {
			for _, ts := range agentExpr.Exported.Toolsets {
				record(ts, toolsetOwnerRefExported, svc, agent)
			}
		}
	}
	for _, serviceExport := range root.ServiceExports {
		if serviceExport == nil || serviceExport.Service == nil {
			continue
		}
		svc := servicesByName[serviceExport.Service.Name]
		if svc == nil {
			continue
		}
		for _, ts := range serviceExport.Toolsets {
			record(ts, toolsetOwnerRefServiceExpo, svc, nil)
		}
	}
	return refsByToolset
}

func buildToolsets(
	genpkg string,
	root *agentsExpr.RootExpr,
	refsByToolset map[string][]toolsetOwnerRef,
	servicesByName map[string]*Service,
) ([]*Toolset, map[string]*Toolset, error) {
	defToolsets := collectDefiningToolsets(root)
	toolsets := make([]*Toolset, 0, len(defToolsets))
	toolsetsByName := make(map[string]*Toolset, len(defToolsets))
	paths := make(map[string]string, len(defToolsets))
	for name, def := range defToolsets {
		owner, err := selectOwner(def, refsByToolset[name], servicesByName)
		if err != nil {
			return nil, nil, err
		}
		ts, err := newToolset(genpkg, def, owner)
		if err != nil {
			return nil, nil, err
		}
		if other, ok := paths[ts.SpecsDir]; ok {
			return nil, nil, fmt.Errorf(
				"toolset %q collides with toolset %q on generated specs path %q",
				ts.Name,
				other,
				ts.SpecsDir,
			)
		}
		paths[ts.SpecsDir] = ts.Name
		toolsets = append(toolsets, ts)
		toolsetsByName[ts.Name] = ts
	}
	slices.SortFunc(toolsets, func(a, b *Toolset) int {
		return strings.Compare(a.Name, b.Name)
	})
	return toolsets, toolsetsByName, nil
}

func collectDefiningToolsets(root *agentsExpr.RootExpr) map[string]*agentsExpr.ToolsetExpr {
	toolsets := make(map[string]*agentsExpr.ToolsetExpr)
	consider := func(ts *agentsExpr.ToolsetExpr) {
		if ts == nil || ts.Name == "" || ts.Origin != nil {
			return
		}
		toolsets[ts.Name] = ts
	}
	for _, ts := range root.Toolsets {
		consider(ts)
	}
	for _, serviceExport := range root.ServiceExports {
		if serviceExport == nil {
			continue
		}
		for _, ts := range serviceExport.Toolsets {
			consider(ts)
		}
	}
	for _, agent := range root.Agents {
		if agent == nil {
			continue
		}
		if agent.Exported != nil {
			for _, ts := range agent.Exported.Toolsets {
				consider(ts)
			}
		}
		if agent.Used != nil {
			for _, ts := range agent.Used.Toolsets {
				consider(ts)
			}
		}
	}
	return toolsets
}

func selectOwner(def *agentsExpr.ToolsetExpr, refs []toolsetOwnerRef, servicesByName map[string]*Service) (Owner, error) {
	if def != nil && def.Provider != nil && def.Provider.Kind == agentsExpr.ProviderMCP && def.Provider.MCPService != "" {
		svc := servicesByName[def.Provider.MCPService]
		if svc == nil {
			return Owner{}, fmt.Errorf("toolset %q references unknown MCP service %q", def.Name, def.Provider.MCPService)
		}
		return Owner{
			Kind:            OwnerKindService,
			ServiceName:     svc.Name,
			ServicePathName: svc.PathName,
		}, nil
	}

	exported := selectOwnerRefs(refs, toolsetOwnerRefExported)
	if len(exported) > 0 {
		slices.SortFunc(exported, func(a, b toolsetOwnerRef) int {
			if delta := strings.Compare(a.serviceName, b.serviceName); delta != 0 {
				return delta
			}
			return strings.Compare(a.agentName, b.agentName)
		})
		ref := exported[0]
		return Owner{
			Kind:            OwnerKindAgentExport,
			ServiceName:     ref.serviceName,
			ServicePathName: ref.servicePathName,
			AgentName:       ref.agentName,
			AgentSlug:       ref.agentSlug,
		}, nil
	}

	serviceExports := selectOwnerRefs(refs, toolsetOwnerRefServiceExpo)
	if len(serviceExports) > 0 {
		slices.SortFunc(serviceExports, func(a, b toolsetOwnerRef) int {
			return strings.Compare(a.serviceName, b.serviceName)
		})
		ref := serviceExports[0]
		return Owner{
			Kind:            OwnerKindService,
			ServiceName:     ref.serviceName,
			ServicePathName: ref.servicePathName,
		}, nil
	}

	if len(refs) == 0 {
		return Owner{}, fmt.Errorf("toolset %q has no owning references", def.Name)
	}
	slices.SortFunc(refs, func(a, b toolsetOwnerRef) int {
		return strings.Compare(a.serviceName, b.serviceName)
	})
	ref := refs[0]
	return Owner{
		Kind:            OwnerKindService,
		ServiceName:     ref.serviceName,
		ServicePathName: ref.servicePathName,
	}, nil
}

func selectOwnerRefs(refs []toolsetOwnerRef, kind toolsetOwnerRefKind) []toolsetOwnerRef {
	selected := make([]toolsetOwnerRef, 0, len(refs))
	for _, ref := range refs {
		if ref.kind == kind {
			selected = append(selected, ref)
		}
	}
	return selected
}

func newToolset(genpkg string, expr *agentsExpr.ToolsetExpr, owner Owner) (*Toolset, error) {
	slug := naming.SanitizeToken(expr.Name, "")
	if slug == "" {
		return nil, fmt.Errorf("toolset %q has no sanitized identifier", expr.Name)
	}
	toolset := &Toolset{
		Expr:             expr,
		Name:             expr.Name,
		Slug:             slug,
		Owner:            owner,
		SpecsPackageName: slug,
	}
	switch owner.Kind {
	case OwnerKindService:
		toolset.SpecsDir = filepath.Join(goacodegen.Gendir, owner.ServicePathName, "toolsets", slug)
		toolset.SpecsImportPath = path.Join(genpkg, owner.ServicePathName, "toolsets", slug)
	case OwnerKindAgentExport:
		toolset.SpecsDir = filepath.Join(goacodegen.Gendir, owner.ServicePathName, "agents", owner.AgentSlug, "exports", slug)
		toolset.SpecsImportPath = path.Join(genpkg, owner.ServicePathName, "agents", owner.AgentSlug, "exports", slug)
		toolset.AgentToolsPackage = slug
		toolset.AgentToolsDir = filepath.Join(goacodegen.Gendir, owner.ServicePathName, "agents", owner.AgentSlug, "agenttools", slug)
		toolset.AgentToolsImportPath = path.Join(genpkg, owner.ServicePathName, "agents", owner.AgentSlug, "agenttools", slug)
	default:
		return nil, fmt.Errorf("unknown toolset owner kind %q for toolset %q", owner.Kind, expr.Name)
	}
	return toolset, nil
}

func attachToolsetRefs(
	genpkg string,
	servicesData *service.ServicesData,
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
	agents []*Agent,
) error {
	for _, agent := range agents {
		used, err := buildAgentToolsetRefs(genpkg, servicesData, servicesByName, toolsetsByName, agent, agent.Expr.Used, ToolsetRefKindUsed)
		if err != nil {
			return err
		}
		exported, err := buildAgentToolsetRefs(genpkg, servicesData, servicesByName, toolsetsByName, agent, agent.Expr.Exported, ToolsetRefKindExported)
		if err != nil {
			return err
		}
		agent.UsedToolsets = used
		agent.ExportedToolsets = exported
	}
	return nil
}

func buildAgentToolsetRefs(
	genpkg string,
	servicesData *service.ServicesData,
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
	agent *Agent,
	group *agentsExpr.ToolsetGroupExpr,
	kind ToolsetRefKind,
) ([]*ToolsetRef, error) {
	if group == nil || len(group.Toolsets) == 0 {
		return nil, nil
	}
	refs := make([]*ToolsetRef, 0, len(group.Toolsets))
	for _, expr := range group.Toolsets {
		ref, err := newToolsetRef(genpkg, servicesData, servicesByName, toolsetsByName, agent, expr, kind)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b *ToolsetRef) int {
		return strings.Compare(a.Name, b.Name)
	})
	return refs, nil
}

func newToolsetRef(
	genpkg string,
	servicesData *service.ServicesData,
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
	agent *Agent,
	expr *agentsExpr.ToolsetExpr,
	kind ToolsetRefKind,
) (*ToolsetRef, error) {
	if expr == nil {
		return nil, fmt.Errorf("agent %q has a nil toolset reference", agent.Name)
	}
	defName := expr.Name
	if expr.Origin != nil && expr.Origin.Name != "" {
		defName = expr.Origin.Name
	}
	def := toolsetsByName[defName]
	if def == nil {
		return nil, fmt.Errorf("toolset %q has no defining IR entry", defName)
	}
	slug := naming.SanitizeToken(expr.Name, "")
	if slug == "" {
		return nil, fmt.Errorf("toolset reference %q has no sanitized identifier", expr.Name)
	}
	sourceService, sourceServiceName := resolveSourceService(servicesData, servicesByName, agent.Service, expr)
	qualifiedName := qualifyToolsetName(agent, expr, kind, sourceServiceName)
	ref := &ToolsetRef{
		Expr:                 expr,
		Definition:           def,
		Kind:                 kind,
		Name:                 expr.Name,
		Slug:                 slug,
		QualifiedName:        qualifiedName,
		Description:          expr.Description,
		Tags:                 slices.Clone(expr.Tags),
		Service:              agent.Service,
		ServiceName:          agent.Service.Name,
		Agent:                agent,
		SourceService:        sourceService,
		SourceServiceName:    sourceServiceName,
		TaskQueue:            naming.QueueName(agent.Service.PathName, agent.Slug, slug, "tasks"),
		PackageName:          slug,
		PackageImportPath:    path.Join(agent.ImportPath, slug),
		Dir:                  filepath.Join(agent.Dir, slug),
		SpecsPackageName:     def.SpecsPackageName,
		SpecsImportPath:      def.SpecsImportPath,
		SpecsDir:             def.SpecsDir,
		AgentToolsPackage:    def.AgentToolsPackage,
		AgentToolsImportPath: def.AgentToolsImportPath,
		AgentToolsDir:        def.AgentToolsDir,
	}
	if expr.Provider != nil {
		ref.Provider = buildToolsetProvider(genpkg, agent, ref, expr)
	}
	return ref, nil
}

func resolveSourceService(
	servicesData *service.ServicesData,
	servicesByName map[string]*Service,
	defaultService *Service,
	expr *agentsExpr.ToolsetExpr,
) (*Service, string) {
	sourceService := defaultService
	if expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderMCP && expr.Provider.MCPService != "" {
		if svc := servicesByName[expr.Provider.MCPService]; svc != nil {
			sourceService = svc
		}
	}
	if expr.Origin != nil && expr.Origin.Agent != nil && expr.Origin.Agent.Service != nil {
		if svc := servicesByName[expr.Origin.Agent.Service.Name]; svc != nil {
			sourceService = svc
		}
	}
	isMCPBacked := expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderMCP
	if !isMCPBacked && servicesData != nil && len(expr.Tools) > 0 {
		if svcName := expr.Tools[0].BoundServiceName(); svcName != "" {
			if svc := servicesByName[svcName]; svc != nil {
				sourceService = svc
			}
		}
	}
	sourceServiceName := defaultService.Name
	if sourceService != nil && sourceService.Name != "" {
		sourceServiceName = sourceService.Name
	} else if expr.Provider != nil && expr.Provider.MCPService != "" {
		sourceServiceName = expr.Provider.MCPService
	}
	return sourceService, sourceServiceName
}

func qualifyToolsetName(agent *Agent, expr *agentsExpr.ToolsetExpr, kind ToolsetRefKind, sourceServiceName string) string {
	qualifiedName := expr.Name
	isMCPBacked := expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderMCP
	originServiceName := ""
	if expr.Origin != nil && expr.Origin.Agent != nil && expr.Origin.Agent.Service != nil {
		originServiceName = expr.Origin.Agent.Service.Name
	}
	if kind == ToolsetRefKindUsed && !isMCPBacked {
		if originServiceName == "" || originServiceName == agent.Service.Name {
			qualifiedName = fmt.Sprintf("%s.%s", sourceServiceName, expr.Name)
		}
	}
	return qualifiedName
}

func buildToolsetProvider(genpkg string, agent *Agent, ref *ToolsetRef, expr *agentsExpr.ToolsetExpr) *ToolsetProvider {
	switch expr.Provider.Kind {
	case agentsExpr.ProviderLocal:
		return nil
	case agentsExpr.ProviderMCP:
		return &ToolsetProvider{
			Kind: agentsExpr.ProviderMCP,
			MCP: &MCPToolsetMeta{
				ServiceName:   expr.Provider.MCPService,
				SuiteName:     expr.Provider.MCPToolset,
				Source:        expr.Provider.MCPSource,
				QualifiedName: ref.QualifiedName,
				ConstName:     mcpToolsetConstName(agent, expr.Provider.MCPService, expr.Provider.MCPToolset, ref.Slug),
			},
		}
	case agentsExpr.ProviderRegistry:
		registryName := ""
		if expr.Provider.Registry != nil {
			registryName = expr.Provider.Registry.Name
		}
		return &ToolsetProvider{
			Kind: agentsExpr.ProviderRegistry,
			Registry: &RegistryToolsetMeta{
				RegistryName:             registryName,
				ToolsetName:              expr.Provider.ToolsetName,
				Version:                  expr.Provider.Version,
				QualifiedName:            ref.QualifiedName,
				RegistryClientImportPath: path.Join(genpkg, agent.Service.PathName, "registry", goacodegen.SnakeCase(registryName)),
				RegistryClientAlias:      "reg" + goacodegen.Goify(registryName, false),
			},
		}
	default:
		panic(fmt.Sprintf("unknown provider kind %q for toolset %q", expr.Provider.Kind, expr.Name))
	}
}

func mcpToolsetConstName(agent *Agent, serviceName, suiteName, toolsetSlug string) string {
	constName := fmt.Sprintf(
		"%s%sService%sSuite",
		goacodegen.Goify(agent.Name, true),
		goacodegen.Goify(serviceName, true),
		goacodegen.Goify(suiteName, true),
	)
	if toolsetSlug != naming.SanitizeToken(suiteName, "") {
		constName += goacodegen.Goify(toolsetSlug, true) + "Alias"
	}
	return constName + "ToolsetID"
}
