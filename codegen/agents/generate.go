package codegen

import (
	"path/filepath"
	"sort"
	"strings"

	agentsExpr "goa.design/goa-ai/expr/agents"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	toolSpecFileData struct {
		PackageName string
		Tools       []*toolEntry
		Types       []*typeData
	}

	toolTypesFileData struct {
		Types []*typeData
	}

	toolCodecsFileData struct {
		Types []*typeData
		Tools []*toolEntry
	}

	toolSpecsAggregateData struct {
		Toolsets []*ToolsetData
	}

	agentToolsetFileData struct {
		PackageName string
		Toolset     *ToolsetData
	}

	serviceToolsetFileData struct {
		PackageName string
		Agent       *AgentData
		Toolset     *ToolsetData
	}

	// transforms
	transformFuncData struct {
		Name          string
		ParamTypeRef  string
		ResultTypeRef string
		Body          string
		Helpers       []*codegen.TransformFunctionData
	}

	transformsFileData struct {
		HeaderComment string
		PackageName   string
		Imports       []*codegen.ImportSpec
		Functions     []transformFuncData
	}
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
//   - service_toolset.go: method-backed tool adapters (when tools bind to service methods)
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
//	gen/<service>/agents/<agent>/<toolset>/service_toolset.go
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
	for _, svc := range data.Services {
		for _, agent := range svc.Agents {
			afiles := agentFiles(agent)
			generated = append(generated, afiles...)
			// Adapter stubs are only generated during the `goa example` phase.
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

func agentFiles(agent *AgentData) []*codegen.File {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentWorkflowFile(agent),
		agentActivitiesFile(agent),
		agentRegistryFile(agent),
	}
	// Emit per-toolset specs packages + aggregator.
	if tsFiles := agentPerToolsetSpecsFiles(agent); len(tsFiles) > 0 {
		files = append(files, tsFiles...)
		if agg := agentSpecsAggregatorFile(agent); agg != nil {
			files = append(files, agg)
		}
	}
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, serviceToolsetFiles(agent)...)
	files = append(files, mcpExecutorFiles(agent)...)

	var filtered []*codegen.File
	for _, f := range files {
		if f != nil {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// agentPerToolsetSpecsFiles emits types/codecs/specs under specs/<toolset>/ using
// short, tool-local type names to avoid collisions between toolsets.
func agentPerToolsetSpecsFiles(agent *AgentData) []*codegen.File {
	var out []*codegen.File
	emitted := make(map[string]struct{})
	for _, ts := range agent.AllToolsets {
		if len(ts.Tools) == 0 {
			continue
		}
		// Deduplicate per-toolset emission by SpecsDir to avoid generating the
		// same codecs/specs multiple times when the toolset appears in multiple
		// groups (e.g., Uses and an external binding) or through data merges.
		if ts.SpecsDir != "" {
			if _, dup := emitted[ts.SpecsDir]; dup {
				continue
			}
			emitted[ts.SpecsDir] = struct{}{}
		}
		data, err := buildToolSpecsDataFor(agent, ts.Tools)
		if err != nil || data == nil {
			continue
		}
		// types.go
		if pure := data.pureTypes(); len(pure) > 0 {
			sections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool types", ts.SpecsPackageName, data.typeImports()),
				{Name: "tool-spec-types", Source: agentsTemplates.Read(toolTypesFileT), Data: toolTypesFileData{Types: pure}},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "types.go"), SectionTemplates: sections})
		}
		if len(data.tools) > 0 {
			// codecs.go
			codecsSections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool codecs", ts.SpecsPackageName, data.codecsImports()),
				{Name: "tool-spec-codecs", Source: agentsTemplates.Read(toolCodecsFileT), Data: toolCodecsFileData{Types: data.typesList(), Tools: data.tools}},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "codecs.go"), SectionTemplates: codecsSections})
			// specs.go
			specImports := []*codegen.ImportSpec{{Path: "sort"}, {Path: "goa.design/goa-ai/runtime/agents/policy"}, {Path: "goa.design/goa-ai/runtime/agents/tools"}}
			specSections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool specs", ts.SpecsPackageName, specImports),
				{Name: "tool-specs", Source: agentsTemplates.Read(toolSpecFileT), Data: toolSpecFileData{PackageName: ts.SpecsPackageName, Tools: data.tools, Types: data.typesList()}},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "specs.go"), SectionTemplates: specSections})

			// transforms.go (only if we can generate conversions)
			if tf := emitTransformsFile(agent, ts, data); tf != nil {
				out = append(out, tf)
			}
		}
	}
	return out
}

// emitTransformsFile builds transforms using Goa GoTransform between tool specs
// and bound method payload/result types when shapes are compatible.
func emitTransformsFile(agent *AgentData, ts *ToolsetData, data *toolSpecsData) *codegen.File {
	// Only meaningful for method-backed toolsets.
	hasMethodBacked := false
	for _, t := range ts.Tools {
		if t.IsMethodBacked {
			hasMethodBacked = true
			break
		}
	}
	if !hasMethodBacked {
		return nil
	}

	// Prepare contexts: source specs (same package conversion), target service (external).
	// Use empty default package for the source so same-package references are not qualified.
	srcCtx := codegen.NewAttributeContextForConversion(false, false, true, "", codegen.NewNameScope())
	tgtCtx := codegen.NewAttributeContext(false, false, true, ts.SourceService.PkgName, codegen.NewNameScope())
	// And reverse for result -> spec
	revSrcCtx := codegen.NewAttributeContext(false, false, true, ts.SourceService.PkgName, codegen.NewNameScope())
	// Use empty default package to avoid qualifying same-package references
	// (helpers should not prefix with the local specs package name).
	revTgtCtx := codegen.NewAttributeContextForConversion(false, false, true, "", codegen.NewNameScope())

	// Import set
	importSet := map[string]*codegen.ImportSpec{}
	// Service import
	if ts.SourceService != nil && ts.SourceService.PkgName != "" {
		svcPath := joinImportPath(agent.Genpkg, ts.SourceService.PathName)
		if svcPath != "" {
			importSet[svcPath] = &codegen.ImportSpec{Name: ts.SourceService.PkgName, Path: svcPath}
		}
	}

	// Collect helper functions
	var fns []transformFuncData
	// Map tool name to its specs typeData for quick lookup
	typeByTool := make(map[string]*typeData)
	resByTool := make(map[string]*typeData)
	for _, td := range data.tools {
		typeByTool[td.Name] = td.Payload
		resByTool[td.Name] = td.Result
	}

	for _, tool := range ts.Tools {
		// Skip tools without payload/result information
		payloadTD := typeByTool[tool.QualifiedName]
		resultTD := resByTool[tool.QualifiedName]
		// ToMethodPayload_<Tool>
		if tool.IsMethodBacked && tool.MethodPayloadAttr != nil && payloadTD != nil {
			if err := codegen.IsCompatible(tool.Args.Type, tool.MethodPayloadAttr.Type, "tool payload", "method payload"); err == nil {
				prefix := "toMethodPayload_" + strings.ToLower(codegen.Goify(tool.Name, false))
				// Ensure source payload is referenced as its local specs user type to avoid inline object inits.
				body, helpers, err := codegen.GoTransform(tool.Args, tool.MethodPayloadAttr, "in", "out", srcCtx, tgtCtx, prefix, false)
				if err == nil && strings.TrimSpace(body) != "" {
					// Gather meta imports and user type locations for both sides
					for _, im := range codegen.GetMetaTypeImports(tool.Args) {
						importSet[im.Path] = im
					}
					for _, im := range codegen.GetMetaTypeImports(tool.MethodPayloadAttr) {
						importSet[im.Path] = im
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, tool.Args) {
						importSet[im.Path] = im
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, tool.MethodPayloadAttr) {
						importSet[im.Path] = im
					}
					// Also include any service user type imports referenced by the generated body/helpers.
					for alias, im := range ts.SourceServiceImports {
						if im == nil || im.Path == "" {
							continue
						}
						if strings.Contains(body, alias+".") {
							importSet[im.Path] = im
						}
						for _, h := range helpers {
							if strings.Contains(h.Code, alias+".") {
								importSet[im.Path] = im
								break
							}
						}
					}
					// Use local spec type name for the parameter (avoid qualifying same-package types).
					paramRef := payloadTD.TypeName
					if payloadTD.Pointer {
						paramRef = "*" + paramRef
					}
					fns = append(fns, transformFuncData{
						Name:          "ToMethodPayload_" + codegen.Goify(tool.Name, true),
						ParamTypeRef:  paramRef,
						ResultTypeRef: tool.MethodPayloadTypeRef,
						Body:          body,
						Helpers:       helpers,
					})
				}
			}
		}
		// ToToolReturn_<Tool>
		if tool.IsMethodBacked && tool.HasResult && tool.MethodResultAttr != nil && resultTD != nil {
			if err := codegen.IsCompatible(tool.MethodResultAttr.Type, tool.Return.Type, "method result", "tool result"); err == nil {
				prefix := "toToolReturn_" + strings.ToLower(codegen.Goify(tool.Name, false))
				// Synthesize a local user type for the target using the
				// emitted specs type name to avoid inline struct literals
				// in the generated code and to keep return type stable.
				base := tool.Return
				if ut, ok := tool.Return.Type.(goaexpr.UserType); ok && ut != nil {
					base = ut.Attribute()
				}
				tgtAttr := &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{AttributeExpr: base, TypeName: resultTD.TypeName}}
				body, helpers, err := codegen.GoTransform(tool.MethodResultAttr, tgtAttr, "in", "out", revSrcCtx, revTgtCtx, prefix, false)
				if err == nil && strings.TrimSpace(body) != "" {
					for _, im := range codegen.GetMetaTypeImports(tool.MethodResultAttr) {
						importSet[im.Path] = im
					}
					for _, im := range codegen.GetMetaTypeImports(tool.Return) {
						importSet[im.Path] = im
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, tool.MethodResultAttr) {
						importSet[im.Path] = im
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, tool.Return) {
						importSet[im.Path] = im
					}
					for alias, im := range ts.SourceServiceImports {
						if im == nil || im.Path == "" {
							continue
						}
						if strings.Contains(body, alias+".") {
							importSet[im.Path] = im
						}
						for _, h := range helpers {
							if strings.Contains(h.Code, alias+".") {
								importSet[im.Path] = im
								break
							}
						}
					}
					// Use local spec type name for the result reference.
					resRef := resultTD.TypeName
					if resultTD.Pointer {
						resRef = "*" + resRef
					}
					fns = append(fns, transformFuncData{
						Name:          "ToToolReturn_" + codegen.Goify(tool.Name, true),
						ParamTypeRef:  tool.MethodResultTypeRef,
						ResultTypeRef: resRef,
						Body:          body,
						Helpers:       helpers,
					})
				}
			}
		}
	}

	if len(fns) == 0 {
		return nil
	}

	// Build imports slice in stable order
	var imports []*codegen.ImportSpec
	if len(importSet) > 0 {
		paths := make([]string, 0, len(importSet))
		for p := range importSet {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			imports = append(imports, importSet[p])
		}
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" tool transforms", ts.SpecsPackageName, imports),
		{Name: "tool-transforms", Source: agentsTemplates.Read(toolTransformsFileT), Data: transformsFileData{
			HeaderComment: agent.StructName + " tool transforms",
			PackageName:   ts.SpecsPackageName,
			Imports:       imports,
			Functions:     fns,
		}},
	}
	return &codegen.File{Path: filepath.Join(ts.SpecsDir, "transforms.go"), SectionTemplates: sections}
}

// agentSpecsAggregatorFile emits specs/specs.go that aggregates Specs and metadata
// from all specs/<toolset> packages into a single package for convenience.
func agentSpecsAggregatorFile(agent *AgentData) *codegen.File {
	// Build import list: runtime + per-toolset packages
	imports := []*codegen.ImportSpec{{Path: "sort"}, {Path: "goa.design/goa-ai/runtime/agents/policy"}, {Path: "goa.design/goa-ai/runtime/agents/tools"}}
	added := make(map[string]struct{})
	toolsets := make([]*ToolsetData, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if len(ts.Tools) == 0 || ts.SpecsImportPath == "" {
			continue
		}
		if _, ok := added[ts.SpecsImportPath]; ok {
			continue
		}
		imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName})
		added[ts.SpecsImportPath] = struct{}{}
		toolsets = append(toolsets, ts)
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
		{Path: "goa.design/goa-ai/runtime/agents/engine"},
		{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "runtime"},
		{Path: "goa.design/goa-ai/runtime/agents/planner"},
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
		{Path: "goa.design/goa-ai/runtime/agents/planner"},
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

func agentWorkflowFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agents/engine"},
		{Path: "goa.design/goa-ai/runtime/agents/runtime"},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" workflow", agent.PackageName, imports),
		{
			Name:   "agent-workflow",
			Source: agentsTemplates.Read(workflowFileT),
			Data:   agent,
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "workflow.go"), SectionTemplates: sections}
}

func agentActivitiesFile(agent *AgentData) *codegen.File {
	if len(agent.Runtime.Activities) == 0 {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agents/engine"},
		{Name: "agentsruntime", Path: "goa.design/goa-ai/runtime/agents/runtime"},
	}
	if agentActivitiesNeedTimeImport(agent) {
		imports = append(imports, &codegen.ImportSpec{Path: "time"})
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" activities", agent.PackageName, imports),
		{
			Name:   "agent-activities",
			Source: agentsTemplates.Read(activitiesFileT),
			Data:   agent,
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "activities.go"), SectionTemplates: sections}
}

func agentRegistryFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agents/engine"},
		{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "agentsruntime"},
	}
	// fmt needed for error messages in external MCP registration path
	hasExternal := false
	for _, ts := range agent.AllToolsets {
		if ts.Expr != nil && ts.Expr.External {
			hasExternal = true
			break
		}
	}
	if hasExternal {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
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
		if ts.Expr != nil && ts.Expr.External {
			needs = true
		}
		if needs && ts.PackageImportPath != "" {
			imports = append(imports, &codegen.ImportSpec{Path: ts.PackageImportPath, Name: ts.PackageName})
		}
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
		data := agentToolsetFileData{PackageName: ts.AgentToolsPackage, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agents/tools"},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" agent tools", ts.AgentToolsPackage, imports),
			{
				Name:   "agent-tools",
				Source: agentsTemplates.Read(agentToolsFileT),
				Data:   data,
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

// mcpExecutorFiles emits per-external-toolset MCP executors that adapt runtime
// ToolCallExecutor to an mcpruntime.Caller using generated codecs.
func mcpExecutorFiles(agent *AgentData) []*codegen.File {
	out := make([]*codegen.File, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if ts.Expr == nil || !ts.Expr.External {
			continue
		}
		data := serviceToolsetFileData{PackageName: ts.PackageName, Agent: agent, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: "strings"},
			{Path: "goa.design/goa-ai/runtime/agents/planner"},
			{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agents/telemetry"},
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

// serviceToolsetFiles generates a per-agent toolset registration for tools that are
// explicitly bound to service methods via Method(). It creates a thin registration
// file that will later be expanded to include per-tool adapters. For now, we emit
// the registration skeleton so Method() influences codegen structure.
func serviceToolsetFiles(agent *AgentData) []*codegen.File {
	// Collect toolsets with at least one method-backed tool
	out := []*codegen.File{}
	for _, ts := range agent.AllToolsets {
		// Emit for method-backed toolsets, external MCP toolsets, and custom global toolsets
		need := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				need = true
				break
			}
		}
		if ts.Expr != nil && ts.Expr.External {
			need = true
		}
		if !need {
			// Non-method, non-external toolsets still require an app-supplied executor when used
			// by the agent; generate the generic registration for Used toolsets.
			if ts.Kind == ToolsetKindUsed {
				need = true
			}
		}
		if !need {
			continue
		}
		// Build imports including the source service package and any user type packages
		// referenced by method payload/result types (e.g., gen/types).
		src := ts.SourceService
		if src == nil {
			// Skip generation if we cannot resolve the source service (e.g., malformed test fixture)
			continue
		}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "goa.design/goa-ai/runtime/agents/planner"},
			{Path: "goa.design/goa-ai/runtime/agents/policy"},
			{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "runtime"},
			// Aggregated specs for registry references
			{Path: agent.ToolSpecsImportPath, Name: agent.ToolSpecsPackage},
		}
		// No per-toolset short type imports or service imports are required in executor-first API.
		data := serviceToolsetFileData{
			PackageName: ts.PackageName,
			Agent:       agent,
			Toolset:     ts,
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" service toolset", ts.PackageName, imports),
			{
				Name:    "service-toolset",
				Source:  agentsTemplates.Read(serviceToolsetFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "service_toolset.go")
		out = append(out, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return out
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
		{Path: "goa.design/goa-ai/runtime/agents/runtime", Name: "agentsruntime"},
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
		{Path: "goa.design/goa-ai/runtime/agents/planner"},
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
		{Path: "goa.design/goa-ai/runtime/agents/runtime"},
		{Path: "goa.design/goa-ai/runtime/agents/planner"},
	}
	// Import specs package for typed payloads and transforms.
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	// Build tool switch metadata
	type execTool struct {
		Qualified        string
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
			Qualified:        t.QualifiedName,
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
