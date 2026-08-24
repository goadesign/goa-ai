// Package codegen writes starter application code for generated agents.
//
// The goa example command writes startup, planning, tool execution, and main
// files outside gen. Running it again updates those starter files without
// changing files written by goa gen.
package codegen

import (
	"os"
	"path/filepath"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"

	evalexpr "goa.design/goa-ai/eval/expr"
)

type (
	// quickstartData holds the agents and evaluation suites listed in the
	// generated quickstart guide.
	quickstartData struct {
		*GeneratorData
		// Suites lists the declared evaluation suites in declaration order.
		Suites []*quickstartSuiteData
	}

	// quickstartSuiteData describes one evaluation suite in the generated guide.
	quickstartSuiteData struct {
		// Name is the suite identifier, e.g. "chat_quality".
		Name string
		// Description explains the evaluated capability.
		Description string
		// Agent is the qualified agent the suite is attached to, e.g.
		// "orchestrator.chat". Empty for top-level suites.
		Agent string
		// Scenarios lists the suite cases in declaration order.
		Scenarios []*quickstartScenarioData
	}

	// quickstartScenarioData describes one evaluation scenario for the
	// generated guide.
	quickstartScenarioData struct {
		// Name is the scenario identifier.
		Name string
		// Description explains the evaluated behavior.
		Description string
		// Tags classify the scenario for runner selection.
		Tags []string
		// HasInput reports whether the scenario hook receives a typed input.
		HasInput bool
	}
)

// generateExampleFiles adds startup code and starter implementations for each
// generated agent.
func generateExampleFiles(data *GeneratorData, files []*codegen.File) ([]*codegen.File, error) {
	if len(data.Services) == 0 {
		return files, nil
	}

	// Write application-owned files under internal/agents without changing an
	// existing main package.
	moduleBase := moduleBaseImport(data.Genpkg)
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
			// Write one starter executor for each local toolset.
			for _, ts := range ag.AllToolsets {
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

// emitInternalBootstrap writes the startup package used by one service
// command. The package starts only the agents declared by that service.
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
	// Import each generated agent, its planner, and its starter executors.
	type toolsetImport struct{ Alias, Path string }
	type exampleToolsetImport struct {
		ExecutorAlias string
		Toolset       *ToolsetData
	}
	type agentImport struct {
		Alias, Path, PlannerAlias, PlannerPath string
		Toolsets                               []toolsetImport
		ExampleToolsets                        []exampleToolsetImport
		Agent                                  *AgentData
	}
	agents := make([]agentImport, 0, len(svc.Agents))
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
		palias := "planner" + ag.PathName
		ppath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "planner"))
		imports = append(imports, &codegen.ImportSpec{Path: ppath, Name: palias})
		// Import the application code that runs tools handled by service methods.
		var tsImports []toolsetImport
		for _, ts := range ag.MethodBackedToolsets {
			tpath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "toolsets", ts.PathName))
			talias := "toolset" + ag.PathName + ts.PathName
			imports = append(imports, &codegen.ImportSpec{Path: tpath, Name: talias})
			tsImports = append(tsImports, toolsetImport{Alias: talias, Path: tpath})
		}
		var exampleToolsets []exampleToolsetImport
		for _, ts := range ag.UsedToolsets {
			if !starterExecutorToolset(ts) || toolsetHasMethod(ts) {
				continue
			}
			executorAlias := "toolset" + ag.PathName + ts.PathName
			executorPath := filepath.ToSlash(
				filepath.Join(moduleBase, "internal", "agents", ag.PathName, "toolsets", ts.PathName),
			)
			imports = append(imports, &codegen.ImportSpec{Path: executorPath, Name: executorAlias})
			exampleToolsets = append(exampleToolsets, exampleToolsetImport{
				ExecutorAlias: executorAlias,
				Toolset:       ts,
			})
		}
		agents = append(agents, agentImport{
			Alias:           ag.PackageName,
			Path:            ag.ImportPath,
			PlannerAlias:    palias,
			PlannerPath:     ppath,
			Toolsets:        tsImports,
			ExampleToolsets: exampleToolsets,
			Agent:           ag,
		})
	}
	path := filepath.Join("internal", "agents", svc.Service.PathName, "bootstrap", "bootstrap.go")
	sections := []*codegen.SectionTemplate{
		applicationHeader(
			"bootstrap",
			"wires the goa-ai runtime and registers generated agents.",
			imports,
		),
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
	return &codegen.File{Path: path, SectionTemplates: sections, SkipExist: true}
}

// emitPlannerInternalStub writes a starter planner that makes one sample tool
// call when the design provides complete examples.
func emitPlannerInternalStub(_ string, ag *AgentData) *codegen.File {
	if ag == nil {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	var exampleTool *toolEntry
	var exampleToolset *ToolsetData
	for _, toolset := range ag.UsedToolsets {
		if !starterExecutorToolset(toolset) {
			continue
		}
		for _, tool := range toolset.specs.tools {
			if tool.Payload == nil || len(tool.Payload.ExampleJSON) == 0 || tool.Bounds != nil {
				continue
			}
			if tool.HasResult && (tool.Result == nil || len(tool.Result.ScaffoldExampleJSON) == 0) {
				continue
			}
			exampleTool = tool
			break
		}
		if exampleTool == nil {
			continue
		}
		exampleToolset = toolset
		imports = append(imports,
			&codegen.ImportSpec{Path: "fmt"},
			&codegen.ImportSpec{Path: toolset.SpecsImportPath, Name: "gentool"},
		)
		break
	}
	data := struct {
		Agent   *AgentData
		Toolset *ToolsetData
		Tool    *toolEntry
	}{
		Agent:   ag,
		Toolset: exampleToolset,
		Tool:    exampleTool,
	}
	sections := []*codegen.SectionTemplate{
		applicationHeader(
			"planner",
			"contains the example planner for "+ag.StructName+".",
			imports,
		),
		{Name: "planner-internal-stub", Source: agentsTemplates.Read(plannerInternalStubT), Data: data},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "planner", "planner.go")
	return &codegen.File{Path: path, SectionTemplates: sections, SkipExist: true}
}

// emitExecutorInternalStub writes starter execution code for one local
// toolset. It uses authored examples until the application supplies real work.
func emitExecutorInternalStub(ag *AgentData, ts *ToolsetData) *codegen.File {
	if !starterExecutorToolset(ts) {
		return nil
	}
	agentImport := &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName}
	imports := make([]*codegen.ImportSpec, 0, 6)
	imports = append(imports,
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/runtime"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/planner"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"},
	)
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	tools := make([]*exampleExecutorToolData, 0, len(ts.specs.tools))
	register := false
	needsFmt := false
	needsRawJSON := false
	for _, tool := range ts.Tools {
		register = register || tool.IsMethodBacked
	}
	for _, tool := range ts.specs.tools {
		entry := &exampleExecutorToolData{
			ID:               tool.Name,
			ConstName:        tool.ConstName,
			TypedTool:        tool.TypedToolVar,
			InjectDecodeFunc: tool.Payload.InjectDecodeFunc,
			HasResult:        tool.HasResult,
		}
		if tool.HasResult {
			needsFmt = true
			entry.ResultExample = string(tool.Result.ScaffoldExampleJSON)
			entry.HasResultExample = len(tool.Result.ScaffoldExampleJSON) > 0
			needsRawJSON = needsRawJSON || entry.HasResultExample
		}
		tools = append(tools, entry)
	}
	if needsFmt {
		imports = append(imports, codegen.SimpleImport("fmt"))
	}
	if needsRawJSON {
		imports = append(imports, &codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/rawjson"})
	}
	if register {
		imports = append(imports, agentImport)
	}
	sections := []*codegen.SectionTemplate{
		applicationHeader(
			ts.PathName,
			"contains the "+ts.Name+" executor for "+ag.StructName+".",
			imports,
		),
		{
			Name:   "example-executor-stub",
			Source: agentsTemplates.Read(exampleExecutorStubT),
			Data: exampleExecutorFileData{
				Agent:       ag,
				Toolset:     ts,
				AgentImport: agentImport,
				SpecsAlias:  specsAlias,
				Tools:       tools,
				Register:    register,
			},
			FuncMap: templateFuncMap(),
		},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "toolsets", ts.PathName, "execute.go")
	return &codegen.File{Path: path, SectionTemplates: sections, SkipExist: true}
}

// starterExecutorToolset reports whether goa example can write a local
// executor for the toolset.
func starterExecutorToolset(toolset *ToolsetData) bool {
	return toolset != nil && !toolset.IsRegistryBacked && toolset.MCP == nil &&
		toolset.SpecsImportPath != "" && toolset.specs != nil && len(toolset.specs.tools) > 0
}

// toolsetHasMethod reports whether a generated service executor already
// registers the toolset.
func toolsetHasMethod(toolset *ToolsetData) bool {
	for _, tool := range toolset.Tools {
		if tool.IsMethodBacked {
			return true
		}
	}
	return false
}

// applicationHeader writes the package comment and imports for application
// code. These files are created once and belong to the application afterward.
func applicationHeader(packageName, purpose string, imports []*codegen.ImportSpec) *codegen.SectionTemplate {
	const source = `// Package {{ .Package }} {{ .Purpose }}
// Goa creates this file only when it does not already exist. The application
// owns all later edits.
package {{ .Package }}
{{ if .Imports }}
import (
{{ range .Imports }}	{{ if .Name }}{{ .Name }} {{ end }}"{{ .Path }}"
{{ end }})
{{ end }}`
	return &codegen.SectionTemplate{
		Name:   "application-source-header",
		Source: source,
		Data: map[string]any{
			"Package": packageName,
			"Purpose": purpose,
			"Imports": imports,
		},
	}
}

// quickstartReadmeFile builds the contextual quickstart README at the module
// root. The file is named AGENTS_QUICKSTART.md and is generated unless disabled
// via DSL. It documents the design's agents plus any declared evaluation
// suites, so roots must include the evaluated eval DSL root when present.
func quickstartReadmeFile(data *GeneratorData, roots []eval.Root) *codegen.File {
	docData := agentQuickstartData(data, roots)
	if docData == nil {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		{
			Name:    "agents-quickstart",
			Source:  agentsTemplates.Read(quickstartReadmeT),
			Data:    docData,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: "AGENTS_QUICKSTART.md", SectionTemplates: sections}
}

// agentQuickstartData filters generator data down to the services that actually
// declare agents so agent quickstart docs never assume agentless services, and
// gathers the evaluation suites declared in the design.
func agentQuickstartData(data *GeneratorData, roots []eval.Root) *quickstartData {
	if data == nil {
		return nil
	}
	doc := &quickstartData{GeneratorData: &GeneratorData{Genpkg: data.Genpkg}}
	for _, svc := range data.Services {
		if svc == nil || len(svc.Agents) == 0 {
			continue
		}
		doc.Services = append(doc.Services, svc)
	}
	if len(doc.Services) == 0 {
		return nil
	}
	doc.Suites = quickstartSuites(roots)
	return doc
}

// quickstartSuites projects the evaluated eval DSL root, when present, into
// the per-suite facts the quickstart guide renders.
func quickstartSuites(roots []eval.Root) []*quickstartSuiteData {
	var root *evalexpr.RootExpr
	for _, r := range roots {
		if er, ok := r.(*evalexpr.RootExpr); ok {
			root = er
			break
		}
	}
	if root == nil {
		return nil
	}
	suites := make([]*quickstartSuiteData, 0, len(root.Suites))
	for _, suite := range root.Suites {
		doc := &quickstartSuiteData{
			Name:        suite.Name,
			Description: suite.Description,
			Scenarios:   make([]*quickstartScenarioData, 0, len(suite.Scenarios)),
		}
		if suite.Agent != nil {
			doc.Agent = suite.Agent.Service.Name + "." + suite.Agent.Name
		}
		for _, scenario := range suite.Scenarios {
			doc.Scenarios = append(doc.Scenarios, &quickstartScenarioData{
				Name:        scenario.Name,
				Description: scenario.Description,
				Tags:        scenario.Tags,
				HasInput:    scenario.Input != nil,
			})
		}
		suites = append(suites, doc)
	}
	return suites
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
	completions := make([]*CompletionData, 0, len(svc.Completions))
	for _, completion := range svc.Completions {
		if completion != nil && completion.HasExample {
			completions = append(completions, completion)
		}
	}

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
		{Path: filepath.ToSlash(filepath.Join(
			moduleBase,
			"internal",
			"agents",
			svc.Service.PathName,
			"bootstrap",
		))},
		{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
	}
	if len(completions) > 0 {
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
			Completions: completions,
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
