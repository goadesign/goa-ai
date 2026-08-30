// Package codegen writes starter application code for generated agents.
//
// The goa example command writes startup, planning, tool execution, and main
// files outside gen. Running it again updates those starter files without
// changing files written by goa gen.
package codegen

import (
	"fmt"
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
		// Services lists only services that declare at least one agent.
		Services []*quickstartServiceData
		// HasServiceProviders reports whether the guide needs the section for
		// service-side tool providers.
		HasServiceProviders bool
		// Suites lists the declared evaluation suites in declaration order.
		Suites []*quickstartSuiteData
	}

	// quickstartServiceData contains one service and the agent descriptions
	// written into the generated guide.
	quickstartServiceData struct {
		*ServiceAgentsData
		// Agents contains quickstart-specific data for the service's agents.
		Agents []*quickstartAgentData
	}

	// quickstartAgentData contains one agent and documentation-ready toolset
	// descriptions. Provider labels and example variable names are prepared
	// before the guide is rendered.
	quickstartAgentData struct {
		*AgentData
		// UsedToolsets lists the toolsets this agent calls.
		UsedToolsets []*quickstartToolsetData
		// ExportedToolsets lists the toolsets this agent provides to other agents.
		ExportedToolsets []*quickstartToolsetData
		// MCPToolsets lists the remote callers shown in the configuration example.
		MCPToolsets []*quickstartMCPToolsetData
	}

	// quickstartToolsetData contains the exact label displayed for one toolset.
	quickstartToolsetData struct {
		*ToolsetData
		// ProviderLabel identifies the remote MCP service and toolset. It is empty
		// for toolsets that run locally.
		ProviderLabel string
	}

	// quickstartMCPToolsetData contains one remote MCP caller shown in the guide.
	quickstartMCPToolsetData struct {
		*MCPToolsetMeta
		// CallerName is the lowercase example variable suffix for this suite.
		CallerName string
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

	// exampleBootstrapFileData contains only the names and values printed into
	// one service bootstrap file.
	exampleBootstrapFileData struct {
		Service           *ServiceAgentsData
		Agents            []*exampleBootstrapAgentData
		ClientVersion     string
		ContextAlias      string
		FlagAlias         string
		FmtAlias          string
		AgentRuntimeAlias string
		MCPRuntimeAlias   string
		StorageAlias      string
		HasMCP            bool
	}

	// exampleBootstrapAgentData contains the linked imports and toolsets used to
	// start one agent.
	exampleBootstrapAgentData struct {
		Alias           string
		PlannerAlias    string
		ExampleToolsets []*exampleBootstrapToolsetData
		MCPToolsets     []*exampleBootstrapMCPData
		Agent           *AgentData
	}

	// exampleBootstrapToolsetData contains the executor import chosen for one
	// application-owned toolset.
	exampleBootstrapToolsetData struct {
		ExecutorAlias string
		Toolset       *ToolsetData
	}

	// exampleBootstrapMCPData contains the finalized endpoint names used for one
	// MCP-backed toolset.
	exampleBootstrapMCPData struct {
		*MCPToolsetMeta
		EndpointVar string
		FlagName    string
	}

	// exampleMainFileData contains the final agent import names and completion
	// helpers written into one example main program.
	exampleMainFileData struct {
		Agents           []*exampleMainAgentData
		Completions      []*CompletionData
		ContextAlias     string
		FmtAlias         string
		IOAlias          string
		LogAlias         string
		TimeAlias        string
		BootstrapAlias   string
		CompletionsAlias string
		ModelAlias       string
		RawJSONAlias     string
		RuntimeAlias     string
		StorageAlias     string
	}

	// exampleMainAgentData contains one agent and its final import name in the
	// example main package.
	exampleMainAgentData struct {
		*AgentData
		Alias string
	}
)

// generateExampleFiles adds startup code and starter implementations for each
// generated agent.
func generateExampleFiles(data *GeneratorData, bootstraps *exampleBootstrapPackagesPlan, mains *exampleMainPackagesPlan, apiVersion string, files []*codegen.File) ([]*codegen.File, error) {
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
		f, err := emitInternalBootstrap(svc, bootstraps, apiVersion)
		if err != nil {
			return nil, err
		}
		if f != nil {
			files = append(files, f)
		}
		f, err = emitCmdMain(svc, mains, files)
		if err != nil {
			return nil, err
		}
		if f != nil {
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
func emitInternalBootstrap(svc *ServiceAgentsData, bootstraps *exampleBootstrapPackagesPlan, apiVersion string) (*codegen.File, error) {
	if svc == nil || len(svc.Agents) == 0 {
		return nil, nil
	}
	planned := bootstraps.byService[svc.Service.PathName]
	if planned == nil {
		return nil, fmt.Errorf("service %q has no example bootstrap plan", svc.Service.Name)
	}
	imports := []*codegen.ImportSpec{
		planned.pkg.Import("context"),
		planned.pkg.Import(bootstrapAgentRuntimeImportPath),
		planned.pkg.Import(bootstrapStorageImportPath),
	}
	if svc.HasMCP {
		imports = append(imports, planned.pkg.Import("fmt"))
		imports = append(imports, planned.pkg.Import("flag"))
		imports = append(imports, planned.pkg.Import(bootstrapMCPRuntimeImportPath))
	}
	agents := make([]*exampleBootstrapAgentData, 0, len(svc.Agents))
	for _, ag := range svc.Agents {
		agentPlan := planned.agents[ag.ID]
		if agentPlan == nil {
			return nil, fmt.Errorf("agent %q has no example bootstrap plan", ag.ID)
		}
		imports = append(imports, planned.pkg.Import(agentPlan.agentImportPath))
		imports = append(imports, planned.pkg.Import(agentPlan.plannerImportPath))
		agentData := &exampleBootstrapAgentData{
			Alias:        planned.pkg.ImportName(agentPlan.agentImportPath),
			PlannerAlias: planned.pkg.ImportName(agentPlan.plannerImportPath),
			Agent:        ag,
		}
		for _, ts := range ag.UsedToolsets {
			if !starterExecutorToolset(ts) {
				continue
			}
			executorPath := agentPlan.executorPaths[ts.QualifiedName]
			if executorPath == "" {
				return nil, fmt.Errorf("toolset %q has no example executor import plan", ts.QualifiedName)
			}
			imports = append(imports, planned.pkg.Import(executorPath))
			agentData.ExampleToolsets = append(agentData.ExampleToolsets, &exampleBootstrapToolsetData{
				ExecutorAlias: planned.pkg.ImportName(executorPath),
				Toolset:       ts,
			})
		}
		for _, meta := range ag.MCPToolsets {
			mcpPlan := agentPlan.mcp[meta.QualifiedName]
			if mcpPlan == nil {
				return nil, fmt.Errorf("MCP toolset %q has no example endpoint plan", meta.QualifiedName)
			}
			agentData.MCPToolsets = append(agentData.MCPToolsets, &exampleBootstrapMCPData{
				MCPToolsetMeta: meta,
				EndpointVar:    mcpPlan.endpoint.Name(),
				FlagName:       mcpPlan.flagName,
			})
		}
		agents = append(agents, agentData)
	}
	path := filepath.Join("internal", "agents", svc.Service.PathName, "bootstrap", "bootstrap.go")
	data := exampleBootstrapFileData{
		Service:           svc,
		Agents:            agents,
		ClientVersion:     apiVersion,
		ContextAlias:      planned.pkg.ImportName("context"),
		AgentRuntimeAlias: planned.pkg.ImportName(bootstrapAgentRuntimeImportPath),
		StorageAlias:      planned.pkg.ImportName(bootstrapStorageImportPath),
		HasMCP:            svc.HasMCP,
	}
	if svc.HasMCP {
		data.FlagAlias = planned.pkg.ImportName("flag")
		data.FmtAlias = planned.pkg.ImportName("fmt")
		data.MCPRuntimeAlias = planned.pkg.ImportName(bootstrapMCPRuntimeImportPath)
	}
	sections := []*codegen.SectionTemplate{
		applicationHeader(
			"bootstrap",
			"wires the goa-ai runtime and registers generated agents.",
			imports,
		),
		{
			Name:   "bootstrap-internal",
			Source: agentsTemplates.Read(bootstrapInternalT),
			Data:   data,
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections, SkipExist: true}, nil
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
	needsFmt := false
	needsRawJSON := false
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
				Agent:      ag,
				Toolset:    ts,
				SpecsAlias: specsAlias,
				Tools:      tools,
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
	return toolset != nil && toolset.AgentToolsImportPath == "" &&
		!toolset.IsRegistryBacked && toolset.MCP == nil &&
		toolset.SpecsImportPath != "" && toolset.specs != nil && len(toolset.specs.tools) > 0
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
	doc := &quickstartData{}
	for _, svc := range data.Services {
		if svc == nil || len(svc.Agents) == 0 {
			continue
		}
		serviceDoc := &quickstartServiceData{ServiceAgentsData: svc}
		for _, agent := range svc.Agents {
			agentDoc := &quickstartAgentData{AgentData: agent}
			for _, toolset := range agent.UsedToolsets {
				agentDoc.UsedToolsets = append(agentDoc.UsedToolsets, newQuickstartToolsetData(toolset))
			}
			for _, toolset := range agent.ExportedToolsets {
				agentDoc.ExportedToolsets = append(agentDoc.ExportedToolsets, newQuickstartToolsetData(toolset))
			}
			for _, toolset := range agent.MCPToolsets {
				agentDoc.MCPToolsets = append(agentDoc.MCPToolsets, &quickstartMCPToolsetData{
					MCPToolsetMeta: toolset,
					CallerName:     strings.ToLower(toolset.SuiteName),
				})
			}
			serviceDoc.Agents = append(serviceDoc.Agents, agentDoc)
			if !doc.HasServiceProviders && agentHasServiceProvider(agent) {
				doc.HasServiceProviders = true
			}
		}
		doc.Services = append(doc.Services, serviceDoc)
	}
	if len(doc.Services) == 0 {
		return nil
	}
	doc.Suites = quickstartSuites(roots)
	return doc
}

// newQuickstartToolsetData prepares the optional provider label displayed next
// to a remote MCP toolset.
func newQuickstartToolsetData(toolset *ToolsetData) *quickstartToolsetData {
	data := &quickstartToolsetData{ToolsetData: toolset}
	if toolset.MCP != nil {
		data.ProviderLabel = toolset.MCP.ServiceName + "." + toolset.Name
	}
	return data
}

// agentHasServiceProvider reports whether generation writes a provider that
// receives registry-routed calls for any toolset owned by this agent.
func agentHasServiceProvider(agent *AgentData) bool {
	for _, toolset := range agent.AllToolsets {
		if toolset.SpecsDir == "" || toolset.SourceService == nil || toolset.IsRegistryBacked {
			continue
		}
		for _, tool := range toolset.Tools {
			if tool.IsMethodBacked {
				return true
			}
		}
	}
	return false
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
func emitCmdMain(svc *ServiceAgentsData, mains *exampleMainPackagesPlan, files []*codegen.File) (*codegen.File, error) {
	if svc == nil || len(svc.Agents) == 0 {
		return nil, nil
	}
	planned := mains.byService[svc.Service.PathName]
	if planned == nil {
		return nil, fmt.Errorf("service %q has no example main plan", svc.Service.Name)
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
		planned.pkg.Import("context"),
		planned.pkg.Import("fmt"),
		planned.pkg.Import("log"),
		planned.pkg.Import("time"),
		planned.pkg.Import(planned.bootstrapPath),
		planned.pkg.Import(exampleMainModelImportPath),
		planned.pkg.Import(bootstrapAgentRuntimeImportPath),
		planned.pkg.Import(exampleMainStorageInmemImportPath),
	}
	if len(completions) > 0 {
		imports = append(imports,
			planned.pkg.Import("io"),
			planned.pkg.Import(planned.completionsPath),
			planned.pkg.Import(exampleMainRawJSONImportPath),
		)
	}
	agents := make([]*exampleMainAgentData, 0, len(svc.Agents))
	for _, ag := range svc.Agents {
		importPath := planned.agentPaths[ag.ID]
		if importPath == "" {
			return nil, fmt.Errorf("agent %q has no example main import plan", ag.ID)
		}
		imports = append(imports, planned.pkg.Import(importPath))
		agents = append(agents, &exampleMainAgentData{
			AgentData: ag,
			Alias:     planned.pkg.ImportName(importPath),
		})
	}

	data := exampleMainFileData{
		Agents:         agents,
		Completions:    completions,
		ContextAlias:   planned.pkg.ImportName("context"),
		FmtAlias:       planned.pkg.ImportName("fmt"),
		LogAlias:       planned.pkg.ImportName("log"),
		TimeAlias:      planned.pkg.ImportName("time"),
		BootstrapAlias: planned.pkg.ImportName(planned.bootstrapPath),
		ModelAlias:     planned.pkg.ImportName(exampleMainModelImportPath),
		RuntimeAlias:   planned.pkg.ImportName(bootstrapAgentRuntimeImportPath),
		StorageAlias:   planned.pkg.ImportName(exampleMainStorageInmemImportPath),
	}
	if len(completions) > 0 {
		data.IOAlias = planned.pkg.ImportName("io")
		data.CompletionsAlias = planned.pkg.ImportName(planned.completionsPath)
		data.RawJSONAlias = planned.pkg.ImportName(exampleMainRawJSONImportPath)
	}
	agentSection := &codegen.SectionTemplate{
		Name:    "cmd-main",
		Source:  agentsTemplates.Read(cmdMainT),
		Data:    data,
		FuncMap: templateFuncMap(),
	}

	if file != nil {
		file.SectionTemplates = []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		}
		return nil, nil
	}

	if _, err := os.Stat(mainPath); !os.IsNotExist(err) {
		return nil, nil
	}

	return &codegen.File{
		Path: mainPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("Example main for "+svc.Service.Name, "main", imports),
			agentSection,
		},
	}, nil
}
