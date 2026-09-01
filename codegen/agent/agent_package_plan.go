// Package codegen plans every package-level name written into an agent package.
// Goa chooses the final spelling once for the whole package before any template
// renders a file, so declarations in separate files cannot accidentally collide.
package codegen

import (
	"fmt"
	"path"
	"slices"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// agentPackagesPlan stores the names written into every generated agent package.
	agentPackagesPlan struct {
		byAgent map[string]*agentPackagePlan
	}

	// agentPackagePlan stores one generated agent package and its declarations.
	agentPackagePlan struct {
		pkg                 *goacodegen.GeneratedPackage
		fixed               map[string]*goacodegen.NameDeclaration
		configType          *goacodegen.NameDeclaration
		structType          *goacodegen.NameDeclaration
		constructor         *goacodegen.NameDeclaration
		register            *goacodegen.NameDeclaration
		usedOptions         *goacodegen.NameDeclaration
		mcp                 map[string]*goacodegen.NameDeclaration
		used                map[string]*plannedUsedToolsetNames
		agentToolsConsumers map[string]*goacodegen.NameDeclaration
		agentToolsImports   map[string]string
		specsImportPaths    map[string]string
		helperImportPaths   map[string]string
		implementationPaths []string
		configPaths         []string
		registryPaths       []string
		definitionAgentIDs  []string
	}

	// agentPackageFilesData contains the imports and package names used by the
	// three files in an agent package.
	agentPackageFilesData struct {
		implementation *agentImplementationFileData
		config         *agentConfigFileData
		registry       *agentRegistryImports
	}

	// agentImplementationFileData contains the chosen import names used by
	// agent.go.
	agentImplementationFileData struct {
		*AgentData
		// Imports contains the packages used by agent.go.
		Imports []*goacodegen.ImportSpec
		// AgentAlias names the package that defines agent identifiers.
		AgentAlias string
		// PlannerAlias names the package that defines planner implementations.
		PlannerAlias string
		// RuntimeAlias names the package that runs generated agents.
		RuntimeAlias string
		// ToolSpecsAlias names this agent's aggregate generated tool package.
		ToolSpecsAlias string
		// ToolsAlias names the runtime tool contract package.
		ToolsAlias string
		// ChildDefinitions contains every agent definition reachable through an
		// agent tool, including definitions used only by nested child agents.
		ChildDefinitions []*agentDefinitionFileData
	}

	// agentDefinitionFileData contains one reachable agent contract rendered
	// into the caller's immutable definition graph.
	agentDefinitionFileData struct {
		*AgentData
		// ToolSpecsAlias names this agent's aggregate generated tool package.
		ToolSpecsAlias string
	}

	// agentConfigFileData contains the chosen import names used by config.go.
	agentConfigFileData struct {
		*AgentData
		// Imports contains the packages used by config.go.
		Imports []*goacodegen.ImportSpec
		// ErrorsAlias names the standard errors package.
		ErrorsAlias string
		// FmtAlias names the standard formatting package.
		FmtAlias string
		// MCPRuntimeAlias names the package that calls MCP servers.
		MCPRuntimeAlias string
		// ModelAlias names the package that defines model clients.
		ModelAlias string
		// PlannerAlias names the package that defines planner implementations.
		PlannerAlias string
		// RuntimeAlias names the package that runs generated agents.
		RuntimeAlias string
	}

	// agentRegistryImports contains the chosen import names used by registry.go.
	agentRegistryImports struct {
		// Imports contains the packages used by registry.go.
		Imports []*goacodegen.ImportSpec
		// ContextAlias names the standard context package.
		ContextAlias string
		// EngineAlias names the workflow engine contract package.
		EngineAlias string
		// ErrorsAlias names the standard errors package.
		ErrorsAlias string
		// FmtAlias names the standard formatting package.
		FmtAlias string
		// HintsAlias names the package that compiles generated hint templates.
		HintsAlias string
		// RuntimeAlias names the package that registers and runs agents.
		RuntimeAlias string
		// TimeAlias names the standard time package.
		TimeAlias string
		// ToolsAlias names the package that identifies tools.
		ToolsAlias string
		// ToolSpecsAlias names this agent's aggregate tool specification package.
		ToolSpecsAlias string
		// AgentVar names the agent value created during registration.
		AgentVar string
	}

	// plannedUsedToolsetNames stores the names written for one local toolset route.
	plannedUsedToolsetNames struct {
		routeConstant      *goacodegen.NameDeclaration
		executorOption     *goacodegen.NameDeclaration
		materializerOption *goacodegen.NameDeclaration
		hintInstaller      *goacodegen.NameDeclaration
	}

	// agentPackageNameOrder keeps collision results stable when design order changes.
	agentPackageNameOrder struct {
		packagePath string
		key         string
	}
)

const (
	agentIDName              = "AgentID"
	workflowNameName         = "WorkflowName"
	defaultTaskQueueName     = "DefaultTaskQueue"
	planActivityName         = "PlanActivity"
	resumeActivityName       = "ResumeActivity"
	executeToolActivityName  = "ExecuteToolActivity"
	definitionName           = "Definition"
	agentDefinitionValueName = "agentDefinition"
	newClientName            = "NewClient"
	usedToolsetOptionsName   = "usedToolsetRegistrationOptions"
	registerUsedToolsetsName = "RegisterUsedToolsets"
	agentRuntimeImportPath   = "goa.design/goa-ai/runtime/agent"
	engineImportPath         = "goa.design/goa-ai/runtime/agent/engine"
	hintsImportPath          = "goa.design/goa-ai/runtime/agent/runtime/hints"
	mcpRuntimeImportPath     = "goa.design/goa-ai/runtime/mcp"
	modelImportPath          = "goa.design/goa-ai/runtime/agent/model"
	plannerImportPath        = "goa.design/goa-ai/runtime/agent/planner"
	runtimeImportPath        = "goa.design/goa-ai/runtime/agent/runtime"
	toolsImportPath          = "goa.design/goa-ai/runtime/agent/tools"
)

// planAgentPackages claims each agent package and declares every package-level
// name written by its agent, config, registry, and agent-tool client files.
func planAgentPackages(generation *goacodegen.Generation, design *agentir.Design) (*agentPackagesPlan, error) {
	planned := &agentPackagesPlan{byAgent: make(map[string]*agentPackagePlan)}
	for _, agent := range design.Agents {
		pkg, err := generation.ClaimPackage(agent.ImportPath)
		if err != nil {
			return nil, fmt.Errorf("plan agent %q package: %w", agent.ID, err)
		}
		packagePlan := &agentPackagePlan{
			pkg:                 pkg,
			fixed:               make(map[string]*goacodegen.NameDeclaration),
			mcp:                 make(map[string]*goacodegen.NameDeclaration),
			used:                make(map[string]*plannedUsedToolsetNames),
			agentToolsConsumers: make(map[string]*goacodegen.NameDeclaration),
			agentToolsImports:   make(map[string]string),
			specsImportPaths:    make(map[string]string),
			helperImportPaths:   make(map[string]string),
			definitionAgentIDs:  reachableAgentIDs(agent),
		}
		if err := packagePlan.declare(agent); err != nil {
			return nil, fmt.Errorf("plan agent %q package names: %w", agent.ID, err)
		}
		planned.byAgent[agent.ID] = packagePlan
	}
	return planned, nil
}

// link copies Goa's final package names into the data rendered by templates.
func (p *agentPackagesPlan) link(data *GeneratorData) error {
	agentsByID := make(map[string]*AgentData)
	for _, service := range data.Services {
		for _, agent := range service.Agents {
			agentsByID[agent.ID] = agent
		}
	}
	for _, service := range data.Services {
		for _, agent := range service.Agents {
			planned := p.byAgent[agent.ID]
			if planned == nil {
				return fmt.Errorf("agent %q has no package name plan", agent.ID)
			}
			planned.link(agent, agentsByID)
		}
	}
	return nil
}

// declare records the fixed and design-derived names written for one agent.
func (p *agentPackagePlan) declare(agent *agentir.Agent) error {
	if err := p.declareFixedNames(map[goacodegen.PackageNameKind][]string{
		goacodegen.NameConstant: {
			agentIDName,
			workflowNameName,
			defaultTaskQueueName,
			planActivityName,
			resumeActivityName,
			executeToolActivityName,
		},
		goacodegen.NameFunction: {definitionName, newClientName},
		goacodegen.NameVariable: {agentDefinitionValueName},
	}); err != nil {
		return err
	}

	var err error
	p.structType, err = p.declarePreferred(goacodegen.NameType, agent.StructName, goacodegen.ExportedName, agent.ID+":agent")
	if err != nil {
		return err
	}
	p.configType, err = p.declarePreferred(goacodegen.NameType, agent.ConfigType, goacodegen.ExportedName, agent.ID+":config")
	if err != nil {
		return err
	}
	p.constructor, err = p.pkg.DeclareDependentName(
		goacodegen.NameFunction,
		p.structType,
		"New",
		"",
		p.order(agent.ID+":constructor"),
	)
	if err != nil {
		return err
	}
	p.register, err = p.pkg.DeclareDependentName(
		goacodegen.NameFunction,
		p.structType,
		"Register",
		"",
		p.order(agent.ID+":register"),
	)
	if err != nil {
		return err
	}

	if err := p.declareMCPNames(agent); err != nil {
		return err
	}
	if err := p.declareUsedToolsetNames(agent); err != nil {
		return err
	}
	if err := p.declareSpecsImports(agent); err != nil {
		return err
	}
	if err := p.declareAgentToolsConsumerNames(agent); err != nil {
		return err
	}
	return p.declareFileImports(agent)
}

// declareFileImports records each package used by agent.go, config.go, and
// registry.go before Goa chooses import names.
func (p *agentPackagePlan) declareFileImports(agent *agentir.Agent) error {
	p.implementationPaths = []string{agentRuntimeImportPath, runtimeImportPath, plannerImportPath, toolsImportPath}
	if len(agent.UsedToolsets)+len(agent.ExportedToolsets) > 0 && agent.ToolSpecsImportPath != "" {
		p.implementationPaths = append(p.implementationPaths, agent.ToolSpecsImportPath)
	}
	for _, childID := range p.definitionAgentIDs {
		child := agentByID(agent, childID)
		if child != nil && len(child.UsedToolsets)+len(child.ExportedToolsets) > 0 && child.ToolSpecsImportPath != "" {
			p.implementationPaths = append(p.implementationPaths, child.ToolSpecsImportPath)
		}
	}
	p.configPaths = []string{"errors", plannerImportPath}
	p.registryPaths = []string{
		"context",
		"errors",
		"fmt",
		engineImportPath,
		runtimeImportPath,
		"time",
	}

	if agent.Expr.RunPolicy != nil && agent.Expr.RunPolicy.History != nil &&
		agent.Expr.RunPolicy.History.Mode == agentexpr.HistoryModeCompress {
		p.configPaths = append(p.configPaths, modelImportPath, runtimeImportPath)
	}
	if agentHasMCPToolset(agent) {
		p.configPaths = append(p.configPaths, "fmt", mcpRuntimeImportPath)
	}
	if agentHasDirectHints(agent) {
		p.registryPaths = append(p.registryPaths, toolsImportPath, hintsImportPath)
	}
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		if reference.Provider != nil && reference.Provider.Kind == agentexpr.ProviderMCP &&
			reference.PackageImportPath != "" {
			p.registryPaths = append(p.registryPaths, reference.PackageImportPath)
		}
	}
	for _, reference := range agent.UsedToolsets {
		if reference.AgentToolsImportPath == "" && reference.SpecsImportPath != "" {
			p.registryPaths = append(p.registryPaths, reference.SpecsImportPath)
		}
	}
	if len(agent.UsedToolsets)+len(agent.ExportedToolsets) > 0 && agent.ToolSpecsImportPath != "" {
		p.registryPaths = append(p.registryPaths, agent.ToolSpecsImportPath)
	}

	requests := map[string]string{
		"context":              "context",
		"errors":               "errors",
		"fmt":                  "fmt",
		"strings":              "strings",
		"time":                 "time",
		agentRuntimeImportPath: "agent",
		engineImportPath:       "engine",
		hintsImportPath:        "hints",
		mcpRuntimeImportPath:   "mcpruntime",
		modelImportPath:        "model",
		plannerImportPath:      "planner",
		runtimeImportPath:      "agentsruntime",
		toolsImportPath:        "tools",
	}
	explicit := map[string]bool{
		agentRuntimeImportPath: true,
		hintsImportPath:        true,
		mcpRuntimeImportPath:   true,
		modelImportPath:        true,
		runtimeImportPath:      true,
	}
	if agent.ToolSpecsImportPath != "" {
		requests[agent.ToolSpecsImportPath] = agent.ToolSpecsPackage
		explicit[agent.ToolSpecsImportPath] = true
	}
	for _, childID := range p.definitionAgentIDs {
		child := agentByID(agent, childID)
		if child == nil || child.ToolSpecsImportPath == "" {
			continue
		}
		requests[child.ToolSpecsImportPath] = child.ToolSpecsPackage
		explicit[child.ToolSpecsImportPath] = true
	}
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		if reference.PackageImportPath != "" {
			requests[reference.PackageImportPath] = reference.PackageName
			explicit[reference.PackageImportPath] = true
		}
		if reference.SpecsImportPath != "" {
			requests[reference.SpecsImportPath] = reference.SpecsPackageName
			explicit[reference.SpecsImportPath] = true
		}
	}
	for _, importPath := range uniqueImportPaths(p.implementationPaths, p.configPaths, p.registryPaths) {
		if err := p.pkg.ReserveGeneratedImport(plannedImport(requests[importPath], importPath, explicit[importPath])); err != nil {
			return err
		}
	}
	return nil
}

// plannedImport omits an explicit qualifier when the package path already
// supplies the requested name.
func plannedImport(preferred, importPath string, explicit bool) *goacodegen.ImportSpec {
	if !explicit && (preferred == "" || preferred == path.Base(importPath)) {
		return goacodegen.SimpleImport(importPath)
	}
	return goacodegen.NewImport(preferred, importPath)
}

// declareSpecsImports records the generated specs packages referenced by the
// agent registry so their aliases are chosen with the package's declarations.
func (p *agentPackagePlan) declareSpecsImports(agent *agentir.Agent) error {
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		needsHelper := reference.Provider != nil && reference.Provider.Kind == agentexpr.ProviderMCP
		if needsHelper && reference.PackageImportPath != "" {
			if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(reference.PackageName, reference.PackageImportPath)); err != nil {
				return err
			}
			p.helperImportPaths[reference.QualifiedName] = reference.PackageImportPath
		}
		if reference.AgentToolsImportPath != "" || reference.SpecsImportPath == "" {
			continue
		}
		if _, ok := p.specsImportPaths[reference.QualifiedName]; ok {
			continue
		}
		if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(reference.SpecsPackageName, reference.SpecsImportPath)); err != nil {
			return err
		}
		p.specsImportPaths[reference.QualifiedName] = reference.SpecsImportPath
	}
	return nil
}

// declareFixedNames records public helpers whose names are part of the agent API.
func (p *agentPackagePlan) declareFixedNames(names map[goacodegen.PackageNameKind][]string) error {
	for kind, values := range names {
		for _, name := range values {
			declaration := goacodegen.NewExactName(kind, name)
			if err := p.pkg.DeclareName(declaration); err != nil {
				return err
			}
			p.fixed[name] = declaration
		}
	}
	return nil
}

// declareMCPNames records one constant for each distinct MCP route used by the agent.
func (p *agentPackagePlan) declareMCPNames(agent *agentir.Agent) error {
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		if reference.Provider == nil || reference.Provider.Kind != agentexpr.ProviderMCP {
			continue
		}
		meta := reference.Provider.MCP
		if p.mcp[meta.QualifiedName] != nil {
			continue
		}
		declaration, err := p.declarePreferred(
			goacodegen.NameConstant,
			meta.ConstName,
			goacodegen.ExportedName,
			agent.ID+":mcp:"+meta.QualifiedName,
		)
		if err != nil {
			return err
		}
		p.mcp[meta.QualifiedName] = declaration
	}
	return nil
}

// declareUsedToolsetNames records routes and options for toolsets registered
// directly by RegisterUsedToolsets.
func (p *agentPackagePlan) declareUsedToolsetNames(agent *agentir.Agent) error {
	for _, reference := range agent.UsedToolsets {
		if !registersUsedToolset(reference) {
			continue
		}
		if len(p.used) == 0 {
			var err error
			p.usedOptions, err = p.declarePreferred(
				goacodegen.NameType,
				usedToolsetOptionsName,
				goacodegen.UnexportedName,
				agent.ID+":used:options",
			)
			if err != nil {
				return err
			}
			if err := p.declareFixedNames(map[goacodegen.PackageNameKind][]string{
				goacodegen.NameFunction: {registerUsedToolsetsName},
			}); err != nil {
				return err
			}
		}
		base := goacodegen.Goify(reference.Slug, true)
		names := &plannedUsedToolsetNames{}
		var err error
		names.routeConstant, err = p.declarePreferred(
			goacodegen.NameConstant,
			base+"ToolsetName",
			goacodegen.ExportedName,
			agent.ID+":used:"+reference.QualifiedName+":route",
		)
		if err != nil {
			return err
		}
		names.executorOption, err = p.declarePreferred(
			goacodegen.NameFunction,
			"With"+base+"Executor",
			goacodegen.ExportedName,
			agent.ID+":used:"+reference.QualifiedName+":executor",
		)
		if err != nil {
			return err
		}
		names.materializerOption, err = p.declarePreferred(
			goacodegen.NameFunction,
			"With"+base+"ResultMaterializer",
			goacodegen.ExportedName,
			agent.ID+":used:"+reference.QualifiedName+":materializer",
		)
		if err != nil {
			return err
		}
		if referenceHasHints(reference) {
			names.hintInstaller, err = p.declarePreferred(
				goacodegen.NameFunction,
				"install"+base+"GeneratedHints",
				goacodegen.UnexportedName,
				agent.ID+":used:"+reference.QualifiedName+":hints",
			)
			if err != nil {
				return err
			}
		}
		p.used[reference.QualifiedName] = names
	}
	return nil
}

// declareAgentToolsConsumerNames records constructors that delegate to an
// exported agent toolset package.
func (p *agentPackagePlan) declareAgentToolsConsumerNames(agent *agentir.Agent) error {
	runtimePlanned := false
	for _, reference := range agent.UsedToolsets {
		if reference.AgentToolsImportPath == "" || len(toolsetContract(reference).Tools) == 0 {
			continue
		}
		preferred := "New" + goacodegen.Goify(agent.Name, true) + goacodegen.Goify(reference.Slug, true) + "AgentToolsetRegistration"
		declaration, err := p.declarePreferred(
			goacodegen.NameFunction,
			preferred,
			goacodegen.ExportedName,
			agent.ID+":agenttools:"+reference.QualifiedName,
		)
		if err != nil {
			return err
		}
		if !runtimePlanned {
			if err := p.pkg.RequireImport(goacodegen.NewImport("runtime", agentToolsRuntimePath)); err != nil {
				return err
			}
			runtimePlanned = true
		}
		if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(reference.AgentToolsPackage, reference.AgentToolsImportPath)); err != nil {
			return err
		}
		p.agentToolsConsumers[reference.QualifiedName] = declaration
		p.agentToolsImports[reference.QualifiedName] = reference.AgentToolsImportPath
	}
	return nil
}

// declarePreferred records a generated name with a stable order key.
func (p *agentPackagePlan) declarePreferred(
	kind goacodegen.PackageNameKind,
	preferred string,
	visibility goacodegen.PackageNameVisibility,
	key string,
) (*goacodegen.NameDeclaration, error) {
	declaration := goacodegen.NewPreferredName(kind, preferred, visibility, p.order(key))
	if err := p.pkg.DeclareName(declaration); err != nil {
		return nil, err
	}
	return declaration, nil
}

// order returns the stable package and design key used for one declaration.
func (p *agentPackagePlan) order(key string) agentPackageNameOrder {
	return agentPackageNameOrder{packagePath: p.pkg.ImportPath(), key: key}
}

// link stores the final names used by all templates for one agent package.
func (p *agentPackagePlan) link(agent *AgentData, agentsByID map[string]*AgentData) {
	agent.StructName = p.structType.Name()
	agent.ConfigType = p.configType.Name()
	agent.PackageNames = AgentPackageNames{
		AgentID:             p.fixed[agentIDName].Name(),
		WorkflowName:        p.fixed[workflowNameName].Name(),
		DefaultTaskQueue:    p.fixed[defaultTaskQueueName].Name(),
		PlanActivity:        p.fixed[planActivityName].Name(),
		ResumeActivity:      p.fixed[resumeActivityName].Name(),
		ExecuteToolActivity: p.fixed[executeToolActivityName].Name(),
		Constructor:         p.constructor.Name(),
		Definition:          p.fixed[definitionName].Name(),
		DefinitionValue:     p.fixed[agentDefinitionValueName].Name(),
		NewClient:           p.fixed[newClientName].Name(),
		Register:            p.register.Name(),
	}
	if p.usedOptions != nil {
		agent.PackageNames.UsedToolsetOptions = p.usedOptions.Name()
		agent.PackageNames.RegisterUsedToolsets = p.fixed[registerUsedToolsetsName].Name()
	}
	agent.packageFiles = p.linkFileData(agent, agentsByID)
	for _, toolset := range agent.AllToolsets {
		if importPath := p.helperImportPaths[toolset.QualifiedName]; importPath != "" {
			toolset.AgentPackageHelperAlias = p.pkg.ImportName(importPath)
		}
		if importPath := p.specsImportPaths[toolset.QualifiedName]; importPath != "" {
			toolset.AgentPackageSpecsAlias = p.pkg.ImportName(importPath)
		}
		if toolset.MCP != nil {
			toolset.MCP.ConstName = p.mcp[toolset.MCP.QualifiedName].Name()
		}
		if names := p.used[toolset.QualifiedName]; names != nil {
			toolset.RegistrationNameConst = names.routeConstant.Name()
			toolset.ExecutorOption = names.executorOption.Name()
			toolset.ResultMaterializerOption = names.materializerOption.Name()
			if names.hintInstaller != nil {
				toolset.GeneratedHintsInstaller = names.hintInstaller.Name()
			}
		}
		if declaration := p.agentToolsConsumers[toolset.QualifiedName]; declaration != nil {
			toolset.AgentToolsRegistrationConstructor = declaration.Name()
			providerPath := p.agentToolsImports[toolset.QualifiedName]
			toolset.agentToolsConsumerImports = []*goacodegen.ImportSpec{
				p.pkg.Import(agentToolsRuntimePath),
				p.pkg.Import(providerPath),
			}
			toolset.agentToolsRuntimeAlias = p.pkg.ImportName(agentToolsRuntimePath)
			toolset.agentToolsProviderAlias = p.pkg.ImportName(providerPath)
		}
	}
}

// linkFileData copies the selected import lines and qualifiers into the data
// used by each agent package template.
func (p *agentPackagePlan) linkFileData(agent *AgentData, agentsByID map[string]*AgentData) *agentPackageFilesData {
	implementation := &agentImplementationFileData{
		AgentData:    agent,
		Imports:      p.linkImports(p.implementationPaths),
		AgentAlias:   p.pkg.ImportName(agentRuntimeImportPath),
		PlannerAlias: p.pkg.ImportName(plannerImportPath),
		RuntimeAlias: p.pkg.ImportName(runtimeImportPath),
		ToolsAlias:   p.pkg.ImportName(toolsImportPath),
	}
	if importPathIncluded(p.implementationPaths, agent.ToolSpecsImportPath) {
		implementation.ToolSpecsAlias = p.pkg.ImportName(agent.ToolSpecsImportPath)
	}
	for _, childID := range p.definitionAgentIDs {
		child := agentsByID[childID]
		if child == nil {
			panic(fmt.Sprintf("agent codegen: reachable agent %q has no generator data", childID))
		}
		definition := &agentDefinitionFileData{AgentData: child}
		if importPathIncluded(p.implementationPaths, child.ToolSpecsImportPath) {
			definition.ToolSpecsAlias = p.pkg.ImportName(child.ToolSpecsImportPath)
		}
		implementation.ChildDefinitions = append(implementation.ChildDefinitions, definition)
	}
	config := &agentConfigFileData{
		AgentData:    agent,
		Imports:      p.linkImports(p.configPaths),
		ErrorsAlias:  p.pkg.ImportName("errors"),
		PlannerAlias: p.pkg.ImportName(plannerImportPath),
	}
	if importPathIncluded(p.configPaths, "fmt") {
		config.FmtAlias = p.pkg.ImportName("fmt")
		config.MCPRuntimeAlias = p.pkg.ImportName(mcpRuntimeImportPath)
	}
	if importPathIncluded(p.configPaths, modelImportPath) {
		config.ModelAlias = p.pkg.ImportName(modelImportPath)
		config.RuntimeAlias = p.pkg.ImportName(runtimeImportPath)
	}
	registryPaths := p.registryPaths
	if len(agent.Tools) == 0 {
		registryPaths = importPathsWithout(registryPaths, agent.ToolSpecsImportPath)
	}
	registry := &agentRegistryImports{
		Imports:      p.linkImports(registryPaths),
		ContextAlias: p.pkg.ImportName("context"),
		EngineAlias:  p.pkg.ImportName(engineImportPath),
		ErrorsAlias:  p.pkg.ImportName("errors"),
		FmtAlias:     p.pkg.ImportName("fmt"),
		RuntimeAlias: p.pkg.ImportName(runtimeImportPath),
		TimeAlias:    p.pkg.ImportName("time"),
	}
	registry.AgentVar = localNameForImports("agent", registry.Imports)
	if importPathIncluded(p.registryPaths, hintsImportPath) {
		registry.HintsAlias = p.pkg.ImportName(hintsImportPath)
		registry.ToolsAlias = p.pkg.ImportName(toolsImportPath)
	}
	if importPathIncluded(registryPaths, agent.ToolSpecsImportPath) {
		registry.ToolSpecsAlias = p.pkg.ImportName(agent.ToolSpecsImportPath)
	}
	return &agentPackageFilesData{
		implementation: implementation,
		config:         config,
		registry:       registry,
	}
}

// reachableAgentIDs returns every agent reachable through generated agent
// tools. The result excludes the root and remains stable when declarations are
// reordered.
func reachableAgentIDs(root *agentir.Agent) []string {
	byID := make(map[string]*agentir.Agent)
	queue := []*agentir.Agent{root}
	seen := map[string]struct{}{root.ID: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, reference := range current.UsedToolsets {
			if reference.SourceExport == nil || reference.SourceExport.Agent == nil {
				continue
			}
			child := reference.SourceExport.Agent
			if _, ok := seen[child.ID]; ok {
				continue
			}
			seen[child.ID] = struct{}{}
			byID[child.ID] = child
			queue = append(queue, child)
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// agentByID finds one reachable agent from the graph rooted at root.
func agentByID(root *agentir.Agent, id string) *agentir.Agent {
	queue := []*agentir.Agent{root}
	seen := make(map[string]struct{})
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.ID == id {
			return current
		}
		if _, ok := seen[current.ID]; ok {
			continue
		}
		seen[current.ID] = struct{}{}
		for _, reference := range current.UsedToolsets {
			if reference.SourceExport != nil && reference.SourceExport.Agent != nil {
				queue = append(queue, reference.SourceExport.Agent)
			}
		}
	}
	return nil
}

// linkImports returns the final import lines for one generated file.
func (p *agentPackagePlan) linkImports(paths []string) []*goacodegen.ImportSpec {
	imports := make([]*goacodegen.ImportSpec, 0, len(paths))
	for _, importPath := range uniqueImportPaths(paths) {
		imports = append(imports, p.pkg.Import(importPath))
	}
	return imports
}

// registersUsedToolset reports whether RegisterUsedToolsets owns this reference.
func registersUsedToolset(reference *agentir.ToolsetRef) bool {
	return reference.AgentToolsImportPath == "" &&
		(reference.Provider == nil || reference.Provider.Kind != agentexpr.ProviderMCP)
}

// referenceHasHints reports whether generated registration installs call or result hints.
func referenceHasHints(reference *agentir.ToolsetRef) bool {
	for _, tool := range toolsetContract(reference).Tools {
		if tool.CallHintTemplate != "" || tool.ResultHintTemplate != "" {
			return true
		}
	}
	return false
}

// agentHasMCPToolset reports whether the agent config accepts an MCP caller.
func agentHasMCPToolset(agent *agentir.Agent) bool {
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		if reference.Provider != nil && reference.Provider.Kind == agentexpr.ProviderMCP {
			return true
		}
	}
	return false
}

// agentHasDirectHints reports whether the registry installs hints for a
// directly registered toolset.
func agentHasDirectHints(agent *agentir.Agent) bool {
	for _, reference := range agent.UsedToolsets {
		if registersUsedToolset(reference) && referenceHasHints(reference) {
			return true
		}
	}
	return false
}

// uniqueImportPaths removes repeated paths while keeping their first position.
func uniqueImportPaths(groups ...[]string) []string {
	var paths []string
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, importPath := range group {
			if _, ok := seen[importPath]; ok {
				continue
			}
			seen[importPath] = struct{}{}
			paths = append(paths, importPath)
		}
	}
	return paths
}

// importPathsWithout returns paths without the package that the linked file
// does not use.
func importPathsWithout(paths []string, excluded string) []string {
	filtered := make([]string, 0, len(paths))
	for _, importPath := range paths {
		if importPath != excluded {
			filtered = append(filtered, importPath)
		}
	}
	return filtered
}

// importPathIncluded reports whether one file uses importPath.
func importPathIncluded(paths []string, importPath string) bool {
	for _, candidate := range paths {
		if candidate == importPath {
			return true
		}
	}
	return false
}

// localNameForImports returns a local name that does not hide a package used
// later in the same function.
func localNameForImports(preferred string, imports []*goacodegen.ImportSpec) string {
	used := make(map[string]struct{}, len(imports))
	for _, spec := range imports {
		used[pkgImportName(spec)] = struct{}{}
	}
	if _, exists := used[preferred]; !exists {
		return preferred
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s%d", preferred, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

// pkgImportName returns the qualifier written for one linked import.
func pkgImportName(spec *goacodegen.ImportSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return path.Base(spec.Path)
}

// toolsetContract returns the definition whose tools generated code exposes.
func toolsetContract(reference *agentir.ToolsetRef) *agentexpr.ToolsetExpr {
	return reference.Definition.Expr
}

// ComparePackageName orders generated agent package names by package and role.
func (o agentPackageNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(agentPackageNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}
