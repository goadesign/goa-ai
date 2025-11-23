package dsl_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestAgentDSLExample(t *testing.T) {
	runDSL(t, func() {
		API("example", func() {})
		Service("docs", func() {
			Agent("docs-agent", "Agent for managing documentation workflows", func() {
				Use("summarization-tools", func() {
					Tool("document-summarizer", "Summarize documents", func() {})
				})
				Export("text-processing-suite", func() {
					Tool("doc-abstractor", "Create document abstracts", func() {})
				})
				RunPolicy(func() {
					DefaultCaps(
						MaxToolCalls(5),
						MaxConsecutiveFailedToolCalls(2),
					)
					TimeBudget("30s")
					InterruptsAllowed(true)
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.Equal(t, "docs-agent", agent.Name)
	require.Equal(t, "docs", agent.Service.Name)
	require.NotNil(t, agent.RunPolicy)
	require.NotNil(t, agent.Used)
	require.NotNil(t, agent.Exported)
}

func TestGlobalToolsetRegisters(t *testing.T) {
	runDSL(t, func() {
		Toolset("global-tools", func() {
			Tool("summarize", "Summarize text", func() {})
		})
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.Equal(t, "global-tools", ts.Name)
	require.Len(t, ts.Tools, 1)
	require.Equal(t, "summarize", ts.Tools[0].Name)
}

func TestRunPolicyDefaults(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("tasks", func() {
			Agent("planner", "Planner agent", func() {
				RunPolicy(func() {
					DefaultCaps(MaxToolCalls(3))
					TimeBudget("45s")
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.NotNil(t, policy.DefaultCaps)
	require.Equal(t, 3, policy.DefaultCaps.MaxToolCalls)
	require.Equal(t, 45*time.Second, policy.TimeBudget)
}

func TestToolsetReferenceReuse(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		shared := Toolset("shared-tools", func() {
			Tool("ping", "Ping helper", func() {})
		})
		Service("ops", func() {
			Agent("watcher", "Watches", func() {
				Use(shared)
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.NotNil(t, agent.Used)
	require.Len(t, agent.Used.Toolsets, 1)
	require.Equal(t, "shared-tools", agent.Used.Toolsets[0].Name)
}

func TestBindToSelfServiceMethod(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Method("GetX", func() {
				Payload(String)
				Result(String)
			})
			Agent("agent", "desc", func() {
				Use("ts", func() {
					Tool("tool", "t", func() {
						BindTo("GetX")
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	require.NotNil(t, a.Used)
	require.Len(t, a.Used.Toolsets, 1)
	ts := a.Used.Toolsets[0]
	require.Len(t, ts.Tools, 1)
	tool := ts.Tools[0]
	require.NotNil(t, tool.Method, "BindTo should resolve to MethodExpr")
	require.Equal(t, "GetX", tool.Method.Name)
}

func TestBindToCrossServiceMethod(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svcA", func() {
			Agent("agent", "desc", func() {
				Use("ts", func() {
					Tool("tool", "t", func() {
						BindTo("svcB", "GetY")
					})
				})
			})
		})
		Service("svcB", func() {
			Method("GetY", func() {
				Payload(String)
				Result(String)
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	ts := a.Used.Toolsets[0]
	tool := ts.Tools[0]
	require.NotNil(t, tool.Method)
	require.Equal(t, "GetY", tool.Method.Name)
	require.Equal(t, "svcB", tool.Method.Service.Name)
}

func TestAgentToolsetCrossServiceReference(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		// Service A exports a toolset
		Service("svcA", func() {
			Agent("agentA", "desc", func() {
				Export("exported", func() {
					Tool("t1", "tool one", func() {})
				})
			})
		})
		// Service B consumes it via AgentToolset
		Service("svcB", func() {
			Agent("agentB", "desc", func() {
				Use(AgentToolset("svcA", "agentA", "exported"))
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 2)
	// Find consumer agent (svcB.agentB)
	var consumer *agentsexpr.AgentExpr
	for _, a := range agentsexpr.Root.Agents {
		if a.Service != nil && a.Service.Name == "svcB" && a.Name == "agentB" {
			consumer = a
			break
		}
	}
	require.NotNil(t, consumer)
	require.NotNil(t, consumer.Used)
	require.Len(t, consumer.Used.Toolsets, 1)
	ts := consumer.Used.Toolsets[0]
	require.NotNil(t, ts.Origin, "AgentToolset should preserve origin")
	// Origin should point to the exported toolset on svcA.agentA.
	var provider *agentsexpr.AgentExpr
	for _, a := range agentsexpr.Root.Agents {
		if a.Service != nil && a.Service.Name == "svcA" && a.Name == "agentA" {
			provider = a
			break
		}
	}
	require.NotNil(t, provider)
	require.NotNil(t, provider.Exported)
	require.Len(t, provider.Exported.Toolsets, 1)
	exported := provider.Exported.Toolsets[0]
	require.Equal(t, exported, ts.Origin)
}

func TestProviderInference_LocalAndMCP(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		var SearchSuite = MCPToolset("svc", "search")
		Service("svc", func() {
			Agent("a", "desc", func() {
				Use("local", func() { Tool("x", "", func() {}) })
				Use(SearchSuite)
			})
		})
	})
	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	require.Len(t, a.Used.Toolsets, 2)
	// Order matches declaration: local then MCP.
	local := a.Used.Toolsets[0]
	mcp := a.Used.Toolsets[1]
	require.False(t, local.External)
	require.True(t, mcp.External)
	require.Equal(t, "svc", mcp.MCPService)
	require.Equal(t, "search", mcp.MCPToolset)
}

func runDSL(t *testing.T, dsl func()) {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsexpr.Root = &agentsexpr.RootExpr{}
	require.NoError(t, eval.Register(agentsexpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	require.True(t, eval.Execute(dsl, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
}
