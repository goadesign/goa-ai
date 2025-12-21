package codegen

import (
	"os"
	"path/filepath"
	"strings"

	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

// Generate is the code generation entry point for the agents plugin. It is called
// by the Goa code generation framework during the `goa gen` command execution.
//
// The function scans the provided DSL roots for agent declarations, transforms them
// into template-ready data structures, and generates all necessary Go files for each
// agent. Generated artifacts include:
//   - agent.go: agent struct and constructor
//   - config.go: configuration types and validation
//   - workflow.go: workflow definition and handler
//   - activities.go: activity definitions (plan, resume, execute_tool)
//   - registry.go: engine registration helpers
//   - specs/<toolset>/: per-toolset specifications, types, and codecs
//   - specs/specs.go: agent-level aggregator of all toolset specs
//   - agenttools/: helpers for exported toolsets (agent-as-tool pattern)
//   - (optional) service_toolset.go: executor-first registration for method-backed toolsets
//
// Parameters:
//   - genpkg: Go import path to the generated code root (e.g., "myapp/gen")
//   - roots: evaluated DSL roots; must include both goaexpr.RootExpr (for services)
//     and agentsExpr.Root (for agents)
//   - files: existing generated files from other Goa plugins; agent files are appended
//
// Returns the input files slice with agent-generated files appended. If no agents are
// declared in the DSL, returns the input slice unchanged. Returns an error if:
//   - The agents root cannot be located in roots
//   - A service referenced by an agent is not found
//   - Template rendering fails
//   - Tool spec generation fails
//
// Generated files follow the structure:
//
//	gen/<service>/agents/<agent>/*.go
//	gen/<service>/agents/<agent>/specs/<toolset>/*.go
//	gen/<service>/agents/<agent>/specs/specs.go
//	gen/<service>/agents/<agent>/agenttools/<toolset>/helpers.go
//	# Note: service_toolset.go is not emitted in the current generator path; registrations
//	# are built at application boundaries via runtime APIs. The template and builder remain
//	# for potential future use.
//
// The function is safe to call multiple times during generation but expects DSL
// evaluation to be complete before invocation.
func Generate(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	data, err := buildGeneratorData(genpkg, roots)
	if err != nil {
		return nil, err
	}
	if len(data.Services) == 0 {
		return files, nil
	}

	var generated []*codegen.File

	// Emit owner-scoped toolset specs/codecs once per defining toolset.
	generated = append(generated, toolsetSpecsFiles(data)...)

	for _, svc := range data.Services {
		// Emit registry client packages for declared registries.
		if regFiles := registryClientFiles(genpkg, svc); len(regFiles) > 0 {
			generated = append(generated, regFiles...)
		}

		for _, agent := range svc.Agents {
			afiles := agentFiles(agent)
			generated = append(generated, afiles...)
		}
	}

	// Emit contextual quickstart README at module root unless disabled via DSL.
	if !agentsExpr.Root.DisableAgentDocs {
		if qf := quickstartReadmeFile(data); qf != nil {
			generated = append(generated, qf)
		}
	}

	return append(files, generated...), nil
}

// GenerateExample appends a service-local bootstrap helper and planner stub(s)
// so developers can run agents inside the service process with no manual wiring.
//
// Behavior:
//   - For each service that declares at least one agent, emits:
//   - cmd/<service>/agents_bootstrap.go
//   - cmd/<service>/agents_planner_<agent>.go (one per agent)
//   - Patches cmd/<service>/main.go to call bootstrapAgents(ctx) at process start.
//
// The function is idempotent over the in-memory file list provided by Goa’s example
// pipeline. It does not modify gen/ output; it only adds/patches service-side files.
func GenerateExample(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	data, err := buildGeneratorData(genpkg, roots)
	if err != nil {
		return nil, err
	}
	if len(data.Services) == 0 {
		return files, nil
	}

	// Emit application-owned scaffold under internal/agents/; do not patch main.
	moduleBase := moduleBaseImport(genpkg)
	for _, svc := range data.Services {
		if len(svc.Agents) == 0 {
			continue
		}
		if f := emitInternalBootstrap(svc, moduleBase); f != nil {
			files = append(files, f)
		}
		if f := emitCmdMain(svc, moduleBase, files); f != nil {
			files = append(files, f)
		}
		for _, ag := range svc.Agents {
			if f := emitPlannerInternalStub(moduleBase, ag); f != nil {
				files = append(files, f)
			}
			// Internal executor stubs under internal/agents/<agent>/toolsets/<toolset>/
			for _, ts := range ag.AllToolsets {
				var has bool
				for _, t := range ts.Tools {
					if t.IsMethodBacked {
						has = true
						break
					}
				}
				if !has {
					continue
				}
				if f := emitExecutorInternalStub(ag, ts); f != nil {
					files = append(files, f)
				}
			}
		}
	}
	return files, nil
}

// agentSpecsAggregatorFile emits specs/specs.go that aggregates Specs and metadata
// from all specs/<toolset> packages into a single package for convenience.
func agentSpecsAggregatorFile(agent *AgentData) *codegen.File {
	// Build import list: runtime + per-toolset packages
	// Always alias runtime tools to avoid conflicts with toolsets named "tools"
	imports := []*codegen.ImportSpec{
		{Path: "goa.design/goa-ai/runtime/agent/policy"},
		{Path: "goa.design/goa-ai/runtime/agent/tools", Name: "tools"},
	}
	added := make(map[string]struct{})
	toolsets := make([]*ToolsetData, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if len(ts.Tools) == 0 || ts.SpecsImportPath == "" {
			continue
		}
		if _, ok := added[ts.SpecsImportPath]; ok {
			continue
		}
		// Alias toolset package to avoid conflicts with runtime tools package
		alias := ts.SpecsPackageName
		if alias == "tools" {
			alias = ts.SpecsPackageName + "specs"
		}
		imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: alias})
		added[ts.SpecsImportPath] = struct{}{}
		// Update toolset data with the alias for template use
		tsCopy := *ts
		tsCopy.SpecsPackageName = alias
		toolsets = append(toolsets, &tsCopy)
	}
	if len(toolsets) == 0 {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" aggregated tool specs", "specs", imports),
		{Name: "tool-specs-aggregate", Source: agentsTemplates.Read(toolSpecsAggregateT), Data: toolSpecsAggregateData{Toolsets: toolsets}},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "specs", "specs.go"), SectionTemplates: sections}
}

func agentImplFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "errors"},
		{Path: "strings"},
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/engine"},
		{Path: "goa.design/goa-ai/runtime/agent", Name: "agent"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" implementation", agent.PackageName, imports),
		{
			Name:   "agent-impl",
			Source: agentsTemplates.Read(agentFileT),
			Data:   agent,
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "agent.go"), SectionTemplates: sections}
}

func agentConfigFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	// Import model client when a compress-history policy is configured so the
	// generated config can reference model.Client in the HistoryModel field.
	if agent.RunPolicy.History != nil && agent.RunPolicy.History.Mode == "compress" {
		imports = append(imports,
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
		)
	}
	// Determine whether fmt is needed. The config Validate() uses fmt.Errorf for
	// missing method-backed toolset dependencies and for MCP callers.
	needsFmt := false
	if len(agent.MCPToolsets) > 0 {
		needsFmt = true
		imports = append(imports,
			&codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/runtime/mcp"},
		)
	}
	// Scan toolsets to see if any tool is method-backed; if so, fmt is also required.
	if !needsFmt {
		for _, ts := range agent.AllToolsets {
			for _, t := range ts.Tools {
				if t.IsMethodBacked {
					needsFmt = true
					break
				}
			}
			if needsFmt {
				break
			}
		}
	}
	if needsFmt {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
	}
	// Import toolset packages that define method-backed tools so config can reference their Config types.
	for _, ts := range agent.AllToolsets {
		has := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				has = true
				break
			}
		}
		if has && ts.PackageImportPath != "" {
			imports = append(imports, &codegen.ImportSpec{Path: ts.PackageImportPath, Name: ts.PackageName})
		}
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" config", agent.PackageName, imports),
		{
			Name:    "agent-config",
			Source:  agentsTemplates.Read(configFileT),
			Data:    agent,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "config.go"), SectionTemplates: sections}
}

func agentRegistryFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "fmt"},
		{Path: "goa.design/goa-ai/runtime/agent/engine"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "agentsruntime"},
	}
	// fmt needed for error messages in registry (used in both MCP and Used toolsets paths)
	hasExternal := false
	for _, ts := range agent.AllToolsets {
		if ts.Expr != nil && ts.Expr.Provider != nil && ts.Expr.Provider.Kind == agentsExpr.ProviderMCP {
			hasExternal = true
			break
		}
	}
	// Import toolset packages that have method-backed tools so we can call their registration helpers.
	for _, ts := range agent.AllToolsets {
		// Import for method-backed (app-supplied executor) or external MCP (local executor)
		needs := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				needs = true
				break
			}
		}
		if ts.Expr != nil && ts.Expr.Provider != nil && ts.Expr.Provider.Kind == agentsExpr.ProviderMCP {
			needs = true
		}
		if needs && ts.PackageImportPath != "" {
			imports = append(imports, &codegen.ImportSpec{Path: ts.PackageImportPath, Name: ts.PackageName})
		}
	}
	// Import tools when non-MCP Used toolsets are present without agenttools
	// helpers; registry templates use tools.Ident for DSL-provided call/result
	// hint templates on these method-backed toolsets.
	needsTools := false
	for _, ts := range agent.UsedToolsets {
		if ts.Expr != nil && ts.Expr.Provider != nil && ts.Expr.Provider.Kind == agentsExpr.ProviderMCP {
			continue
		}
		if ts.AgentToolsImportPath != "" {
			continue
		}
		needsTools = true
		break
	}
	if needsTools {
		imports = append(imports,
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"},
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/runtime/hints", Name: "hints"},
		)
	}
	if needsTimeImport(agent) {
		imports = append(imports, &codegen.ImportSpec{Path: "time"})
	}
	if len(agent.Tools) > 0 {
		imports = append(imports, &codegen.ImportSpec{Path: agent.ToolSpecsImportPath, Name: agent.ToolSpecsPackage})
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" registry", agent.PackageName, imports),
		{
			Name:   "agent-registry",
			Source: agentsTemplates.Read(registryFileT),
			Data: struct {
				*AgentData
				HasExternalMCP bool
			}{AgentData: agent, HasExternalMCP: hasExternal},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "registry.go"),
		SectionTemplates: sections,
	}
}

func activityNeedsTime(act ActivityArtifact) bool {
	return act.Timeout > 0 || act.RetryPolicy.InitialInterval > 0
}

func agentActivitiesNeedTimeImport(agent *AgentData) bool {
	for _, act := range agent.Runtime.Activities {
		if activityNeedsTime(act) {
			return true
		}
	}
	return false
}

func needsTimeImport(agent *AgentData) bool {
	if agent.RunPolicy.TimeBudget > 0 {
		return true
	}
	return agentActivitiesNeedTimeImport(agent)
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
		// Build tool entries so templates can reuse the same type/codec naming
		// decisions as specs generation.
		svc := ts.SourceService
		if svc == nil {
			svc = agent.Service
		}
		specs, err := buildToolSpecsDataFor(agent.Genpkg, svc, ts.Tools)
		if err != nil {
			continue
		}
		data := agentToolsetFileData{
			PackageName: ts.AgentToolsPackage,
			Toolset:     ts,
			Tools:       specs.tools,
		}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent", Name: "agent"},
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime/hints", Name: "hints"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			// Per-toolset specs package for typed payloads
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName + "specs"},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" agent tools", ts.AgentToolsPackage, imports),
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
		data := agentToolsetConsumerFileData{
			Agent:         agent,
			Toolset:       ts,
			ProviderAlias: ts.AgentToolsPackage,
		}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: ts.AgentToolsImportPath, Name: ts.AgentToolsPackage},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(
				ts.Name+" agent toolset client",
				agent.PackageName,
				imports,
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

// mcpExecutorFiles emits per-MCP-backed-toolset MCP executors that adapt runtime
// ToolCallExecutor to an mcpruntime.Caller using generated codecs.
func mcpExecutorFiles(agent *AgentData) []*codegen.File {
	out := make([]*codegen.File, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if ts.Expr == nil || ts.Expr.Provider == nil || ts.Expr.Provider.Kind != agentsExpr.ProviderMCP {
			continue
		}
		data := serviceToolsetFileData{PackageName: ts.PackageName, Agent: agent, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: "strings"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent/telemetry"},
			{Path: "goa.design/goa-ai/runtime/mcp", Name: "mcpruntime"},
			// Per-toolset specs package (codecs + schemas)
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" MCP executor", ts.PackageName, imports),
			{
				Name:    "mcp-executor",
				Source:  agentsTemplates.Read(mcpExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "mcp_executor.go")
		out = append(out, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return out
}

// usedToolsFiles emits typed call builders and type aliases for method-backed Used toolsets
// to align UX with agent-as-tool helpers.
func usedToolsFiles(agent *AgentData) []*codegen.File {
	if len(agent.MethodBackedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.MethodBackedToolsets))
	for _, ts := range agent.MethodBackedToolsets {
		// Only emit when specs are present
		if ts.SpecsImportPath == "" || len(ts.Tools) == 0 {
			continue
		}
		svc := ts.SourceService
		if svc == nil {
			svc = agent.Service
		}
		specs, err := buildToolSpecsDataFor(agent.Genpkg, svc, ts.Tools)
		if err != nil {
			continue
		}
		data := agentToolsetFileData{PackageName: ts.PackageName, Toolset: ts, Tools: specs.tools}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			// Per-toolset specs package for typed payloads
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName + "specs"},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" used tool helpers", ts.PackageName, imports),
			{
				Name:    "used-tools",
				Source:  agentsTemplates.Read(usedToolsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "used_tools.go")
		files = append(files, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return files
}

// serviceExecutorFiles emits per-toolset service executors that adapt runtime
// ToolCallExecutor to user-provided callers using generated codecs and optional mappers.
func serviceExecutorFiles(agent *AgentData) []*codegen.File {
	if len(agent.MethodBackedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.MethodBackedToolsets))
	for _, ts := range agent.MethodBackedToolsets {
		if ts.Expr == nil || len(ts.Tools) == 0 {
			continue
		}
		svc := ts.SourceService
		if svc == nil {
			svc = agent.Service
		}
		// Use a NameScope to guarantee unique import aliases for the service client
		// and specs packages within this file (for example, service "todos" with
		// toolset "todos"). Keep the service alias derived from the original
		// package name so precomputed method type references remain valid and
		// assign a distinct alias to the specs package when needed.
		aliasScope := codegen.NewNameScope()
		svcAlias := ""
		if svc != nil {
			svcAlias = aliasScope.Unique(servicePkgAlias(svc))
		}
		specsAlias := aliasScope.Unique(ts.SpecsPackageName, "specs")
		// Gather additional imports required by method payload/result types so
		// that type assertions in the executor (for example, args.(*types.Foo))
		// compile even when the payload/result types live in external packages
		// such as the shared gen/types module.
		extraImports := make(map[string]*codegen.ImportSpec)
		for _, t := range ts.Tools {
			if !t.IsMethodBacked {
				continue
			}
			for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodPayloadAttr) {
				if im != nil && im.Path != "" {
					extraImports[im.Path] = im
				}
			}
			for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodResultAttr) {
				if im != nil && im.Path != "" {
					extraImports[im.Path] = im
				}
			}
			for _, im := range gatherAttributeImports(agent.Genpkg, t.Artifact) {
				if im != nil && im.Path != "" {
					extraImports[im.Path] = im
				}
			}
		}

		// Use a local copy of the toolset so we can override the SpecsPackageName
		// alias for this file without affecting other generated artifacts.
		tsCopy := *ts
		tsCopy.SpecsPackageName = specsAlias

		data := serviceToolsetFileData{
			PackageName:     ts.PackageName,
			Agent:           agent,
			Toolset:         &tsCopy,
			ServicePkgAlias: svcAlias,
		}
		needsSharedTypes := false
		for _, t := range ts.Tools {
			if t == nil || !t.IsMethodBacked {
				continue
			}
			if strings.Contains(t.MethodPayloadTypeRef, "types.") || strings.Contains(t.MethodResultTypeRef, "types.") {
				needsSharedTypes = true
				break
			}
		}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: "errors"},
			{Path: "fmt"},
			{Path: "strings"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: ts.SpecsImportPath, Name: specsAlias},
		}
		if needsSharedTypes {
			typesPath := filepath.ToSlash(filepath.Join(agent.Genpkg, "types"))
			imports = append(imports, &codegen.ImportSpec{Path: typesPath})
			delete(extraImports, typesPath)
		}
		if svc != nil {
			// Import the service client package (e.g. gen/atlas_data)
			clientPath := filepath.Join(agent.Genpkg, svc.PathName)
			// Check for slash/backslash issues if Genpkg has slashes
			clientPath = strings.ReplaceAll(clientPath, "\\", "/")
			imports = append(imports, &codegen.ImportSpec{Path: clientPath, Name: svcAlias})
			// Avoid duplicating the client import when also discovered via
			// gatherAttributeImports on method payload/result types.
			delete(extraImports, clientPath)
		}
		// Append any remaining external imports needed for payload/result
		// types (for example, the shared gen/types package).
		for _, im := range extraImports {
			if im == nil || im.Path == "" {
				continue
			}
			// Specs and service client imports are already in the list.
			if im.Path == ts.SpecsImportPath {
				continue
			}
			imports = append(imports, im)
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" service executor", ts.PackageName, imports),
			{
				Name:    "service-executor",
				Source:  agentsTemplates.Read(serviceExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "service_executor.go")
		files = append(files, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return files
}

// Note: we intentionally avoid parsing type references to infer imports. All
// needed user type imports come from Goa's UserTypeLocation captured in ToolData.

// moduleBaseImport returns the module base import path by stripping trailing /gen from genpkg.
func moduleBaseImport(genpkg string) string {
	base := strings.TrimSuffix(genpkg, "/")
	for strings.HasSuffix(base, "/gen") {
		base = strings.TrimSuffix(base, "/gen")
	}
	return base
}

// emitInternalBootstrap emits internal/agents/bootstrap/bootstrap.go with a simple New(ctx) bootstrap.
func emitInternalBootstrap(svc *ServiceAgentsData, moduleBase string) *codegen.File {
	if svc == nil || len(svc.Agents) == 0 {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "agentsruntime"},
	}
	needsMCP := svc.HasMCP
	if needsMCP {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
		imports = append(imports, &codegen.ImportSpec{Path: "flag"})
		imports = append(imports, &codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/runtime/mcp"})
	}
	// Import generated agent registration packages and per-agent planner packages.
	type toolsetImport struct{ Alias, Path string }
	type agentImport struct {
		Alias, Path, PlannerAlias, PlannerPath string
		Toolsets                               []toolsetImport
		Agent                                  *AgentData
	}
	agents := make([]agentImport, 0, len(svc.Agents))
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
		palias := "planner" + ag.PathName
		ppath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "planner"))
		imports = append(imports, &codegen.ImportSpec{Path: ppath, Name: palias})
		// Import internal toolset executor packages for method-backed toolsets.
		var tsImports []toolsetImport
		for _, ts := range ag.MethodBackedToolsets {
			tpath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "toolsets", ts.PathName))
			talias := "toolset" + ag.PathName + ts.PathName
			imports = append(imports, &codegen.ImportSpec{Path: tpath, Name: talias})
			tsImports = append(tsImports, toolsetImport{Alias: talias, Path: tpath})
		}
		agents = append(agents, agentImport{Alias: ag.PackageName, Path: ag.ImportPath, PlannerAlias: palias, PlannerPath: ppath, Toolsets: tsImports, Agent: ag})
	}
	path := filepath.Join("internal", "agents", "bootstrap", "bootstrap.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("Agents bootstrap (internal)", "bootstrap", imports),
		{
			Name:   "bootstrap-internal",
			Source: agentsTemplates.Read(bootstrapInternalT),
			Data: struct {
				Service *ServiceAgentsData
				Agents  []agentImport
			}{svc, agents},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// emitPlannerInternalStub emits internal/agents/<agent>/planner/planner.go with a tiny planner.
func emitPlannerInternalStub(_ string, ag *AgentData) *codegen.File {
	if ag == nil {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header("Planner stub for "+ag.StructName, "planner", imports),
		{Name: "planner-internal-stub", Source: agentsTemplates.Read(plannerInternalStubT), Data: ag},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "planner", "planner.go")
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// emitExecutorInternalStub emits internal/agents/<agent>/toolsets/<toolset>/execute.go stub for method-backed tools.
func emitExecutorInternalStub(ag *AgentData, ts *ToolsetData) *codegen.File {
	src := ts.SourceService
	if src == nil {
		return nil
	}
	// Build imports: agent package for registration.
	agentImport := &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName}
	imports := []*codegen.ImportSpec{
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		agentImport,
		{Path: "goa.design/goa-ai/runtime/agent/runtime"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	// Import specs package for typed payloads and transforms.
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	// Build tool switch metadata
	type execTool struct {
		ID               string
		GoName           string
		PayloadUnmarshal string
		PayloadType      string
	}
	// Pre-allocate for method-backed tools
	count := 0
	for _, t := range ts.Tools {
		if t.IsMethodBacked {
			count++
		}
	}
	tools := make([]execTool, 0, count)
	for _, t := range ts.Tools {
		if !t.IsMethodBacked {
			continue
		}
		g := codegen.Goify(t.Name, true)
		tools = append(tools, execTool{
			ID:               t.Name,
			GoName:           g,
			PayloadUnmarshal: "Unmarshal" + g + "Payload",
			PayloadType:      g + "Payload",
		})
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" executor stub for "+ag.StructName, ts.PathName, imports),
		{
			Name:   "example-executor-stub",
			Source: agentsTemplates.Read(exampleExecutorStubT),
			Data: struct {
				Agent       *AgentData
				Toolset     *ToolsetData
				AgentImport *codegen.ImportSpec
				SpecsAlias  string
				Tools       []execTool
			}{ag, ts, agentImport, specsAlias, tools},
			FuncMap: templateFuncMap(),
		},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "toolsets", ts.PathName, "execute.go")
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// quickstartReadmeFile builds the contextual quickstart README at the module root.
// The file is named AGENTS_QUICKSTART.md and is generated unless disabled via DSL.
func quickstartReadmeFile(data *GeneratorData) *codegen.File {
	if data == nil || len(data.Services) == 0 {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		{
			Name:    "agents-quickstart",
			Source:  agentsTemplates.Read(quickstartReadmeT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: "AGENTS_QUICKSTART.md", SectionTemplates: sections}
}

// emitCmdMain patches cmd/<service>/main.go for agent-only designs.
// If goa core generated a main.go file (found in files), it replaces the sections
// with agent-specific content that uses the generated bootstrap. If no main.go
// exists in files, it creates a new one.
func emitCmdMain(svc *ServiceAgentsData, moduleBase string, files []*codegen.File) *codegen.File {
	if svc == nil || len(svc.Agents) == 0 {
		return nil
	}
	mainPath := filepath.Join("cmd", svc.Service.PathName, "main.go")

	// Find existing main.go from files (goa core may have generated it)
	var file *codegen.File
	for _, f := range files {
		if f.Path == mainPath {
			file = f
			break
		}
	}

	// Build imports for agent main
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "fmt"},
		{Path: "log"},
		{Path: filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", "bootstrap"))},
		{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
	}
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
	}

	agentSection := &codegen.SectionTemplate{
		Name:    "cmd-main",
		Source:  agentsTemplates.Read(cmdMainT),
		Data:    struct{ Agents []*AgentData }{Agents: svc.Agents},
		FuncMap: templateFuncMap(),
	}

	if file != nil {
		// Replace the existing file's sections with agent-specific content
		file.SectionTemplates = []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		}
		return nil // Already in files, no need to return a new file
	}

	// No existing file - check filesystem and create new if needed
	if _, err := os.Stat(mainPath); !os.IsNotExist(err) {
		return nil // file already exists on disk, skip it
	}

	return &codegen.File{
		Path: mainPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		},
	}
}
