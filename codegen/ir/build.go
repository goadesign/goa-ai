// Package ir builds the saved services, agents, toolsets, and output paths used
// while goa-ai writes files.
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
	expr *agentsExpr.ToolsetExpr

	service *Service
	agent   *Agent

	serviceName     string
	servicePathName string

	agentName string
	agentSlug string
}

// Build reads one Goa generation and its service plan, then returns all values
// needed to generate agent code. The service plan supplies the exact package
// paths chosen by Goa for this generation.
func Build(generation *goacodegen.Generation, servicePlan *service.Plan) (*Design, error) {
	roots := generation.Roots()
	agentsRoot, err := findAgentsRoot(roots)
	if err != nil {
		return nil, err
	}
	goaRoot, err := findGoaRoot(roots)
	if err != nil {
		return nil, err
	}
	if servicePlan.Root() != goaRoot {
		return nil, fmt.Errorf("agent IR and Goa service plan use different design roots")
	}
	services, err := buildServices(generation, goaRoot, servicePlan)
	if err != nil {
		return nil, err
	}
	servicesByName := make(map[string]*Service, len(services))
	for _, svc := range services {
		servicesByName[svc.Name] = svc
	}

	genpkg := generation.GenPkg()
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
	registries := buildRegistries(agentsRoot)

	refsByToolset := collectToolsetOwnerRefs(agentsRoot, servicesByName)
	toolsets, toolsetsByName, err := buildToolsets(genpkg, agentsRoot, refsByToolset, servicesByName)
	if err != nil {
		return nil, err
	}
	serviceExports, err := buildServiceExports(genpkg, agentsRoot, servicesByName, toolsetsByName)
	if err != nil {
		return nil, err
	}
	for _, export := range serviceExports {
		export.Service.Exports = append(export.Service.Exports, export)
	}
	if err := attachToolsetRefs(genpkg, servicesByName, toolsetsByName, agents); err != nil {
		return nil, err
	}
	if err := linkOwnerReferences(toolsets, serviceExports); err != nil {
		return nil, err
	}

	return &Design{
		Genpkg:         genpkg,
		GoaRoot:        goaRoot,
		AgentsRoot:     agentsRoot,
		Services:       services,
		Agents:         agents,
		Toolsets:       toolsets,
		ServiceExports: serviceExports,
		Completions:    completions,
		Registries:     registries,
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

// buildServices copies each service name together with the exact generated
// directory already chosen by Goa.
func buildServices(generation *goacodegen.Generation, root *goaexpr.RootExpr, plan *service.Plan) ([]*Service, error) {
	out := make([]*Service, 0, len(root.Services))
	for _, svc := range root.Services {
		if svc == nil {
			continue
		}
		serviceImport, _, err := plan.ServicePackageImports(svc)
		if err != nil {
			return nil, err
		}
		pkg := generation.Package(serviceImport.Path)
		if pkg == nil {
			return nil, fmt.Errorf("service %q generated package is not claimed", svc.Name)
		}
		out = append(out, &Service{
			Expr:       svc,
			Name:       svc.Name,
			PathName:   path.Base(serviceImport.Path),
			ImportPath: pkg.ImportPath(),
			Dir:        pkg.OutputDirectory(),
		})
	}
	slices.SortFunc(out, func(a, b *Service) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// buildServiceExports keeps every explicit service export separate from the
// reusable toolset definition and its selected file location.
func buildServiceExports(
	genpkg string,
	root *agentsExpr.RootExpr,
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
) ([]*ToolsetRef, error) {
	exports := make([]*ToolsetRef, 0)
	for _, group := range root.ServiceExports {
		service := servicesByName[group.Service.Name]
		if service == nil {
			return nil, fmt.Errorf("service %q not found for toolset exports", group.Service.Name)
		}
		for _, expr := range group.Toolsets {
			ref, err := newToolsetRef(
				genpkg,
				servicesByName,
				toolsetsByName,
				service,
				nil,
				expr,
				ToolsetRefKindServiceExport,
			)
			if err != nil {
				return nil, err
			}
			exports = append(exports, ref)
		}
	}
	slices.SortFunc(exports, func(a, b *ToolsetRef) int {
		if compared := strings.Compare(a.Service.Name, b.Service.Name); compared != 0 {
			return compared
		}
		return strings.Compare(a.QualifiedName, b.QualifiedName)
	})
	return exports, nil
}

// linkOwnerReferences attaches the complete selected reference after agent and
// service references have been built.
func linkOwnerReferences(toolsets []*Toolset, serviceExports []*ToolsetRef) error {
	for _, toolset := range toolsets {
		selected := toolset.Owner.pendingRef
		if selected == nil {
			return fmt.Errorf("toolset %q has no pending owner reference", toolset.Name)
		}
		switch selected.kind {
		case toolsetOwnerRefUsed, toolsetOwnerRefExported:
			var refs []*ToolsetRef
			if selected.kind == toolsetOwnerRefUsed {
				refs = selected.agent.UsedToolsets
			} else {
				refs = selected.agent.ExportedToolsets
			}
			for _, candidate := range refs {
				if candidate.Expr == selected.expr {
					toolset.Owner.Ref = candidate
					break
				}
			}
			if toolset.Owner.Ref == nil {
				return fmt.Errorf("toolset %q selected agent owner reference was not built", toolset.Name)
			}
		case toolsetOwnerRefServiceExpo:
			for _, candidate := range serviceExports {
				if candidate.Expr == selected.expr && candidate.Service == selected.service {
					toolset.Owner.Ref = candidate
					break
				}
			}
			if toolset.Owner.Ref == nil {
				return fmt.Errorf("toolset %q selected service export was not built", toolset.Name)
			}
		default:
			return fmt.Errorf("toolset %q has unknown owner reference kind %q", toolset.Name, selected.kind)
		}
		toolset.Owner.pendingRef = nil
	}
	return nil
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

// buildRegistries copies registry definitions into name order.
func buildRegistries(root *agentsExpr.RootExpr) []*Registry {
	registries := make([]*Registry, 0, len(root.Registries))
	for _, registry := range root.Registries {
		if registry == nil {
			continue
		}
		registries = append(registries, &Registry{Expr: registry, Name: registry.Name})
	}
	slices.SortFunc(registries, func(a, b *Registry) int {
		return strings.Compare(a.Name, b.Name)
	})
	return registries
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
			expr:            ts,
			service:         svc,
			agent:           agent,
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
	for _, ts := range root.DefiningToolsets() {
		toolsets[ts.Name] = ts
	}
	return toolsets
}

func selectOwner(def *agentsExpr.ToolsetExpr, refs []toolsetOwnerRef, servicesByName map[string]*Service) (Owner, error) {
	ref, ownerKind, err := selectPackageOwnerRef(def, refs)
	if err != nil {
		return Owner{}, err
	}
	if def != nil && def.Provider != nil && def.Provider.Kind == agentsExpr.ProviderMCP && def.Provider.MCPService != "" {
		svc := servicesByName[def.Provider.MCPService]
		if svc == nil {
			return Owner{}, fmt.Errorf("toolset %q references unknown MCP service %q", def.Name, def.Provider.MCPService)
		}
		return Owner{
			Kind:            OwnerKindService,
			pendingRef:      &ref,
			ServiceName:     svc.Name,
			ServicePathName: svc.PathName,
		}, nil
	}
	return Owner{
		Kind:            ownerKind,
		pendingRef:      &ref,
		ServiceName:     ref.serviceName,
		ServicePathName: ref.servicePathName,
		AgentName:       ref.agentName,
		AgentSlug:       ref.agentSlug,
	}, nil
}

// selectPackageOwnerRef chooses the service or agent package where one
// defining toolset's reusable specs are written.
func selectPackageOwnerRef(def *agentsExpr.ToolsetExpr, refs []toolsetOwnerRef) (toolsetOwnerRef, OwnerKind, error) {
	for _, candidate := range []struct {
		kind      toolsetOwnerRefKind
		ownerKind OwnerKind
	}{
		{toolsetOwnerRefExported, OwnerKindAgentExport},
		{toolsetOwnerRefServiceExpo, OwnerKindService},
		{toolsetOwnerRefUsed, OwnerKindService},
	} {
		selected := selectOwnerRefs(refs, candidate.kind)
		if len(selected) == 0 {
			continue
		}
		slices.SortFunc(selected, func(a, b toolsetOwnerRef) int {
			if compared := strings.Compare(a.serviceName, b.serviceName); compared != 0 {
				return compared
			}
			return strings.Compare(a.agentName, b.agentName)
		})
		return selected[0], candidate.ownerKind, nil
	}
	return toolsetOwnerRef{}, "", fmt.Errorf("toolset %q has no owning references", def.Name)
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
	default:
		return nil, fmt.Errorf("unknown toolset owner kind %q for toolset %q", owner.Kind, expr.Name)
	}
	return toolset, nil
}

func attachToolsetRefs(
	genpkg string,
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
	agents []*Agent,
) error {
	exportsByExpr := make(map[*agentsExpr.ToolsetExpr]*ToolsetRef)
	exportsByDefinition := make(map[*Toolset][]*ToolsetRef)
	for _, agent := range agents {
		exported, err := buildAgentToolsetRefs(genpkg, servicesByName, toolsetsByName, agent, agent.Expr.Exported, ToolsetRefKindExported)
		if err != nil {
			return err
		}
		agent.ExportedToolsets = exported
		for _, ref := range exported {
			exportsByExpr[ref.Expr] = ref
			exportsByDefinition[ref.Definition] = append(exportsByDefinition[ref.Definition], ref)
		}
	}
	for _, agent := range agents {
		used, err := buildAgentToolsetRefs(genpkg, servicesByName, toolsetsByName, agent, agent.Expr.Used, ToolsetRefKindUsed)
		if err != nil {
			return err
		}
		for _, ref := range used {
			if err := linkSourceExport(ref, exportsByExpr, exportsByDefinition[ref.Definition]); err != nil {
				return err
			}
		}
		agent.UsedToolsets = used
	}
	return nil
}

// linkSourceExport records the exact agent that executes a used toolset. An
// explicit AgentToolset reference wins. A plain Use is linked only when one
// agent in another service exports the definition.
func linkSourceExport(ref *ToolsetRef, exportsByExpr map[*agentsExpr.ToolsetExpr]*ToolsetRef, exports []*ToolsetRef) error {
	if ref.Expr.Origin != nil && ref.Expr.Origin.Agent != nil {
		selected := exportsByExpr[ref.Expr.Origin]
		if selected == nil {
			return fmt.Errorf("toolset %q selects an agent export that was not built", ref.Name)
		}
		linkAgentToolExport(ref, selected)
		return nil
	}
	candidates := make([]*ToolsetRef, 0, len(exports))
	for _, export := range exports {
		if export.Service != ref.Service {
			candidates = append(candidates, export)
		}
	}
	switch len(candidates) {
	case 0:
		return nil
	case 1:
		linkAgentToolExport(ref, candidates[0])
		return nil
	default:
		return fmt.Errorf("agent %q uses toolset %q exported by more than one agent; select one with AgentToolset", ref.Agent.Name, ref.Name)
	}
}

// linkAgentToolExport records the generated provider package selected by one
// consuming agent. Later planning can then classify and name the consumer
// helper without repeating export selection.
func linkAgentToolExport(ref, selected *ToolsetRef) {
	ref.SourceExport = selected
	ref.QualifiedName = selected.QualifiedName
	ref.AgentToolsPackage = selected.AgentToolsPackage
	ref.AgentToolsImportPath = selected.AgentToolsImportPath
	ref.AgentToolsDir = selected.AgentToolsDir
}

func buildAgentToolsetRefs(
	genpkg string,
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
		ref, err := newToolsetRef(genpkg, servicesByName, toolsetsByName, agent.Service, agent, expr, kind)
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
	servicesByName map[string]*Service,
	toolsetsByName map[string]*Toolset,
	service *Service,
	agent *Agent,
	expr *agentsExpr.ToolsetExpr,
	kind ToolsetRefKind,
) (*ToolsetRef, error) {
	if expr == nil {
		return nil, fmt.Errorf("service %q has a nil toolset reference", service.Name)
	}
	defName := definingToolsetExpr(expr).Name
	def := toolsetsByName[defName]
	if def == nil {
		return nil, fmt.Errorf("toolset %q has no defining IR entry", defName)
	}
	slug := naming.SanitizeToken(expr.Name, "")
	if slug == "" {
		return nil, fmt.Errorf("toolset reference %q has no sanitized identifier", expr.Name)
	}
	sourceService, sourceServiceName := resolveSourceService(servicesByName, service, expr)
	qualifiedName := qualifyServiceToolsetName(service.Name, expr.Name)
	if agent != nil {
		qualifiedName = qualifyToolsetName(agent, expr, kind, sourceServiceName)
	}
	ref := &ToolsetRef{
		Expr:              expr,
		Definition:        def,
		Kind:              kind,
		Name:              expr.Name,
		Slug:              slug,
		QualifiedName:     qualifiedName,
		Description:       expr.Description,
		Tags:              slices.Clone(expr.Tags),
		Service:           service,
		ServiceName:       service.Name,
		Agent:             agent,
		SourceService:     sourceService,
		SourceServiceName: sourceServiceName,
		SpecsPackageName:  def.SpecsPackageName,
		SpecsImportPath:   def.SpecsImportPath,
		SpecsDir:          def.SpecsDir,
	}
	if agent != nil {
		ref.TaskQueue = naming.QueueName(agent.Service.PathName, agent.Slug, slug, "tasks")
		ref.PackageName = slug
		ref.PackageImportPath = path.Join(agent.ImportPath, slug)
		ref.Dir = filepath.Join(agent.Dir, slug)
	}
	if kind == ToolsetRefKindExported {
		ref.AgentToolsPackage = slug
		ref.AgentToolsImportPath = path.Join(agent.ImportPath, "agenttools", slug)
		ref.AgentToolsDir = filepath.Join(agent.Dir, "agenttools", slug)
	}
	if expr.Provider != nil {
		ref.Provider = buildToolsetProvider(genpkg, service, agent, ref, expr)
	}
	return ref, nil
}

// definingToolsetExpr follows references to the declaration that owns the
// reusable tool contract.
func definingToolsetExpr(expr *agentsExpr.ToolsetExpr) *agentsExpr.ToolsetExpr {
	for expr.Origin != nil {
		expr = expr.Origin
	}
	return expr
}

func resolveSourceService(
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
	if !isMCPBacked && len(expr.Tools) > 0 {
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
			qualifiedName = qualifyServiceToolsetName(sourceServiceName, expr.Name)
		}
	}
	return qualifiedName
}

// qualifyServiceToolsetName returns the route owned by a service. Authored dots
// remain part of the toolset name.
func qualifyServiceToolsetName(serviceName, toolsetName string) string {
	return serviceName + "." + toolsetName
}

func buildToolsetProvider(genpkg string, service *Service, agent *Agent, ref *ToolsetRef, expr *agentsExpr.ToolsetExpr) *ToolsetProvider {
	switch expr.Provider.Kind {
	case agentsExpr.ProviderLocal:
		return nil
	case agentsExpr.ProviderMCP:
		constName := ""
		if agent != nil {
			constName = mcpToolsetConstName(agent, expr.Provider.MCPService, expr.Provider.MCPToolset, ref.Slug)
		}
		return &ToolsetProvider{
			Kind: agentsExpr.ProviderMCP,
			MCP: &MCPToolsetMeta{
				ServiceName:   expr.Provider.MCPService,
				SuiteName:     expr.Provider.MCPToolset,
				Source:        expr.Provider.MCPSource,
				QualifiedName: ref.QualifiedName,
				ConstName:     constName,
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
				RegistryClientImportPath: path.Join(genpkg, service.PathName, "registry", goacodegen.SnakeCase(registryName)),
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
