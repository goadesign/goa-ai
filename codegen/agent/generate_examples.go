// Package codegen keeps example-only scaffolding separate from the main agent
// generator.
//
// This file owns the `goa example` path: it emits application-side bootstrap,
// planner, executor, and main wiring files that live outside `gen/`. The
// helpers are idempotent over Goa's in-memory example file list so rerunning
// generation updates scaffolded files without affecting the regular `gen`
// output produced by the main generator entrypoint.
package codegen

import (
	"os"
	"path/filepath"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

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

// moduleBaseImport returns the module base import path by stripping trailing
// /gen segments from the generated package import path.
func moduleBaseImport(genpkg string) string {
	base := strings.TrimSuffix(genpkg, "/")
	for strings.HasSuffix(base, "/gen") {
		base = strings.TrimSuffix(base, "/gen")
	}
	return base
}

// emitInternalBootstrap emits internal/agents/bootstrap/bootstrap.go with a
// simple New(ctx) bootstrap for every generated agent in one service.
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
		agents = append(agents, agentImport{
			Alias:        ag.PackageName,
			Path:         ag.ImportPath,
			PlannerAlias: palias,
			PlannerPath:  ppath,
			Toolsets:     tsImports,
			Agent:        ag,
		})
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

// emitPlannerInternalStub emits internal/agents/<agent>/planner/planner.go
// with the minimal planner scaffold for an example application.
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

// emitExecutorInternalStub emits
// internal/agents/<agent>/toolsets/<toolset>/execute.go for method-backed tools.
func emitExecutorInternalStub(ag *AgentData, ts *ToolsetData) *codegen.File {
	src := ts.SourceService
	if src == nil {
		return nil
	}
	// Build imports: agent package for registration.
	agentImport := &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName}
	imports := make([]*codegen.ImportSpec, 0, 6)
	imports = append(imports,
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		agentImport,
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/runtime"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/planner"},
	)
	// Import specs package for typed payloads and transforms.
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	// Build tool switch metadata.
	type execTool struct {
		ID               string
		GoName           string
		PayloadUnmarshal string
		PayloadType      string
	}
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

// quickstartReadmeFile builds the contextual quickstart README at the module
// root. The file is named AGENTS_QUICKSTART.md and is generated unless disabled
// via DSL.
func quickstartReadmeFile(data *GeneratorData) *codegen.File {
	quickstartData := agentQuickstartData(data)
	if quickstartData == nil {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		{
			Name:    "agents-quickstart",
			Source:  agentsTemplates.Read(quickstartReadmeT),
			Data:    quickstartData,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: "AGENTS_QUICKSTART.md", SectionTemplates: sections}
}

// agentQuickstartData filters generator data down to the services that actually
// declare agents so agent quickstart docs never assume agentless services.
func agentQuickstartData(data *GeneratorData) *GeneratorData {
	if data == nil {
		return nil
	}
	quickstartData := &GeneratorData{Genpkg: data.Genpkg}
	for _, svc := range data.Services {
		if svc == nil || len(svc.Agents) == 0 {
			continue
		}
		quickstartData.Services = append(quickstartData.Services, svc)
	}
	if len(quickstartData.Services) == 0 {
		return nil
	}
	return quickstartData
}

// emitCmdMain patches cmd/<service>/main.go for services that expose runnable
// examples. Agent-bearing services always get a main, and the generated example
// also demonstrates service-owned completions when present. If Goa core already
// generated the file in memory, the function rewrites its sections in place.
// Otherwise it creates a new example main unless one already exists on disk.
func emitCmdMain(svc *ServiceAgentsData, moduleBase string, files []*codegen.File) *codegen.File {
	if svc == nil || len(svc.Agents) == 0 {
		return nil
	}
	mainPath := filepath.Join("cmd", svc.Service.PathName, "main.go")

	var file *codegen.File
	for _, f := range files {
		if f.Path == mainPath {
			file = f
			break
		}
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "fmt"},
		{Path: "log"},
		{Path: filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", "bootstrap"))},
		{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
	}
	if len(svc.Completions) > 0 {
		imports = append(imports,
			&codegen.ImportSpec{Path: "io"},
			&codegen.ImportSpec{Path: filepath.ToSlash(filepath.Join(moduleBase, "gen", svc.Service.PathName, "completions"))},
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/rawjson"},
		)
	}
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
	}

	agentSection := &codegen.SectionTemplate{
		Name:   "cmd-main",
		Source: agentsTemplates.Read(cmdMainT),
		Data: struct {
			Agents      []*AgentData
			Completions []*CompletionData
		}{
			Agents:      svc.Agents,
			Completions: svc.Completions,
		},
		FuncMap: templateFuncMap(),
	}

	if file != nil {
		file.SectionTemplates = []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		}
		return nil
	}

	if _, err := os.Stat(mainPath); !os.IsNotExist(err) {
		return nil
	}

	return &codegen.File{
		Path: mainPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		},
	}
}
