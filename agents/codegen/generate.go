package codegen

import (
	"path/filepath"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
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

	agentToolsetFileData struct {
		PackageName string
		Toolset     *ToolsetData
	}

	serviceToolsetFileData struct {
		PackageName string
		Agent       *AgentData
		Toolset     *ToolsetData
	}
)

const bootstrapMainSnippet = `
	rt, cleanup, err := bootstrapAgents(context.Background())
	if err != nil {
		panic(err)
	}
	defer cleanup()
	_ = rt

`

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
//   - tool_specs/: tool specifications, types, and codecs
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
//	gen/<service>/agents/<agent>/tool_specs/*.go
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
			files, err := agentFiles(agent)
			if err != nil {
				return nil, err
			}
			generated = append(generated, files...)
		}
	}

	return append(files, generated...), nil
}

// ModifyExampleFiles appends a service-local bootstrap helper and planner stub(s)
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
func ModifyExampleFiles(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	data, err := buildGeneratorData(genpkg, roots)
	if err != nil {
		return nil, err
	}
	if len(data.Services) == 0 {
		return files, nil
	}

	// For each service that has at least one agent, emit cmd/<service>/agents_bootstrap.go
	// and a trivial planner stub per agent under the application module root.
	for _, svc := range data.Services {
		if len(svc.Agents) == 0 {
			continue
		}
		// Bootstrap helper in cmd/<service>/agents_bootstrap.go
		bfile := emitBootstrapHelper(svc)
		if bfile != nil {
			files = append(files, bfile)
		}
		// Patch cmd/<service>/main.go to call bootstrapAgents(ctx)
    files = patchServiceMainForBootstrap(svc, files)
		// Planner stubs at module root: <service>_<agent>_planner.go
		for _, ag := range svc.Agents {
			if f := emitPlannerStub(svc, ag); f != nil {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

func agentFiles(agent *AgentData) ([]*codegen.File, error) {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentWorkflowFile(agent),
		agentActivitiesFile(agent),
		agentRegistryFile(agent),
	}
	toolSpecFiles, err := agentToolSpecsFiles(agent)
	if err != nil {
		return nil, err
	}
	files = append(files, toolSpecFiles...)
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, serviceToolsetFiles(agent)...)

	var filtered []*codegen.File
	for _, f := range files {
		if f != nil {
			filtered = append(filtered, f)
		}
	}
	return filtered, nil
}

// emitBootstrapHelper constructs a codegen.File for cmd/<service>/agents_bootstrap.go.
// It wires a Temporal engine (default options), in-memory stores, and calls each
// Register<Agent> with a placeholder planner constructor.
func emitBootstrapHelper(svc *ServiceAgentsData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/agents/runtime/runtime", Name: "agentsruntime"},
		{Path: "goa.design/goa-ai/agents/runtime/memory/inmem", Name: "meminmem"},
		{Path: "goa.design/goa-ai/agents/runtime/run/inmem", Name: "runinmem"},
		{Path: "goa.design/goa-ai/agents/runtime/engine/temporal", Name: "runtimeTemporal"},
		{Path: "go.temporal.io/sdk/client", Name: "temporalclient"},
	}
	needsMCP := false
	for _, ag := range svc.Agents {
		if len(ag.MCPToolsets) > 0 {
			needsMCP = true
			break
		}
	}
	if needsMCP {
		imports = append(imports,
			&codegen.ImportSpec{Path: "fmt"},
			&codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/features/mcp/runtime"},
		)
	}
	// Import agent registration packages
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
	}
	path := filepath.Join("cmd", svc.Service.PathName, "agents_bootstrap.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("Agents bootstrap helper", "main", imports),
		{
			Name:   "agents-bootstrap",
			Source: agentsTemplates.Read(bootstrapHelperT),
			Data:   svc,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// emitPlannerStub creates a minimal planner implementation file at module root
// named <service>_<agent>_planner.go so example builds compile without custom code.
func emitPlannerStub(svc *ServiceAgentsData, ag *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/agents/runtime/planner"},
	}
	name := filepath.Join("cmd", svc.Service.PathName, "agents_planner_"+ag.PathName+".go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("Example planner stub for "+ag.Name, "main", imports),
		{Name: "planner-stub", Source: agentsTemplates.Read(plannerStubT), Data: ag},
	}
	return &codegen.File{Path: name, SectionTemplates: sections}
}

// patchServiceMainForBootstrap locates cmd/<service>/main.go and inserts a call
// to bootstrapAgents(ctx) after a context/logger setup if present, or at the
// start of main() otherwise. It also adds necessary imports.
const sectionSourceHeader = "source-header"

func patchServiceMainForBootstrap(svc *ServiceAgentsData, files []*codegen.File) []*codegen.File {
    mainPath := filepath.Join("cmd", svc.Service.PathName, "main.go")
    var fmain *codegen.File
    for _, f := range files {
        if f.Path == mainPath {
            fmain = f
            break
        }
    }
    if fmain == nil {
        return files
    }
    // Add context import if missing (header import management is idempotent)
    for _, s := range fmain.SectionTemplates {
        if s.Name == sectionSourceHeader {
            codegen.AddImport(s, &codegen.ImportSpec{Path: "context"})
            break
        }
    }
	// If the snippet already exists, replace it with the canonical version to keep
	// future runs idempotent.
    const snippetHead = "\n\trt, cleanup, err := bootstrapAgents("
    const snippetTail = "\n\t_ = rt\n"
    for _, s := range fmain.SectionTemplates {
        if s.Name == sectionSourceHeader {
            continue
        }
        start := strings.Index(s.Source, snippetHead)
        if start < 0 {
            continue
        }
        end := strings.Index(s.Source[start:], snippetTail)
        if end < 0 {
            continue
        }
        end += start + len(snippetTail)
        s.Source = s.Source[:start] + bootstrapMainSnippet + s.Source[end:]
        return files
    }
    // Insert bootstrap call inside main function body
    for _, s := range fmain.SectionTemplates {
        if s.Name == sectionSourceHeader {
            continue
        }
        src := s.Source
        const mainSig = "func main() {"
        idx := findSubstring(src, mainSig)
        if idx < 0 {
            continue
        }
        sigEnd := idx + len(mainSig)
        insert := "\n" + bootstrapMainSnippet
        s.Source = src[:sigEnd] + insert + src[sigEnd:]
        break
    }
    return files
}

func agentImplFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/agents/runtime/engine"},
		{Path: "goa.design/goa-ai/agents/runtime/runtime", Name: "runtime"},
		{Path: "goa.design/goa-ai/agents/runtime/planner"},
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
		{Path: "goa.design/goa-ai/agents/runtime/planner"},
	}
	if len(agent.MCPToolsets) > 0 {
		imports = append(imports,
			&codegen.ImportSpec{Path: "fmt"},
			&codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/features/mcp/runtime"},
		)
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
		{Path: "goa.design/goa-ai/agents/runtime/engine"},
		{Path: "goa.design/goa-ai/agents/runtime/runtime"},
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
		{Path: "goa.design/goa-ai/agents/runtime/engine"},
		{Name: "agentsruntime", Path: "goa.design/goa-ai/agents/runtime/runtime"},
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
		{Path: "goa.design/goa-ai/agents/runtime/engine"},
		{Path: "goa.design/goa-ai/agents/runtime/runtime", Name: "agentsruntime"},
	}
	if len(agent.MCPToolsets) > 0 {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
	}
	// Import toolset packages that have method-backed tools so we can call their registration helpers.
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
	if needsTimeImport(agent) {
		imports = append(imports, &codegen.ImportSpec{Path: "time"})
	}
	if len(agent.MCPToolsets) > 0 {
		added := make(map[string]struct{})
		for _, meta := range agent.MCPToolsets {
			if meta.HelperImportPath == "" {
				continue
			}
			if _, ok := added[meta.HelperImportPath]; ok {
				continue
			}
			imports = append(imports, &codegen.ImportSpec{Path: meta.HelperImportPath, Name: meta.HelperAlias})
			added[meta.HelperImportPath] = struct{}{}
		}
	}
	imports = append(imports, &codegen.ImportSpec{Path: agent.ToolSpecsImportPath, Name: agent.ToolSpecsPackage})
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" registry", agent.PackageName, imports),
		{
			Name:    "agent-registry",
			Source:  agentsTemplates.Read(registryFileT),
			Data:    agent,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "registry.go"),
		SectionTemplates: sections,
	}
}

func agentToolSpecsFiles(agent *AgentData) ([]*codegen.File, error) {
	if len(agent.Tools) == 0 {
		return nil, nil
	}
	data, err := buildToolSpecsData(agent)
	if err != nil {
		return nil, err
	}
	var files []*codegen.File
	if pure := data.pureTypes(); len(pure) > 0 {
		sections := []*codegen.SectionTemplate{
			codegen.Header(agent.StructName+" tool types", agent.ToolSpecsPackage, data.typeImports()),
			{
				Name:   "tool-spec-types",
				Source: agentsTemplates.Read(toolTypesFileT),
				Data:   toolTypesFileData{Types: pure},
			},
		}
		files = append(files, &codegen.File{
			Path:             filepath.Join(agent.ToolSpecsDir, "types.go"),
			SectionTemplates: sections,
		})
	}
	if len(data.tools) > 0 {
		codecsSections := []*codegen.SectionTemplate{
			codegen.Header(agent.StructName+" tool codecs", agent.ToolSpecsPackage, data.codecsImports()),
			{
				Name:   "tool-spec-codecs",
				Source: agentsTemplates.Read(toolCodecsFileT),
				Data:   toolCodecsFileData{Types: data.typesList(), Tools: data.tools},
			},
		}
		files = append(files, &codegen.File{
			Path:             filepath.Join(agent.ToolSpecsDir, "codecs.go"),
			SectionTemplates: codecsSections,
		})

		specImports := []*codegen.ImportSpec{
			{Path: "sort"},
			{Path: "goa.design/goa-ai/agents/runtime/policy"},
			{Path: "goa.design/goa-ai/agents/runtime/tools"},
		}
		specSections := []*codegen.SectionTemplate{
			codegen.Header(agent.StructName+" tool specs", agent.ToolSpecsPackage, specImports),
			{
				Name:   "tool-specs",
				Source: agentsTemplates.Read(toolSpecFileT),
				Data: toolSpecFileData{
					PackageName: agent.ToolSpecsPackage,
					Tools:       data.tools,
					Types:       data.typesList(),
				},
			},
		}
		files = append(files, &codegen.File{
			Path:             filepath.Join(agent.ToolSpecsDir, "specs.go"),
			SectionTemplates: specSections,
		})
	}
	return files, nil
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
			{Path: "goa.design/goa-ai/agents/runtime/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/agents/runtime/tools"},
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

// serviceToolsetFiles generates a per-agent toolset registration for tools that are
// explicitly bound to service methods via Method(). It creates a thin registration
// file that will later be expanded to include per-tool adapters. For now, we emit
// the registration skeleton so Method() influences codegen structure.
func serviceToolsetFiles(agent *AgentData) []*codegen.File {
	// Collect toolsets with at least one method-backed tool
	out := []*codegen.File{}
	for _, ts := range agent.AllToolsets {
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
		// Build imports including the owning service package (for method types)
		svcImportPath := joinImportPath(agent.Genpkg, agent.Service.PathName)
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "errors"},
			{Path: "encoding/json"},
			{Path: "goa.design/goa-ai/agents/runtime/engine"},
			{Path: "goa.design/goa-ai/agents/runtime/planner"},
			{Path: "goa.design/goa-ai/agents/runtime/policy"},
			{Path: "goa.design/goa-ai/agents/runtime/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/agents/runtime/tools"},
			{Path: svcImportPath, Name: agent.Service.PkgName},
			{Path: agent.ToolSpecsImportPath, Name: agent.ToolSpecsPackage},
		}
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

// findSubstring returns the first index of sub in s, or -1 if not present.
//
// This is a tiny helper used by the example-phase patcher to locate injection
// points in generated main.go sources. Files are small and the naive search is
// sufficient and easy to reason about.
func findSubstring(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 || n < m {
		return -1
	}
	for i := 0; i <= n-m; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
