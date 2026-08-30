// Package codegen plans the declarations and imports written into each agent's
// aggregate tool specifications package. Templates receive only final names
// chosen by Goa before source generation starts.
package codegen

import (
	"fmt"
	"path/filepath"

	agentir "goa.design/goa-ai/codegen/ir"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// aggregateSpecsPackagesPlan stores one aggregate package for each agent
	// that has compile-time tool specifications.
	aggregateSpecsPackagesPlan struct {
		byAgent map[string]*aggregateSpecsPackagePlan
	}

	// aggregateSpecsPackagePlan stores the declarations, imports, and final
	// render data for one agent's aggregate specifications package.
	aggregateSpecsPackagePlan struct {
		pkg              *goacodegen.GeneratedPackage
		functions        map[string]*goacodegen.NameDeclaration
		toolsetImports   []string
		linkedRenderData *aggregateSpecsFileData
	}
)

const (
	aggregateSpecsFuncName          = "Specs"
	aggregateNamesFuncName          = "Names"
	aggregateRequiredLabelsFuncName = "RequiredLabels"
	aggregateSpecFuncName           = "Spec"
	aggregateMetadataFuncName       = "Metadata"
	aggregateMetadataByNameFuncName = "MetadataByName"
	aggregatePolicyImportPath       = "goa.design/goa-ai/runtime/agent/policy"
	aggregateToolsImportPath        = "goa.design/goa-ai/runtime/agent/tools"
)

// planAggregateSpecsPackages claims every emitted aggregate package and records
// its declarations and imports before Goa chooses their final names.
func planAggregateSpecsPackages(generation *goacodegen.Generation, design *agentir.Design, mcpRoot *mcpexpr.RootExpr) (*aggregateSpecsPackagesPlan, error) {
	planned := &aggregateSpecsPackagesPlan{byAgent: make(map[string]*aggregateSpecsPackagePlan)}
	for _, agent := range design.Agents {
		packagePlan, err := planAggregateSpecsPackage(generation, agent, mcpRoot)
		if err != nil {
			return nil, fmt.Errorf("plan agent %q aggregate specs package: %w", agent.ID, err)
		}
		if packagePlan != nil {
			planned.byAgent[agent.ID] = packagePlan
		}
	}
	return planned, nil
}

// planAggregateSpecsPackage returns nil when the agent has no compile-time
// tools and therefore does not emit an aggregate Go file.
func planAggregateSpecsPackage(generation *goacodegen.Generation, agent *agentir.Agent, mcpRoot *mcpexpr.RootExpr) (*aggregateSpecsPackagePlan, error) {
	seen := make(map[string]struct{})
	imports := make([]*goacodegen.ImportSpec, 0)
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		tools, err := toolExpressionsForReference(mcpRoot, reference)
		if err != nil {
			return nil, err
		}
		if len(tools) == 0 {
			continue
		}
		if _, ok := seen[reference.SpecsImportPath]; ok {
			continue
		}
		seen[reference.SpecsImportPath] = struct{}{}
		imports = append(imports, goacodegen.NewImport(reference.SpecsPackageName, reference.SpecsImportPath))
	}
	if len(imports) == 0 {
		return nil, nil
	}

	pkg, err := generation.ClaimPackage(agent.ToolSpecsImportPath)
	if err != nil {
		return nil, err
	}
	functions := make(map[string]*goacodegen.NameDeclaration)
	for _, name := range []string{
		aggregateSpecsFuncName,
		aggregateNamesFuncName,
		aggregateRequiredLabelsFuncName,
		aggregateSpecFuncName,
		aggregateMetadataFuncName,
		aggregateMetadataByNameFuncName,
	} {
		declaration := goacodegen.NewExactName(goacodegen.NameFunction, name)
		if err := pkg.DeclareName(declaration); err != nil {
			return nil, err
		}
		functions[name] = declaration
	}
	if err := requirePackageImports(pkg, []*goacodegen.ImportSpec{
		goacodegen.SimpleImport(aggregatePolicyImportPath),
		goacodegen.NewImport("tools", aggregateToolsImportPath),
	}); err != nil {
		return nil, err
	}
	toolsetImports := make([]string, 0, len(imports))
	for _, spec := range imports {
		if err := pkg.ReserveGeneratedImport(spec); err != nil {
			return nil, err
		}
		toolsetImports = append(toolsetImports, spec.Path)
	}
	return &aggregateSpecsPackagePlan{
		pkg:            pkg,
		functions:      functions,
		toolsetImports: toolsetImports,
	}, nil
}

// link builds the complete aggregate file data from Goa's final declaration
// and import names and the linked tool specifications.
func (p *aggregateSpecsPackagesPlan) link(data *GeneratorData) error {
	for _, service := range data.Services {
		for _, agent := range service.Agents {
			packagePlan := p.byAgent[agent.ID]
			if packagePlan == nil {
				continue
			}
			if err := packagePlan.link(agent); err != nil {
				return fmt.Errorf("link agent %q aggregate specs package: %w", agent.ID, err)
			}
		}
	}
	return nil
}

// link builds one aggregate file without choosing or changing any Go names.
func (p *aggregateSpecsPackagePlan) link(agent *AgentData) error {
	imports := make([]*goacodegen.ImportSpec, 0, 2+len(p.toolsetImports))
	imports = append(imports,
		p.pkg.Import(aggregatePolicyImportPath),
		p.pkg.Import(aggregateToolsImportPath),
	)
	for _, importPath := range p.toolsetImports {
		imports = append(imports, p.pkg.Import(importPath))
	}

	seen := make(map[string]struct{})
	toolsets := make([]*aggregateToolsetRenderData, 0, len(agent.AllToolsets))
	labelToolsets := make([]*ToolsetData, 0, len(agent.AllToolsets))
	for _, toolset := range agent.AllToolsets {
		if len(toolset.Tools) == 0 {
			continue
		}
		if _, ok := seen[toolset.SpecsImportPath]; ok {
			continue
		}
		seen[toolset.SpecsImportPath] = struct{}{}
		tools, err := buildToolRenderData(toolset)
		if err != nil {
			return err
		}
		toolsets = append(toolsets, &aggregateToolsetRenderData{
			SpecsPackageName: p.pkg.ImportName(toolset.SpecsImportPath),
			AgentID:          toolset.AgentToolAgentID,
			Tools:            tools,
		})
		labelToolsets = append(labelToolsets, toolset)
	}
	p.linkedRenderData = &aggregateSpecsFileData{
		Path:        filepath.Join(agent.ToolSpecsDir, "specs.go"),
		Description: agent.StructName + " aggregated tool specs",
		PackageName: agent.ToolSpecsPackage,
		Imports:     imports,
		Template: toolSpecsAggregateData{
			SpecsFunc:          p.functions[aggregateSpecsFuncName].Name(),
			NamesFunc:          p.functions[aggregateNamesFuncName].Name(),
			RequiredLabelsFunc: p.functions[aggregateRequiredLabelsFuncName].Name(),
			SpecFunc:           p.functions[aggregateSpecFuncName].Name(),
			MetadataFunc:       p.functions[aggregateMetadataFuncName].Name(),
			MetadataByNameFunc: p.functions[aggregateMetadataByNameFuncName].Name(),
			PolicyPackageName:  p.pkg.ImportName(aggregatePolicyImportPath),
			ToolsPackageName:   p.pkg.ImportName(aggregateToolsImportPath),
			Toolsets:           toolsets,
			RequiredLabels:     unionRequiredLabels(labelToolsets),
		},
	}
	return nil
}

// file returns the completed render data for one agent, or nil when the agent
// does not emit aggregate specifications.
func (p *aggregateSpecsPackagesPlan) file(agentID string) *aggregateSpecsFileData {
	packagePlan := p.byAgent[agentID]
	if packagePlan == nil {
		return nil
	}
	return packagePlan.linkedRenderData
}
