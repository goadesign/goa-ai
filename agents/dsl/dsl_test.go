package dsl_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentsdsl "goa.design/goa-ai/agents/dsl"
	agentsExpr "goa.design/goa-ai/agents/expr"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
	. "goa.design/goa/v3/dsl"
)

func TestAgentDSLExample(t *testing.T) {
	runDSL(t, func() {
		API("example", func() {})
		Service("docs", func() {
			agentsdsl.Agent("docs-agent", "Agent for managing documentation workflows", func() {
				agentsdsl.Uses(func() {
					agentsdsl.Toolset("summarization-tools", func() {
						agentsdsl.Tool("document-summarizer", "Summarize documents", func() {})
					})
				})
				agentsdsl.Exports(func() {
					agentsdsl.Toolset("text-processing-suite", func() {
						agentsdsl.Tool("doc-abstractor", "Create document abstracts", func() {})
					})
				})
				agentsdsl.RunPolicy(func() {
					agentsdsl.DefaultCaps(
						agentsdsl.MaxToolCalls(5),
						agentsdsl.MaxConsecutiveFailedToolCalls(2),
					)
					agentsdsl.TimeBudget("30s")
					agentsdsl.InterruptsAllowed(true)
				})
			})
		})
	})

	require.Len(t, agentsExpr.Root.Agents, 1)
	agent := agentsExpr.Root.Agents[0]
	require.Equal(t, "docs-agent", agent.Name)
	require.Equal(t, "docs", agent.Service.Name)
	require.NotNil(t, agent.RunPolicy)
	require.NotNil(t, agent.Used)
	require.NotNil(t, agent.Exported)
}

func TestGlobalToolsetRegisters(t *testing.T) {
	runDSL(t, func() {
		agentsdsl.Toolset("global-tools", func() {
			agentsdsl.Tool("summarize", "Summarize text", func() {})
		})
	})

	require.Len(t, agentsExpr.Root.Toolsets, 1)
	ts := agentsExpr.Root.Toolsets[0]
	require.Equal(t, "global-tools", ts.Name)
	require.Len(t, ts.Tools, 1)
	require.Equal(t, "summarize", ts.Tools[0].Name)
}

func TestRunPolicyDefaults(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("tasks", func() {
			agentsdsl.Agent("planner", "Planner agent", func() {
				agentsdsl.RunPolicy(func() {
					agentsdsl.DefaultCaps(agentsdsl.MaxToolCalls(3))
					agentsdsl.TimeBudget("45s")
				})
			})
		})
	})

	require.Len(t, agentsExpr.Root.Agents, 1)
	policy := agentsExpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.NotNil(t, policy.DefaultCaps)
	require.Equal(t, 3, policy.DefaultCaps.MaxToolCalls)
	require.Equal(t, 45*time.Second, policy.TimeBudget)
}

func TestToolsetReferenceReuse(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		shared := agentsdsl.Toolset("shared-tools", func() {
			agentsdsl.Tool("ping", "Ping helper", func() {})
		})
		Service("ops", func() {
			agentsdsl.Agent("watcher", "Watches", func() {
				agentsdsl.Uses(func() {
					agentsdsl.Toolset(shared)
				})
			})
		})
	})

	require.Len(t, agentsExpr.Root.Agents, 1)
	agent := agentsExpr.Root.Agents[0]
	require.NotNil(t, agent.Used)
	require.Len(t, agent.Used.Toolsets, 1)
	require.Equal(t, "shared-tools", agent.Used.Toolsets[0].Name)
}

func runDSL(t *testing.T, dsl func()) {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	require.True(t, eval.Execute(dsl, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
}
