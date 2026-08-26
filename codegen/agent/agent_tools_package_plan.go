// Package codegen plans every declaration written into exported agent-tool
// packages. The plan is complete before Goa chooses names, so separate files
// and generators cannot choose conflicting Go identifiers.
package codegen

import (
	"fmt"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// agentToolsPackagesPlan stores one generated package for each agent export.
	agentToolsPackagesPlan struct {
		byExport map[agentToolsPackageKey]*agentToolsPackagePlan
	}

	// agentToolsPackagePlan stores the names written for one exported toolset.
	agentToolsPackagePlan struct {
		pkg                     *goacodegen.GeneratedPackage
		reference               *agentir.ToolsetRef
		imports                 []*goacodegen.ImportSpec
		fixed                   map[string]*goacodegen.NameDeclaration
		providerConstructor     *goacodegen.NameDeclaration
		registrationConstructor *goacodegen.NameDeclaration
		tools                   map[string]*plannedAgentToolNames
	}

	// plannedAgentToolNames stores the related names emitted for one tool.
	plannedAgentToolNames struct {
		constant     *goacodegen.NameDeclaration
		payloadAlias *goacodegen.NameDeclaration
		resultAlias  *goacodegen.NameDeclaration
		call         *goacodegen.NameDeclaration
	}

	// agentToolsPackageKey identifies one concrete export without string encoding.
	agentToolsPackageKey struct {
		agentID string
		route   string
	}

	// agentToolsNameOrder keeps collision results stable across generation runs.
	agentToolsNameOrder struct {
		packagePath string
		key         string
	}
)

const (
	agentToolsToolsetName      = "ToolsetName"
	agentToolsServiceName      = "Service"
	agentToolsAgentIDName      = "AgentID"
	agentToolsSpecsName        = "agentToolSpecs"
	agentToolsHintsName        = "installGeneratedHints"
	agentToolsRegistrationName = "NewRegistration"
	agentToolsRuntimePath      = "goa.design/goa-ai/runtime/agent/runtime"
	agentToolsAgentPath        = "goa.design/goa-ai/runtime/agent"
	agentToolsToolsPath        = "goa.design/goa-ai/runtime/agent/tools"
	agentToolsPlannerPath      = "goa.design/goa-ai/runtime/agent/planner"
	agentToolsHintsPath        = "goa.design/goa-ai/runtime/agent/runtime/hints"
)

// planAgentToolsPackages claims every exported agent-tool package and declares
// all names that its helper file writes.
func planAgentToolsPackages(generation *goacodegen.Generation, design *agentir.Design) (*agentToolsPackagesPlan, error) {
	planned := &agentToolsPackagesPlan{byExport: make(map[agentToolsPackageKey]*agentToolsPackagePlan)}
	for _, agent := range design.Agents {
		for _, reference := range agent.ExportedToolsets {
			if reference.AgentToolsImportPath == "" {
				continue
			}
			pkg, err := generation.ClaimPackage(reference.AgentToolsImportPath)
			if err != nil {
				return nil, fmt.Errorf("plan agent %q exported toolset %q package: %w", agent.ID, reference.Name, err)
			}
			packagePlan := &agentToolsPackagePlan{
				pkg:       pkg,
				reference: reference,
				fixed:     make(map[string]*goacodegen.NameDeclaration),
				tools:     make(map[string]*plannedAgentToolNames),
			}
			if err := packagePlan.declare(); err != nil {
				return nil, fmt.Errorf("plan agent %q exported toolset %q names: %w", agent.ID, reference.Name, err)
			}
			planned.byExport[agentToolsPackageKey{agentID: agent.ID, route: reference.QualifiedName}] = packagePlan
		}
	}
	return planned, nil
}

// declare records the fixed helpers and per-tool declarations in one package.
func (p *agentToolsPackagePlan) declare() error {
	fixed := map[goacodegen.PackageNameKind][]string{
		goacodegen.NameConstant: {agentToolsToolsetName, agentToolsServiceName, agentToolsAgentIDName},
		goacodegen.NameFunction: {agentToolsSpecsName, agentToolsRegistrationName},
	}
	if referenceHasHints(p.reference) {
		fixed[goacodegen.NameFunction] = append(fixed[goacodegen.NameFunction], agentToolsHintsName)
	}
	if err := declareExactNames(p.pkg, p.fixed, fixed); err != nil {
		return err
	}

	var err error
	p.providerConstructor, err = p.declarePreferred(
		goacodegen.NameFunction,
		"New"+goacodegen.Goify(p.reference.Agent.Name, true)+"ToolsetRegistration",
		"provider-constructor",
	)
	if err != nil {
		return err
	}
	p.registrationConstructor = p.fixed[agentToolsRegistrationName]

	for _, tool := range toolsetContract(p.reference).Tools {
		qualified := p.reference.Definition.Name + "." + tool.Name
		constant, err := p.declarePreferred(
			goacodegen.NameConstant,
			tool.Name,
			qualified+":constant",
		)
		if err != nil {
			return err
		}
		names := &plannedAgentToolNames{constant: constant}
		names.payloadAlias, err = p.declarePreferred(
			goacodegen.NameType,
			goacodegen.Goify(tool.Name, true)+"Payload",
			qualified+":payload",
		)
		if err != nil {
			return err
		}
		if tool.Return != nil && tool.Return.Type != nil && tool.Return.Type != goaexpr.Empty {
			names.resultAlias, err = p.declarePreferred(
				goacodegen.NameType,
				goacodegen.Goify(tool.Name, true)+"Result",
				qualified+":result",
			)
			if err != nil {
				return err
			}
		}
		names.call, err = p.declarePreferred(
			goacodegen.NameFunction,
			"New"+goacodegen.Goify(tool.Name, true)+"Call",
			qualified+":call",
		)
		if err != nil {
			return err
		}
		p.tools[qualified] = names
	}
	return p.declareImports()
}

// declareImports records every package referenced by the exported helper.
func (p *agentToolsPackagePlan) declareImports() error {
	p.imports = []*goacodegen.ImportSpec{
		goacodegen.NewImport("runtime", agentToolsRuntimePath),
		goacodegen.NewImport("agent", agentToolsAgentPath),
		goacodegen.SimpleImport(agentToolsToolsPath),
		goacodegen.SimpleImport(agentToolsPlannerPath),
	}
	if referenceHasHints(p.reference) {
		p.imports = append(p.imports, goacodegen.NewImport("hints", agentToolsHintsPath))
	}
	if err := requirePackageImports(p.pkg, p.imports); err != nil {
		return err
	}
	specs := goacodegen.NewImport(p.reference.SpecsPackageName+"specs", p.reference.SpecsImportPath)
	if err := p.pkg.ReserveGeneratedImport(specs); err != nil {
		return err
	}
	p.imports = append(p.imports, specs)
	return nil
}

// link copies Goa's final names into the template data for every export.
func (p *agentToolsPackagesPlan) link(data *GeneratorData) error {
	for _, service := range data.Services {
		for _, agent := range service.Agents {
			for _, toolset := range agent.AllToolsets {
				if toolset.AgentToolsImportPath == "" {
					continue
				}
				planned := p.byExport[agentToolsPackageKey{
					agentID: toolset.AgentToolAgentID,
					route:   toolset.QualifiedName,
				}]
				if planned == nil {
					return fmt.Errorf("agent %q toolset %q has no exported package name plan", agent.ID, toolset.QualifiedName)
				}
				toolset.AgentToolsProviderRegistrationConstructor = planned.registrationConstructor.Name()
				if toolset.Kind == ToolsetKindExported {
					toolset.agentTools = planned.renderData(toolset)
				}
			}
		}
	}
	return nil
}

// renderData builds the exact values consumed by the exported helper template.
func (p *agentToolsPackagePlan) renderData(toolset *ToolsetData) *agentToolsetFileData {
	tools := make([]*agentToolRenderData, 0, len(toolset.specs.tools))
	for _, tool := range toolset.specs.tools {
		names := p.tools[tool.Name]
		if names == nil {
			panic(fmt.Sprintf("agent codegen: exported tool %q has no package name plan", tool.Name))
		}
		rendered := &agentToolRenderData{
			toolEntry:    tool,
			ConstName:    names.constant.Name(),
			PayloadAlias: names.payloadAlias.Name(),
			CallFunc:     names.call.Name(),
		}
		if names.resultAlias != nil {
			rendered.ResultAlias = names.resultAlias.Name()
		}
		tools = append(tools, rendered)
	}
	data := &agentToolsetFileData{
		PackageName:             toolset.AgentToolsPackage,
		Imports:                 linkedPlannedImports(p.pkg, p.imports),
		Toolset:                 toolset,
		RuntimeAlias:            p.pkg.ImportName(agentToolsRuntimePath),
		AgentAlias:              p.pkg.ImportName(agentToolsAgentPath),
		ToolsAlias:              p.pkg.ImportName(agentToolsToolsPath),
		PlannerAlias:            p.pkg.ImportName(agentToolsPlannerPath),
		SpecsAlias:              p.pkg.ImportName(p.reference.SpecsImportPath),
		ToolsetName:             p.fixed[agentToolsToolsetName].Name(),
		ServiceName:             p.fixed[agentToolsServiceName].Name(),
		AgentIDName:             p.fixed[agentToolsAgentIDName].Name(),
		SpecsFunc:               p.fixed[agentToolsSpecsName].Name(),
		ProviderConstructor:     p.providerConstructor.Name(),
		RegistrationConstructor: p.registrationConstructor.Name(),
		Tools:                   tools,
	}
	if hints := p.fixed[agentToolsHintsName]; hints != nil {
		data.HintsInstaller = hints.Name()
		data.HintsAlias = p.pkg.ImportName(agentToolsHintsPath)
	}
	return data
}

// declarePreferred records one collision-safe name in this package.
func (p *agentToolsPackagePlan) declarePreferred(kind goacodegen.PackageNameKind, preferred, key string) (*goacodegen.NameDeclaration, error) {
	declaration := goacodegen.NewPreferredName(kind, preferred, goacodegen.ExportedName, p.order(key))
	if err := p.pkg.DeclareName(declaration); err != nil {
		return nil, err
	}
	return declaration, nil
}

// order returns the stable key used to resolve a name collision.
func (p *agentToolsPackagePlan) order(key string) agentToolsNameOrder {
	return agentToolsNameOrder{packagePath: p.pkg.ImportPath(), key: key}
}

// ComparePackageName orders exported agent-tool names by package and purpose.
func (o agentToolsNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(agentToolsNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}
