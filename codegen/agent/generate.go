// Package codegen writes agent packages from names and types chosen before file
// generation starts.
package codegen

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// generateAgentFiles appends the agent, tool specification, completion, and
// helper files described by data.
func generateAgentFiles(data *GeneratorData, roots []eval.Root, specsPlan *toolSpecsPlan, helpersPlan *toolsetHelperPackagesPlan, aggregates *aggregateSpecsPackagesPlan, exportsPlan *serviceExportPackagesPlan, registryPlan *registryClientPlan, files []*codegen.File) ([]*codegen.File, error) {
	generated := serviceExportFiles(exportsPlan)
	generated = append(generated, toolsetSpecsFiles(specsPlan)...)
	helperFiles, err := toolsetHelperFiles(helpersPlan)
	if err != nil {
		return nil, err
	}
	generated = append(generated, helperFiles...)
	if registryPlan != nil {
		generated = append(generated, registryClientFiles(registryPlan.clientData)...)
	}
	if len(data.Services) == 0 {
		return append(files, generated...), nil
	}

	completionFiles, err := completionSpecsFiles(data, specsPlan)
	if err != nil {
		return nil, err
	}
	generated = append(generated, completionFiles...)

	for _, svc := range data.Services {
		for _, agent := range svc.Agents {
			afiles, err := agentFiles(agent, aggregates)
			if err != nil {
				return nil, err
			}
			generated = append(generated, afiles...)
		}
	}

	// Write the starter guide unless the design disabled it.
	if !data.DisableAgentDocs {
		if qf := quickstartReadmeFile(data, roots); qf != nil {
			generated = append(generated, qf)
		}
	}

	return append(files, generated...), nil
}

// serviceExportFiles writes the route constants owned by Goa service packages.
func serviceExportFiles(plan *serviceExportPackagesPlan) []*codegen.File {
	if plan == nil {
		return nil
	}
	files := make([]*codegen.File, 0, len(plan.files))
	for _, data := range plan.files {
		files = append(files, &codegen.File{
			Path: filepath.Join(data.Dir, "toolset_exports.go"),
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header("Exported toolset routes", data.PackageName, nil),
				{
					Name:   "service-toolset-exports",
					Source: agentsTemplates.Read(serviceToolsetExportsT),
					Data:   data,
				},
			},
		})
	}
	return files
}

// agentSpecsAggregatorFile writes the fully planned package that lists every
// tool available to one agent.
func agentSpecsAggregatorFile(data *aggregateSpecsFileData) *codegen.File {
	if data == nil {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(data.Description, data.PackageName, data.Imports),
		{
			Name:   "tool-specs-aggregate",
			Source: agentsTemplates.Read(toolSpecsAggregateT),
			Data:   data.Template,
		},
	}
	return &codegen.File{Path: data.Path, SectionTemplates: sections}
}

// unionRequiredLabels returns the sorted, deduplicated union of every
// toolset's RequiredLabels, giving the agent-level aggregate a single
// generated source of truth for run-start label validation.
func unionRequiredLabels(toolsets []*ToolsetData) []string {
	seen := make(map[string]struct{})
	for _, ts := range toolsets {
		for _, l := range ts.RequiredLabels {
			seen[l] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}

// agentImplFile writes the agent identifiers, constructor, and client helpers
// using imports selected by the package plan.
func agentImplFile(agent *AgentData) *codegen.File {
	if agent.packageFiles == nil || agent.packageFiles.implementation == nil {
		panic(fmt.Sprintf("agent codegen: agent %q has no linked implementation file", agent.ID))
	}
	data := agent.packageFiles.implementation
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" implementation", agent.PackageName, data.Imports),
		{
			Name:   "agent-impl",
			Source: agentsTemplates.Read(agentFileT),
			Data:   data,
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "agent.go"), SectionTemplates: sections}
}

// agentConfigFile writes the dependencies supplied when the agent starts.
func agentConfigFile(agent *AgentData) *codegen.File {
	if agent.packageFiles == nil || agent.packageFiles.config == nil {
		panic(fmt.Sprintf("agent codegen: agent %q has no linked config file", agent.ID))
	}
	data := agent.packageFiles.config
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" config", agent.PackageName, data.Imports),
		{
			Name:    "agent-config",
			Source:  agentsTemplates.Read(configFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "config.go"), SectionTemplates: sections}
}

// agentRegistryFile writes the functions that add the agent and its toolsets
// to one running process.
func agentRegistryFile(agent *AgentData) *codegen.File {
	if agent.packageFiles == nil || agent.packageFiles.registry == nil {
		panic(fmt.Sprintf("agent codegen: agent %q has no linked registry file", agent.ID))
	}
	data := newAgentRegistryFileData(agent)
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" registry", agent.PackageName, data.Imports),
		{
			Name:    "agent-registry",
			Source:  agentsTemplates.Read(registryFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "registry.go"),
		SectionTemplates: sections,
	}
}

func agentToolsFiles(agent *AgentData) []*codegen.File {
	if len(agent.ExportedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.ExportedToolsets))
	for _, ts := range agent.ExportedToolsets {
		if ts.AgentToolsDir == "" {
			continue
		}
		data := ts.agentTools
		if data == nil {
			panic(fmt.Sprintf("agent codegen: exported toolset %q has no linked helper package", ts.QualifiedName))
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" agent tools", ts.AgentToolsPackage, data.Imports),
			{
				Name:    "agent-tools",
				Source:  agentsTemplates.Read(agentToolsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.AgentToolsDir, "helpers.go")
		files = append(files, &codegen.File{
			Path:             path,
			SectionTemplates: sections,
		})
	}
	return files
}

// agentToolsConsumerFiles emits thin helpers in the consumer agent package that
// delegate to provider-side agenttools.NewRegistration helpers for toolsets
// exported by other agents. These helpers improve ergonomics for the agent-as-tool
// pattern without hard-coding aggregators or prompts in the generator.
func agentToolsConsumerFiles(agent *AgentData) []*codegen.File {
	if len(agent.UsedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.UsedToolsets))
	for _, ts := range agent.UsedToolsets {
		// Only emit helpers when the toolset is backed by an exported agent and
		// we have a provider agenttools package to call into.
		if ts.AgentToolsImportPath == "" || len(ts.Tools) == 0 {
			continue
		}
		if len(ts.agentToolsConsumerImports) == 0 {
			panic(fmt.Sprintf("agent codegen: consumed agent toolset %q has no linked helper", ts.QualifiedName))
		}
		data := agentToolsetConsumerFileData{
			Toolset:                         ts,
			Imports:                         ts.agentToolsConsumerImports,
			RuntimeAlias:                    ts.agentToolsRuntimeAlias,
			ProviderAlias:                   ts.agentToolsProviderAlias,
			ProviderRegistrationConstructor: ts.AgentToolsProviderRegistrationConstructor,
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(
				ts.Name+" agent toolset client",
				agent.PackageName,
				data.Imports,
			),
			{
				Name:    "agent-tools-consumer",
				Source:  agentsTemplates.Read(agentToolsConsumerT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(agent.Dir, ts.PathName+"_agenttools_client.go")
		files = append(files, &codegen.File{
			Path:             path,
			SectionTemplates: sections,
		})
	}
	return files
}

// toolsetHelperFiles emits each helper package once from the package plan that
// owns its names, imports, and file contents.
func toolsetHelperFiles(plan *toolsetHelperPackagesPlan) ([]*codegen.File, error) {
	if plan == nil {
		return nil, nil
	}
	var files []*codegen.File
	for _, path := range slices.Sorted(maps.Keys(plan.byPath)) {
		planned := plan.byPath[path]
		if planned.usedTools != nil {
			files = append(files, usedToolsFile(planned))
		}
		if planned.serviceExecutor != nil {
			files = append(files, serviceExecutorFile(planned))
		}
		if planned.mcpExecutor != nil {
			file, err := mcpExecutorFile(planned)
			if err != nil {
				return nil, err
			}
			files = append(files, file)
		}
	}
	return files, nil
}

// usedToolsFile writes typed calls for one method-backed helper package.
func usedToolsFile(plan *toolsetHelperPackagePlan) *codegen.File {
	data := plan.usedTools
	return &codegen.File{
		Path: filepath.Join(plan.toolset.Dir, "used_tools.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(plan.toolset.Name+" used tool helpers", plan.toolset.PackageName, data.Imports),
			{
				Name:    "used-tools",
				Source:  agentsTemplates.Read(usedToolsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		},
	}
}

// serviceExecutorFile writes the service caller for one method-backed helper
// package.
func serviceExecutorFile(plan *toolsetHelperPackagePlan) *codegen.File {
	linked := plan.serviceExecutor
	toolset := *plan.toolset
	toolset.SpecsPackageName = linked.SpecsPackageAlias
	data := serviceToolsetFileData{
		PackageName:      toolset.PackageName,
		Toolset:          &toolset,
		Tools:            linked.Tools,
		ServiceClientRef: linked.ServiceClientRef,
		Constructor:      linked.Constructor,
		Names:            linked.Names,
	}
	return &codegen.File{
		Path: filepath.Join(toolset.Dir, "service_executor.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(toolset.Name+" service executor", toolset.PackageName, linked.Imports),
			{
				Name:    "service-executor",
				Source:  agentsTemplates.Read(serviceExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		},
	}
}

// mcpExecutorFile writes the caller for one MCP-backed helper package.
func mcpExecutorFile(plan *toolsetHelperPackagePlan) (*codegen.File, error) {
	toolset := plan.toolset
	tools := make([]mcpExecutorToolData, 0, len(toolset.Tools))
	entries := make(map[string]*toolEntry, len(toolset.specs.tools))
	for _, entry := range toolset.specs.tools {
		entries[entry.Name] = entry
	}
	for _, tool := range toolset.Tools {
		entry := entries[tool.QualifiedName]
		if entry == nil {
			return nil, fmt.Errorf("toolset %q MCP spec for %q is missing", toolset.QualifiedName, tool.QualifiedName)
		}
		tools = append(tools, mcpExecutorToolData{
			LocalName:        tool.Name,
			ConstName:        entry.ConstName,
			SpecVar:          entry.SpecVar,
			HasResult:        entry.HasResult,
			StructuredResult: entry.HasResult && goaexpr.AsObject(tool.Return.Type) != nil,
			TextResult:       entry.HasResult && shared.IsStringType(tool.Return.Type),
		})
	}
	data := mcpExecutorFileData{
		PackageName:     toolset.PackageName,
		Toolset:         toolset,
		mcpExecutorData: plan.mcpExecutor,
		Tools:           tools,
	}
	return &codegen.File{
		Path: filepath.Join(toolset.Dir, "mcp_executor.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(toolset.Name+" MCP executor", toolset.PackageName, plan.mcpExecutor.Imports),
			{
				Name:    "mcp-executor",
				Source:  agentsTemplates.Read(mcpExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		},
	}, nil
}
