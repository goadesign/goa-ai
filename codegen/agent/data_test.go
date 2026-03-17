package codegen_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

const alphaServiceName = "alpha"

func TestBuildGeneratorData(t *testing.T) {
	roots := runAgentDesign(t)
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", roots)
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data.Services, 1)

	svc := data.Services[0]
	require.Equal(t, "calc", svc.Service.Name)
	require.Len(t, svc.Agents, 1)

	agent := svc.Agents[0]
	require.Equal(t, "scribe", agent.Name)
	require.Equal(t, "calc.scribe", agent.ID)
	require.Equal(t, "scribe", agent.Slug)
	require.Equal(t, "ScribeAgent", agent.StructName)
	require.Equal(t, "ScribeAgentConfig", agent.ConfigType)
	require.Equal(t, filepath.Join("gen", "calc", "agents", "scribe"), agent.Dir)
	require.Equal(t, filepath.Join("gen", "calc", "agents", "scribe", "specs"), agent.ToolSpecsDir)
	require.Equal(t, "ScribeWorkflow", agent.WorkflowFunc)
	require.Equal(t, "ScribeWorkflowDefinition", agent.WorkflowDefinitionVar)
	require.Equal(t, "calc.scribe.workflow", agent.WorkflowName)
	require.Equal(t, "calc_scribe_workflow", agent.WorkflowQueue)
	require.Equal(t, "ScribeWorkflow", agent.Runtime.Workflow.FuncName)
	require.Equal(t, "ScribeWorkflowDefinition", agent.Runtime.Workflow.DefinitionVar)
	require.Equal(t, "calc.scribe.workflow", agent.Runtime.Workflow.Name)
	require.Equal(t, "calc_scribe_workflow", agent.Runtime.Workflow.Queue)
	require.Len(t, agent.Runtime.Activities, 3)
	require.Equal(t, "calc.scribe.plan", agent.Runtime.Activities[0].Name)
	require.Equal(t, "ScribePlanActivity", agent.Runtime.Activities[0].FuncName)
	require.Equal(t, "ScribePlanActivityDefinition", agent.Runtime.Activities[0].DefinitionVar)
	require.Equal(t, "calc_scribe_workflow", agent.Runtime.Activities[0].Queue)
	require.Equal(t, 3, agent.Runtime.Activities[0].RetryPolicy.MaxAttempts)
	require.Equal(t, time.Second, agent.Runtime.Activities[0].RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, agent.Runtime.Activities[0].RetryPolicy.BackoffCoefficient, 0.001)
	require.Equal(t, 2*time.Minute, agent.Runtime.Activities[0].StartToCloseTimeout)
	require.Equal(t, "calc.scribe.resume", agent.Runtime.Activities[1].Name)
	require.Equal(t, "ScribeResumeActivity", agent.Runtime.Activities[1].FuncName)
	require.Equal(t, "ScribeResumeActivityDefinition", agent.Runtime.Activities[1].DefinitionVar)
	require.Equal(t, "calc_scribe_workflow", agent.Runtime.Activities[1].Queue)
	require.Equal(t, 3, agent.Runtime.Activities[1].RetryPolicy.MaxAttempts)
	require.Equal(t, time.Second, agent.Runtime.Activities[1].RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, agent.Runtime.Activities[1].RetryPolicy.BackoffCoefficient, 0.001)
	require.Equal(t, 2*time.Minute, agent.Runtime.Activities[1].StartToCloseTimeout)
	require.Equal(t, "calc.scribe.executetool", agent.Runtime.Activities[2].Name)
	require.Equal(t, "ScribeExecuteToolActivity", agent.Runtime.Activities[2].FuncName)
	require.Equal(t, "ScribeExecuteToolActivityDefinition", agent.Runtime.Activities[2].DefinitionVar)
	require.Empty(t, agent.Runtime.Activities[2].Queue)
	require.Zero(t, agent.Runtime.Activities[2].StartToCloseTimeout)
	require.Equal(t, 3, agent.Runtime.Activities[2].RetryPolicy.MaxAttempts)
	require.Equal(t, time.Second, agent.Runtime.Activities[2].RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, agent.Runtime.Activities[2].RetryPolicy.BackoffCoefficient, 0.001)
	require.NotNil(t, agent.Runtime.ExecuteTool)
	require.Equal(t, "calc.scribe.executetool", agent.Runtime.ExecuteTool.Name)
	require.NotNil(t, agent.Runtime.PlanActivity)
	require.NotNil(t, agent.Runtime.ResumeActivity)

	require.Equal(t, 5, agent.RunPolicy.Caps.MaxToolCalls)
	require.Equal(t, 2, agent.RunPolicy.Caps.MaxConsecutiveFailedToolCalls)
	require.Equal(t, 45*time.Second, agent.RunPolicy.TimeBudget)
	require.True(t, agent.RunPolicy.InterruptsAllowed)

	require.Len(t, agent.UsedToolsets, 1)
	require.Len(t, agent.ExportedToolsets, 1)
	require.Len(t, agent.Tools, 2)

	consumed := agent.UsedToolsets[0]
	require.Equal(t, "summarize", consumed.Name)
	require.Equal(t, "calc", consumed.ServiceName)
	require.Equal(t, "calc.summarize", consumed.QualifiedName)
	require.Equal(t, "calc_scribe_summarize_tasks", consumed.TaskQueue)
	require.Len(t, consumed.Tools, 1)
	require.Equal(t, "summarize_doc", consumed.Tools[0].Name)
	require.Equal(t, "summarize.summarize_doc", consumed.Tools[0].QualifiedName)

	exported := agent.ExportedToolsets[0]
	require.Equal(t, "docs.export", exported.Name)
	require.Equal(t, filepath.Join("gen", "calc", "agents", "scribe", "agenttools", "docs_export", "helpers.go"),
		filepath.Join(exported.AgentToolsDir, "helpers.go"))
	require.Equal(t, "calc", exported.ServiceName)
	require.Equal(t, "docs.export", exported.QualifiedName)
}

func TestGenerateProducesFiles(t *testing.T) {
	roots := runAgentDesign(t)
	files, err := codegen.Generate("goa.design/goa-ai", roots, nil)
	require.NoError(t, err)
	require.NotEmpty(t, files)

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = filepath.ToSlash(f.Path)
	}

	require.Contains(t, paths, "gen/calc/agents/scribe/agent.go")
	require.Contains(t, paths, "gen/calc/agents/scribe/config.go")
	require.Contains(t, paths, "gen/calc/agents/scribe/registry.go")
	require.Contains(t, paths, "gen/calc/agents/scribe/specs/specs.go")
	require.Contains(t, paths, "gen/calc/agents/scribe/agenttools/docs_export/helpers.go")
}

func TestBuildGeneratorData_AliasedMCPToolsetUsesDefinitionNameForArtifacts(t *testing.T) {
	roots := runAliasedMCPDesign(t)
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", roots)
	require.NoError(t, err)

	var consumerAgent *codegen.AgentData
	for _, svc := range data.Services {
		if svc.Service.Name != alphaServiceName {
			continue
		}
		require.Len(t, svc.Agents, 1)
		consumerAgent = svc.Agents[0]
		break
	}
	require.NotNil(t, consumerAgent)
	require.Len(t, consumerAgent.UsedToolsets, 1)

	used := consumerAgent.UsedToolsets[0]
	require.Equal(t, "calc-remote", used.Name)
	require.Equal(t, "calc-remote", used.QualifiedName)
	require.Equal(t, "calc_remote", used.PathName)
	require.Equal(t, filepath.Join("gen", "calc", "toolsets", "calc_remote"), used.SpecsDir)
	require.Equal(t, "goa.design/goa-ai/calc/toolsets/calc_remote", used.SpecsImportPath)
	require.Len(t, used.Tools, 1)
	require.Equal(t, "add", used.Tools[0].Name)
	require.Equal(t, "calc-remote.add", used.Tools[0].QualifiedName)
}

func TestBuildGeneratorData_AliasedMCPToolsetsUseDistinctConstNames(t *testing.T) {
	roots := runDuplicateAliasedMCPDesign(t)
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", roots)
	require.NoError(t, err)

	var consumerAgent *codegen.AgentData
	for _, svc := range data.Services {
		if svc.Service.Name != alphaServiceName {
			continue
		}
		require.Len(t, svc.Agents, 1)
		consumerAgent = svc.Agents[0]
		break
	}
	require.NotNil(t, consumerAgent)
	require.Len(t, consumerAgent.MCPToolsets, 2)
	require.NotEqual(t, consumerAgent.MCPToolsets[0].ConstName, consumerAgent.MCPToolsets[1].ConstName)
}

func TestBuildGeneratorData_MCPToolsetConstNameDoesNotRepeatSuiteName(t *testing.T) {
	roots := runDirectMCPUseDesign(t)
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", roots)
	require.NoError(t, err)

	var consumerAgent *codegen.AgentData
	for _, svc := range data.Services {
		if svc.Service.Name != alphaServiceName {
			continue
		}
		require.Len(t, svc.Agents, 1)
		consumerAgent = svc.Agents[0]
		break
	}
	require.NotNil(t, consumerAgent)
	require.Len(t, consumerAgent.MCPToolsets, 1)
	require.Equal(t, "ScribeCalcServiceCoreSuiteToolsetID", consumerAgent.MCPToolsets[0].ConstName)
}

func TestBuildGeneratorData_MCPToolsetConstNamesStayDistinctAcrossProviderPartitions(t *testing.T) {
	roots := runPartitionedMCPConstCollisionDesign(t)
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", roots)
	require.NoError(t, err)

	var consumerAgent *codegen.AgentData
	for _, svc := range data.Services {
		if svc.Service.Name != alphaServiceName {
			continue
		}
		require.Len(t, svc.Agents, 1)
		consumerAgent = svc.Agents[0]
		break
	}
	require.NotNil(t, consumerAgent)
	require.Len(t, consumerAgent.MCPToolsets, 2)
	require.NotEqual(t, consumerAgent.MCPToolsets[0].ConstName, consumerAgent.MCPToolsets[1].ConstName)
}

func runAgentDesign(t *testing.T) []eval.Root {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	require.NoError(t, eval.Register(mcpexpr.Root))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	design := func() {
		API("calc", func() {})
		var SummarizeArgs = Type("SummarizeArgs", func() {
			Attribute("doc_id", String, "Document identifier")
			Required("doc_id")
		})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("summarize", func() {
					Tool("summarize_doc", "Summarize a document", func() {
						Args(SummarizeArgs)
					})
				})
				Export("docs.export", func() {
					Tool("draft_reply", "Draft a reply", func() {})
				})
				RunPolicy(func() {
					DefaultCaps(
						MaxToolCalls(5),
						MaxConsecutiveFailedToolCalls(2),
					)
					TimeBudget("45s")
					InterruptsAllowed(true)
				})
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentsExpr.Root}
}

func runAliasedMCPDesign(t *testing.T) []eval.Root {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	require.NoError(t, eval.Register(mcpexpr.Root))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	design := func() {
		API("calc", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
		})

		var CalcRemote = Toolset("calc-remote", FromMCP("calc", "core"))
		Service(alphaServiceName, func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcRemote)
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentsExpr.Root}
}

func runDuplicateAliasedMCPDesign(t *testing.T) []eval.Root {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	require.NoError(t, eval.Register(mcpexpr.Root))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	design := func() {
		API("calc", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
		})

		var CalcRemotePrimary = Toolset("calc-remote-primary", FromMCP("calc", "core"))
		var CalcRemoteSecondary = Toolset("calc-remote-secondary", FromMCP("calc", "core"))
		Service(alphaServiceName, func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcRemotePrimary)
				Use(CalcRemoteSecondary)
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentsExpr.Root}
}

func runDirectMCPUseDesign(t *testing.T) []eval.Root {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	require.NoError(t, eval.Register(mcpexpr.Root))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	design := func() {
		API("calc", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
		})

		var Core = Toolset("core", FromMCP("calc", "core"))
		Service(alphaServiceName, func() {
			Agent("scribe", "Doc helper", func() {
				Use(Core)
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentsExpr.Root}
}

func runPartitionedMCPConstCollisionDesign(t *testing.T) []eval.Root {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	require.NoError(t, eval.Register(mcpexpr.Root))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	design := func() {
		API("calc", func() {})
		Service("calc-core", func() {
			MCP("remote", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
		})
		Service("calc", func() {
			MCP("core", "1.0.0")
			Method("multiply", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("multiply", "Multiply two numbers")
			})
		})

		var CalcCoreAPI = Toolset("api", FromMCP("calc-core", "remote"))
		var CalcRemoteAPI = Toolset("remote-api", FromMCP("calc", "core"))
		Service(alphaServiceName, func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcCoreAPI)
				Use(CalcRemoteAPI)
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentsExpr.Root}
}
