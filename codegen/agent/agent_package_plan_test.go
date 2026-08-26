// These tests compare the names claimed from Goa with the declarations emitted
// by every template in an agent package. Receiver methods are checked separately
// because they share a namespace with fields on their receiver, not package names.
package codegen

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
)

func TestAgentPackagePlanMovesDerivedNamesAroundPackageCollisions(t *testing.T) {
	const importPath = "generated.local/gen/alpha/agents/scribe"
	generation, err := goacodegen.NewGeneration("generated.local/gen", nil)
	require.NoError(t, err)
	pkg, err := generation.ClaimPackage(importPath)
	require.NoError(t, err)
	for _, collision := range []struct {
		kind goacodegen.PackageNameKind
		name string
	}{
		{goacodegen.NameType, "ScribeAgent"},
		{goacodegen.NameType, "ScribeAgentConfig"},
		{goacodegen.NameConstant, "SharedToolsetName"},
	} {
		require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(collision.kind, collision.name)))
	}

	agent := packagePlanTestAgent(importPath)
	planned, err := planAgentPackages(generation, &agentir.Design{Agents: []*agentir.Agent{agent}})
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())

	data := packagePlanTestData(agent)
	require.NoError(t, planned.link(data))
	linked := data.Services[0].Agents[0]
	require.Equal(t, "AgentID", linked.PackageNames.AgentID)
	require.Equal(t, "WorkflowName", linked.PackageNames.WorkflowName)
	require.Equal(t, "NewWorker", linked.PackageNames.NewWorker)
	require.Equal(t, "Route", linked.PackageNames.Route)
	require.Equal(t, "NewClient", linked.PackageNames.NewClient)
	require.Equal(t, "RegisterUsedToolsets", linked.PackageNames.RegisterUsedToolsets)
	require.Equal(t, "ScribeAgent2", linked.StructName)
	require.Equal(t, "ScribeAgentConfig2", linked.ConfigType)
	require.Equal(t, "NewScribeAgent2", linked.PackageNames.Constructor)
	require.Equal(t, "SharedToolsetName2", linked.UsedToolsets[0].RegistrationNameConst)
}

func TestAgentPackagePlanCoversEveryRenderedDeclaration(t *testing.T) {
	const importPath = "generated.local/gen/alpha/agents/scribe"
	generation, err := goacodegen.NewGeneration("generated.local/gen", nil)
	require.NoError(t, err)
	agent := packagePlanTestAgent(importPath)
	planned, err := planAgentPackages(generation, &agentir.Design{Agents: []*agentir.Agent{agent}})
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())

	data := packagePlanTestData(agent)
	require.NoError(t, planned.link(data))
	linked := data.Services[0].Agents[0]

	sources := []string{
		renderAgentPackageTemplate(t, agentFileT, linked.packageFiles.implementation),
		renderAgentPackageTemplate(t, configFileT, linked.packageFiles.config),
		renderAgentPackageTemplate(t, registryFileT, newAgentRegistryFileData(linked)),
		renderAgentPackageTemplate(t, agentToolsConsumerT, agentToolsetConsumerFileData{
			Toolset:                         linked.UsedToolsets[2],
			RuntimeAlias:                    "runtime",
			ProviderAlias:                   "provider",
			ProviderRegistrationConstructor: "NewRegistration",
		}),
	}

	packageNames := make(map[string]struct{})
	receiverMethods := make(map[string]struct{})
	configFields := make(map[string]struct{})
	for _, source := range sources {
		file, parseErr := parser.ParseFile(token.NewFileSet(), "generated.go", "package scribe\n"+source, parser.ParseComments)
		require.NoError(t, parseErr, source)
		collectRenderedNames(file, packageNames, receiverMethods, configFields, linked.ConfigType)
	}

	require.Equal(t, plannedAgentPackageNames(planned.byAgent[agent.ID]), packageNames)
	require.Equal(t, map[string]struct{}{
		"Validate":      {},
		"WithMCPCaller": {},
	}, receiverMethods)
	for method := range receiverMethods {
		_, collides := configFields[method]
		require.Falsef(t, collides, "config field and method both use %q", method)
	}
}

func packagePlanTestAgent(importPath string) *agentir.Agent {
	localExpr := &agentexpr.ToolsetExpr{
		Name:  "shared",
		Tools: []*agentexpr.ToolExpr{{Name: "ping", CallHintTemplate: "Call {{ .Payload }}"}},
	}
	mcpExpr := &agentexpr.ToolsetExpr{
		Name:     "remote",
		Provider: &agentexpr.ProviderExpr{Kind: agentexpr.ProviderMCP},
	}
	agentToolsExpr := &agentexpr.ToolsetExpr{
		Name:  "writer",
		Tools: []*agentexpr.ToolExpr{{Name: "write"}},
	}
	localDefinition := &agentir.Toolset{Expr: localExpr}
	mcpDefinition := &agentir.Toolset{Expr: mcpExpr}
	agentToolsDefinition := &agentir.Toolset{Expr: agentToolsExpr}
	return &agentir.Agent{
		Expr:       &agentexpr.AgentExpr{RunPolicy: &agentexpr.RunPolicyExpr{}},
		Name:       "scribe",
		ID:         "alpha.scribe",
		ImportPath: importPath,
		StructName: "ScribeAgent",
		ConfigType: "ScribeAgentConfig",
		UsedToolsets: []*agentir.ToolsetRef{
			{
				Expr:             localExpr,
				Definition:       localDefinition,
				Name:             "shared",
				Slug:             "shared",
				QualifiedName:    "alpha.shared",
				SpecsPackageName: "shared",
				SpecsImportPath:  "generated.local/gen/alpha/toolsets/shared",
			},
			{
				Expr:              mcpExpr,
				Definition:        mcpDefinition,
				Name:              "remote",
				Slug:              "remote",
				QualifiedName:     "remote.tools",
				PackageName:       "remote",
				PackageImportPath: "generated.local/gen/alpha/agents/scribe/remote",
				SpecsPackageName:  "remote",
				SpecsImportPath:   "generated.local/gen/remote/toolsets/tools",
				Provider: &agentir.ToolsetProvider{
					Kind: agentexpr.ProviderMCP,
					MCP: &agentir.MCPToolsetMeta{
						QualifiedName: "remote.tools",
						ConstName:     "ScribeRemoteToolsetID",
					},
				},
			},
			{
				Expr:                 agentToolsExpr,
				Definition:           agentToolsDefinition,
				Name:                 "writer",
				Slug:                 "writer",
				QualifiedName:        "alpha.writer",
				AgentToolsImportPath: "generated.local/gen/alpha/agents/writer/agenttools/writer",
			},
		},
	}
}

func packagePlanTestData(agent *agentir.Agent) *GeneratorData {
	local := &ToolsetData{
		Expr:             agent.UsedToolsets[0].Expr,
		QualifiedName:    "alpha.shared",
		PathName:         "shared",
		SpecsPackageName: "shared",
		SpecsImportPath:  agent.UsedToolsets[0].SpecsImportPath,
		Tools: []*ToolData{{
			Name:             "ping",
			QualifiedName:    "shared.ping",
			CallHintTemplate: "Call {{ .Payload }}",
		}},
	}
	mcpMeta := &MCPToolsetMeta{QualifiedName: "remote.tools", ConstName: "ScribeRemoteToolsetID"}
	mcp := &ToolsetData{
		Expr:              agent.UsedToolsets[1].Expr,
		QualifiedName:     "remote.tools",
		PathName:          "remote",
		MCP:               mcpMeta,
		PackageImportPath: agent.UsedToolsets[1].PackageImportPath,
		SpecsImportPath:   agent.UsedToolsets[1].SpecsImportPath,
	}
	agentTools := &ToolsetData{
		Expr:                 agent.UsedToolsets[2].Expr,
		Name:                 "writer",
		QualifiedName:        "alpha.writer",
		PathName:             "writer",
		AgentToolsImportPath: agent.UsedToolsets[2].AgentToolsImportPath,
		Tools:                []*ToolData{{Name: "write"}},
	}
	linked := &AgentData{
		Name:         agent.Name,
		ID:           agent.ID,
		StructName:   agent.StructName,
		ConfigType:   agent.ConfigType,
		UsedToolsets: []*ToolsetData{local, mcp, agentTools},
		AllToolsets:  []*ToolsetData{local, mcp, agentTools},
		MCPToolsets:  []*MCPToolsetMeta{mcpMeta},
		Runtime: RuntimeData{
			Workflow:       WorkflowArtifact{Name: "alpha.scribe.workflow", Queue: "alpha_scribe_workflow"},
			PlanActivity:   &ActivityArtifact{Name: "alpha.scribe.plan"},
			ResumeActivity: &ActivityArtifact{Name: "alpha.scribe.resume"},
			ExecuteTool:    &ActivityArtifact{Name: "alpha.scribe.execute_tool"},
		},
	}
	return &GeneratorData{Services: []*ServiceAgentsData{{Agents: []*AgentData{linked}}}}
}

func renderAgentPackageTemplate(t *testing.T, name string, data any) string {
	t.Helper()
	parsed, err := template.New(name).Funcs(templateFuncMap()).Parse(agentsTemplates.Read(name))
	require.NoError(t, err)
	var source bytes.Buffer
	require.NoError(t, parsed.Execute(&source, data))
	return source.String()
}

func collectRenderedNames(file *ast.File, packageNames, receiverMethods, configFields map[string]struct{}, configType string) {
	for _, declaration := range file.Decls {
		switch current := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range current.Specs {
				switch named := spec.(type) {
				case *ast.TypeSpec:
					packageNames[named.Name.Name] = struct{}{}
					if named.Name.Name == configType {
						if fields, ok := named.Type.(*ast.StructType); ok {
							for _, field := range fields.Fields.List {
								for _, name := range field.Names {
									configFields[name.Name] = struct{}{}
								}
							}
						}
					}
				case *ast.ValueSpec:
					for _, name := range named.Names {
						packageNames[name.Name] = struct{}{}
					}
				}
			}
		case *ast.FuncDecl:
			if current.Recv == nil {
				packageNames[current.Name.Name] = struct{}{}
			} else {
				receiverMethods[current.Name.Name] = struct{}{}
			}
		}
	}
}

func plannedAgentPackageNames(planned *agentPackagePlan) map[string]struct{} {
	names := make(map[string]struct{})
	add := func(declaration *goacodegen.NameDeclaration) {
		if declaration != nil {
			names[declaration.Name()] = struct{}{}
		}
	}
	for _, declaration := range planned.fixed {
		add(declaration)
	}
	for _, declaration := range []*goacodegen.NameDeclaration{
		planned.configType,
		planned.structType,
		planned.constructor,
		planned.register,
		planned.usedOptions,
	} {
		add(declaration)
	}
	for _, declaration := range planned.mcp {
		add(declaration)
	}
	for _, toolset := range planned.used {
		add(toolset.routeConstant)
		add(toolset.executorOption)
		add(toolset.materializerOption)
		add(toolset.hintInstaller)
	}
	for _, declaration := range planned.agentToolsConsumers {
		add(declaration)
	}
	return names
}
