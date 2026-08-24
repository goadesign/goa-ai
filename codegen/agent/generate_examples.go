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

	evalexpr "goa.design/goa-ai/eval/expr"
)

type (
	// quickstartData feeds the AGENTS_QUICKSTART.md template. It bundles the
	// agent-bearing services with the evaluation suites declared in the design
	// so the generated guide documents both.
	quickstartData struct {
		*GeneratorData
		// Suites lists the declared evaluation suites in declaration order.
		Suites []*quickstartSuiteData
	}

	// quickstartSuiteData describes one declared evaluation suite for the
	// generated guide.
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
	}
	if needsMCP {
		imports = append(imports, &codegen.ImportSpec{Path: "flag"})
		imports = append(imports, &codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/runtime/mcp"})
	}
	// Import generated agent registration packages and per-agent planner packages.
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
		// Import internal toolset executor packages for method-backed toolsets.
		var tsImports []toolsetImport
		for _, ts := range ag.MethodBackedToolsets {
			tpath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "toolsets", ts.PathName))
			talias := "toolset" + ag.PathName + ts.PathName
			imports = append(imports, &codegen.ImportSpec{Path: tpath, Name: talias})
			tsImports = append(tsImports, toolsetImport{Alias: talias, Path: tpath})
		}
		var exampleToolsets []exampleToolsetImport
		for _, ts := range ag.UsedToolsets {
			if ts == nil || ts.IsRegistryBacked || ts.MCP != nil || ts.SpecsImportPath == "" {
				continue
			}
			hasMethod := false
			for _, tool := range ts.Tools {
				if tool != nil && tool.IsMethodBacked {
					hasMethod = true
					break
				}
			}
			if hasMethod {
				continue
			}
			serviceData := ts.SourceService
			if serviceData == nil {
				serviceData = ag.Service
			}
			specs, err := buildToolSpecsDataFor(ag.Genpkg, serviceData, ts.Tools)
			if err != nil || len(specs.tools) == 0 {
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
	path := filepath.Join("internal", "agents", "bootstrap", "bootstrap.go")
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
	var exampleTool *toolEntry
	var exampleToolset *ToolsetData
	for _, toolset := range ag.UsedToolsets {
		if toolset == nil || toolset.IsRegistryBacked || toolset.MCP != nil || toolset.SpecsImportPath == "" {
			continue
		}
		serviceData := toolset.SourceService
		if serviceData == nil {
			serviceData = ag.Service
		}
		specs, err := buildToolSpecsDataFor(ag.Genpkg, serviceData, toolset.Tools)
		if err != nil || len(specs.tools) == 0 {
			continue
		}
		// The scaffold can demonstrate a complete tool round trip when the
		// design supplies a model payload and either no result or an authored
		// result example. Bounded tools need real executor-owned counts and are
		// not replaced with invented scaffold metadata.
		for _, tool := range specs.tools {
			if len(tool.Payload.ExampleJSON) == 0 || tool.Bounds != nil {
				continue
			}
			if tool.HasResult &&
				(tool.Result == nil || len(tool.Result.ScaffoldExampleJSON) == 0) {
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
			&codegen.ImportSpec{
				Path: toolset.SpecsImportPath,
				Name: "gentool",
			},
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

// emitExecutorInternalStub emits one application-owned executor package for a
// local used toolset. The generated implementation decodes the exact payload
// contract and returns its typed example result until the application replaces
// that result with a service call.
func emitExecutorInternalStub(ag *AgentData, ts *ToolsetData) *codegen.File {
	if ts == nil || ts.IsRegistryBacked || ts.MCP != nil || ts.SpecsImportPath == "" {
		return nil
	}
	agentImport := &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName}
	imports := make([]*codegen.ImportSpec, 0, 6)
	imports = append(imports,
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/runtime"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/planner"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/rawjson"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"},
	)
	// Import specs package for typed payloads and transforms.
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	// Build tool switch metadata.
	type execTool struct {
		ID               string
		ConstName        string
		TypedTool        string
		InjectDecodeFunc string
		ResultExample    string
		HasResult        bool
		HasResultExample bool
	}
	serviceData := ts.SourceService
	if serviceData == nil {
		serviceData = ag.Service
	}
	specs, err := buildToolSpecsDataFor(ag.Genpkg, serviceData, ts.Tools)
	if err != nil || len(specs.tools) == 0 {
		return nil
	}
	tools := make([]execTool, 0, len(specs.tools))
	register := false
	needsFmt := false
	for _, tool := range ts.Tools {
		register = register || tool.IsMethodBacked
	}
	for _, t := range specs.tools {
		tool := execTool{
			ID:               t.Name,
			ConstName:        t.ConstName,
			TypedTool:        t.TypedToolVar,
			InjectDecodeFunc: t.Payload.InjectDecodeFunc,
			HasResult:        t.HasResult,
		}
		if t.HasResult {
			needsFmt = true
			tool.ResultExample = string(t.Result.ScaffoldExampleJSON)
			tool.HasResultExample = len(t.Result.ScaffoldExampleJSON) > 0
		}
		tools = append(tools, tool)
	}
	if needsFmt {
		imports = append(imports, codegen.SimpleImport("fmt"))
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
			Data: struct {
				Agent       *AgentData
				Toolset     *ToolsetData
				AgentImport *codegen.ImportSpec
				SpecsAlias  string
				Tools       []execTool
				Register    bool
			}{ag, ts, agentImport, specsAlias, tools, register},
			FuncMap: templateFuncMap(),
		},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "toolsets", ts.PathName, "execute.go")
	return &codegen.File{Path: path, SectionTemplates: sections, SkipExist: true}
}

// applicationHeader emits the package and imports for a file the application
// edits after Goa creates it. It deliberately omits Go's generated-code marker
// so formatters and linters continue to inspect the file.
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
		{Path: filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", "bootstrap"))},
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
