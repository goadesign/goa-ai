// Package codegen plans the names used by generated example bootstrap files.
//
// The goa example command writes one bootstrap package for each service that
// owns agents. This file records every package name and import before Goa
// freezes the generation, then gives the renderer only the final spellings.
package codegen

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// exampleBootstrapPackagesPlan stores one planned bootstrap package per
	// service path.
	exampleBootstrapPackagesPlan struct {
		byService map[string]*exampleBootstrapPackagePlan
	}

	// exampleBootstrapPackagePlan stores the declarations and imports written
	// into one service bootstrap package.
	exampleBootstrapPackagePlan struct {
		pkg    *goacodegen.GeneratedPackage
		agents map[string]*exampleBootstrapAgentPlan
	}

	// exampleBootstrapAgentPlan stores the imports and endpoint variables used
	// to start one generated agent.
	exampleBootstrapAgentPlan struct {
		agentImportPath   string
		plannerImportPath string
		executorPaths     map[string]string
		mcp               map[string]*exampleBootstrapMCPPlan
	}

	// exampleBootstrapMCPPlan stores the command-line flag and Go variable used
	// to configure one MCP caller.
	exampleBootstrapMCPPlan struct {
		endpoint *goacodegen.NameDeclaration
		flagName string
	}

	// exampleBootstrapNameOrder keeps endpoint variable names stable when the
	// design declaration order changes.
	exampleBootstrapNameOrder struct {
		packagePath string
		key         string
	}
)

const (
	bootstrapAgentRuntimeImportPath = "goa.design/goa-ai/runtime/agent/runtime"
	bootstrapMCPRuntimeImportPath   = "goa.design/goa-ai/runtime/mcp"
)

// planExampleBootstrapPackages claims every application bootstrap package and
// records the imports and endpoint names that its source will use.
func planExampleBootstrapPackages(generation *goacodegen.Generation, design *agentir.Design) (*exampleBootstrapPackagesPlan, error) {
	planned := &exampleBootstrapPackagesPlan{byService: make(map[string]*exampleBootstrapPackagePlan)}
	moduleBase := moduleBaseImport(design.Genpkg)
	for _, service := range design.Services {
		if len(service.Agents) == 0 {
			continue
		}
		outputDir := filepath.ToSlash(filepath.Join("internal", "agents", service.PathName, "bootstrap"))
		importPath := filepath.ToSlash(filepath.Join(moduleBase, outputDir))
		pkg, err := generation.ClaimOutputPackage(importPath, outputDir)
		if err != nil {
			return nil, fmt.Errorf("plan service %q example bootstrap package: %w", service.Name, err)
		}
		packagePlan := &exampleBootstrapPackagePlan{
			pkg:    pkg,
			agents: make(map[string]*exampleBootstrapAgentPlan),
		}
		if err := packagePlan.declare(service, moduleBase); err != nil {
			return nil, fmt.Errorf("plan service %q example bootstrap names: %w", service.Name, err)
		}
		planned.byService[service.PathName] = packagePlan
	}
	return planned, nil
}

// declare records the New function, imports, and MCP endpoint variables used
// by one bootstrap package.
func (p *exampleBootstrapPackagePlan) declare(service *agentir.Service, moduleBase string) error {
	if err := p.pkg.DeclareName(goacodegen.NewExactName(goacodegen.NameFunction, "New")); err != nil {
		return err
	}
	for _, spec := range []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("context"),
		goacodegen.NewImport("agentsruntime", bootstrapAgentRuntimeImportPath),
	} {
		if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
			return err
		}
	}

	needsMCP := false
	for _, agent := range service.Agents {
		agentPlan := &exampleBootstrapAgentPlan{
			agentImportPath:   agent.ImportPath,
			plannerImportPath: filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", agent.PathName, "planner")),
			executorPaths:     make(map[string]string),
			mcp:               make(map[string]*exampleBootstrapMCPPlan),
		}
		for _, spec := range []*goacodegen.ImportSpec{
			goacodegen.NewImport(agent.PackageName, agentPlan.agentImportPath),
			goacodegen.NewImport("planner"+agent.PathName, agentPlan.plannerImportPath),
		} {
			if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
				return err
			}
		}
		for _, reference := range agent.UsedToolsets {
			if !starterExecutorReference(reference) {
				continue
			}
			executorPath := filepath.ToSlash(filepath.Join(
				moduleBase,
				"internal",
				"agents",
				agent.PathName,
				"toolsets",
				reference.Slug,
			))
			if err := p.pkg.ReserveGeneratedImport(goacodegen.NewImport(
				"toolset"+agent.PathName+reference.Slug,
				executorPath,
			)); err != nil {
				return err
			}
			agentPlan.executorPaths[reference.QualifiedName] = executorPath
		}
		for _, meta := range exampleBootstrapMCPReferences(agent) {
			preferred := "mcp" + goacodegen.Goify(meta.ServiceName, true) +
				goacodegen.Goify(meta.SuiteName, true) + "Endpoint"
			declaration := goacodegen.NewPreferredName(
				goacodegen.NameVariable,
				preferred,
				goacodegen.UnexportedName,
				exampleBootstrapNameOrder{
					packagePath: p.pkg.ImportPath(),
					key:         agent.ID + ":mcp:" + meta.QualifiedName,
				},
			)
			if err := p.pkg.DeclareName(declaration); err != nil {
				return err
			}
			agentPlan.mcp[meta.QualifiedName] = &exampleBootstrapMCPPlan{
				endpoint: declaration,
				flagName: "mcp-" + strings.ToLower(meta.ServiceName) + "-" +
					strings.ToLower(meta.SuiteName) + "-endpoint",
			}
			needsMCP = true
		}
		p.agents[agent.ID] = agentPlan
	}
	if !needsMCP {
		return nil
	}
	for _, spec := range []*goacodegen.ImportSpec{
		goacodegen.SimpleImport("flag"),
		goacodegen.SimpleImport("fmt"),
		goacodegen.NewImport("mcpruntime", bootstrapMCPRuntimeImportPath),
	} {
		if err := p.pkg.ReserveGeneratedImport(spec); err != nil {
			return err
		}
	}
	return nil
}

// starterExecutorReference reports whether goa example writes an
// application-owned executor for a used toolset reference.
func starterExecutorReference(reference *agentir.ToolsetRef) bool {
	if reference.AgentToolsImportPath != "" || reference.SpecsImportPath == "" {
		return false
	}
	if reference.Provider != nil && (reference.Provider.Kind == agentexpr.ProviderMCP ||
		reference.Provider.Kind == agentexpr.ProviderRegistry) {
		return false
	}
	return len(toolsetContract(reference).Tools) > 0
}

// exampleBootstrapMCPReferences returns each distinct MCP route configured by
// one agent in stable route-name order.
func exampleBootstrapMCPReferences(agent *agentir.Agent) []*agentir.MCPToolsetMeta {
	byName := make(map[string]*agentir.MCPToolsetMeta)
	for _, reference := range append(append([]*agentir.ToolsetRef{}, agent.UsedToolsets...), agent.ExportedToolsets...) {
		if reference.Provider == nil || reference.Provider.Kind != agentexpr.ProviderMCP {
			continue
		}
		byName[reference.Provider.MCP.QualifiedName] = reference.Provider.MCP
	}
	metas := make([]*agentir.MCPToolsetMeta, 0, len(byName))
	for _, meta := range byName {
		metas = append(metas, meta)
	}
	slices.SortFunc(metas, func(left, right *agentir.MCPToolsetMeta) int {
		return strings.Compare(left.QualifiedName, right.QualifiedName)
	})
	return metas
}

// ComparePackageName orders endpoint variables by package and MCP route.
func (o exampleBootstrapNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(exampleBootstrapNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}
