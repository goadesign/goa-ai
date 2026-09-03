package dsl_test

import (
	"errors"
	"fmt"
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
						MaxRecoveryTurns(2),
					)
					TimeBudget("30s")
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
		Service("workflow", func() {
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

func TestDefaultCapsRequiresPositiveMaxToolCalls(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		Service("tasks", func() {
			Agent("planner", "Planner agent", func() {
				RunPolicy(func() {
					DefaultCaps(MaxToolCalls(0))
				})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "MaxToolCalls requires n > 0")
}

func TestDefaultCapsAcceptsIndependentLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		caps func()
	}{
		{
			name: "recovery turns only",
			caps: func() {
				DefaultCaps(MaxRecoveryTurns(2))
			},
		},
		{
			name: "no overrides",
			caps: func() {
				DefaultCaps()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runDSL(t, func() {
				API("test", func() {})
				Service("tasks", func() {
					Agent("planner", "Planner agent", func() {
						RunPolicy(test.caps)
					})
				})
			})
		})
	}
}

func TestMaxRecoveryTurnsRequiresPositiveValue(t *testing.T) {
	for _, value := range []int{-1, 0} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			err := runDSLWithError(t, func() {
				API("test", func() {})
				Service("tasks", func() {
					Agent("planner", "Planner agent", func() {
						RunPolicy(func() {
							DefaultCaps(MaxRecoveryTurns(value))
						})
					})
				})
			})

			require.ErrorContains(t, err, "MaxRecoveryTurns requires n > 0")
		})
	}
}

func TestTerminalRunImpliesBookkeeping(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("tasks", func() {
			Agent("planner", "Planner agent", func() {
				Use("workflow.progress", func() {
					Tool("complete", "Complete workflow", func() {
						TerminalRun()
					})
				})
			})
		})
	})

	tool := agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0]
	require.True(t, tool.TerminalRun)
	require.True(t, tool.Bookkeeping)
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
				Payload(func() {
					Attribute("value", String)
				})
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
				Payload(func() {
					Attribute("value", String)
				})
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
		var SearchSuite = Toolset(FromExternalMCP("svc", "search"), func() {
			Tool("search", "", func() {})
		})
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
	// Local toolset has no Provider (or Provider.Kind != ProviderMCP)
	require.True(t, local.Provider == nil || local.Provider.Kind != agentsexpr.ProviderMCP)
	// MCP toolset has Provider with ProviderMCP kind
	require.NotNil(t, mcp.Provider)
	require.Equal(t, agentsexpr.ProviderMCP, mcp.Provider.Kind)
	require.Equal(t, "svc", mcp.Provider.MCPService)
	require.Equal(t, "search", mcp.Provider.MCPToolset)
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

func runDSLWithError(t *testing.T, dsl func()) error {
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

	if !eval.Execute(dsl, nil) {
		return errors.New(eval.Context.Error())
	}
	return eval.RunDSL()
}

func TestToolRejectsDuplicateDSLFunctions(t *testing.T) {
	err := runDSLWithError(t, func() {
		Toolset("local", func() {
			Tool("search", func() {}, func() {})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "Tool accepts at most one DSL function")
}

func TestToolArgsRequireObjectShape(t *testing.T) {
	tests := []struct {
		name string
		args func() any
	}{
		{name: "primitive", args: func() any { return String }},
		{name: "array", args: func() any { return ArrayOf(String) }},
		{name: "map", args: func() any { return MapOf(String, String) }},
		{name: "primitive alias", args: func() any { return Type("Message", String) }},
		{name: "array alias", args: func() any { return Type("Messages", ArrayOf(String)) }},
		{name: "map alias", args: func() any { return Type("Labels", MapOf(String, String)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				args := test.args()
				Service("assistant", func() {
					Agent("planner", "Plans work", func() {
						Use("local", func() {
							Tool("echo", "Echo a value", func() {
								Args(args)
							})
						})
					})
				})
			})

			require.ErrorContains(t, err, "Args must define an object")
		})
	}
}

func TestBoundToolArgsRequireObjectShapedMethodPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload func() any
	}{
		{name: "primitive", payload: func() any { return String }},
		{name: "array", payload: func() any { return ArrayOf(String) }},
		{name: "map", payload: func() any { return MapOf(String, String) }},
		{name: "primitive alias", payload: func() any { return Type("BoundMessage", String) }},
		{name: "array alias", payload: func() any { return Type("BoundMessages", ArrayOf(String)) }},
		{name: "map alias", payload: func() any { return Type("BoundLabels", MapOf(String, String)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				payload := test.payload()
				Service("assistant", func() {
					Method("Echo", func() {
						Payload(payload)
					})
					Agent("planner", "Plans work", func() {
						Use("local", func() {
							Tool("echo", "Echo a value", func() {
								BindTo("Echo")
							})
						})
					})
				})
			})

			require.ErrorContains(t, err, "Args must define an object")
		})
	}
}

func TestToolRejectsInvalidArgumentType(t *testing.T) {
	err := runDSLWithError(t, func() {
		Toolset("local", func() {
			Tool("search", 42)
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "optional description string or DSL function")
}

func TestUseRejectsMultipleDSLFunctions(t *testing.T) {
	err := runDSLWithError(t, func() {
		Service("assistant", func() {
			Agent("planner", "planner", func() {
				Use("adhoc", func() {}, func() {})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "Use accepts at most one DSL function")
}

func TestExportRejectsMultipleDSLFunctions(t *testing.T) {
	err := runDSLWithError(t, func() {
		Service("assistant", func() {
			Agent("planner", "planner", func() {
				Export("adhoc", func() {}, func() {})
			})
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "Export accepts at most one DSL function")
}

// TestPassthroughWithServiceAndMethodNames verifies Passthrough works with
// service name and method name strings.
func TestPassthroughWithServiceAndMethodNames(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		var LogPayload = Type("LogPayload", func() {
			Attribute("message", String, "Message to log")
			Required("message")
		})
		Service("logging", func() {
			Method("LogMessage", func() {
				Payload(LogPayload)
				Result(String)
			})
			Agent("agent", "desc", func() {
				Export("logging-tools", func() {
					Tool("log_message", "Log a message", func() {
						Args(LogPayload)
						Return(String)
						Passthrough("log_message", "logging", "LogMessage")
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	require.NotNil(t, a.Exported)
	require.Len(t, a.Exported.Toolsets, 1)
	ts := a.Exported.Toolsets[0]
	require.Len(t, ts.Tools, 1)
	tool := ts.Tools[0]
	require.NotNil(t, tool.ExportPassthrough, "Passthrough should set ExportPassthrough")
	require.Equal(t, "logging", tool.ExportPassthrough.TargetService)
	require.Equal(t, "LogMessage", tool.ExportPassthrough.TargetMethod)
}

// TestPassthroughWithMethodExpr verifies Passthrough works with a MethodExpr reference.
func TestPassthroughWithMethodExpr(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		var LogPayload = Type("LogPayload", func() {
			Attribute("message", String, "Message to log")
			Required("message")
		})
		Service("logging", func() {
			var logMethod *goaexpr.MethodExpr
			Method("LogMessage", func() {
				Payload(LogPayload)
				Result(String)
			})
			// Get the method expression after it's created
			logMethod = goaexpr.Root.Service("logging").Method("LogMessage")
			Agent("agent", "desc", func() {
				Export("logging-tools", func() {
					Tool("log_message", "Log a message", func() {
						Args(LogPayload)
						Return(String)
						Passthrough("log_message", logMethod)
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	a := agentsexpr.Root.Agents[0]
	require.NotNil(t, a.Exported)
	require.Len(t, a.Exported.Toolsets, 1)
	ts := a.Exported.Toolsets[0]
	require.Len(t, ts.Tools, 1)
	tool := ts.Tools[0]
	require.NotNil(t, tool.ExportPassthrough, "Passthrough should set ExportPassthrough")
	require.Equal(t, "logging", tool.ExportPassthrough.TargetService)
	require.Equal(t, "LogMessage", tool.ExportPassthrough.TargetMethod)
}

// TestTimingConfiguration verifies Timing DSL with Budget, Plan, and Tools.
func TestTimingConfiguration(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				RunPolicy(func() {
					Timing(func() {
						Budget("10m")
						Plan("45s")
						Tools("2m")
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.Equal(t, 10*time.Minute, policy.TimeBudget)
	require.Equal(t, 45*time.Second, policy.PlanTimeout)
	require.Equal(t, 2*time.Minute, policy.ToolTimeout)
}

// TestHistoryKeepRecentTurns verifies History with KeepRecentTurns.
func TestHistoryKeepRecentTurns(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				RunPolicy(func() {
					History(func() {
						KeepRecentTurns(20)
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.NotNil(t, policy.History)
	require.Equal(t, agentsexpr.HistoryModeKeepRecent, policy.History.Mode)
	require.Equal(t, 20, policy.History.KeepRecent)
}

// TestHistoryCompress verifies History with Compress.
func TestHistoryCompress(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				RunPolicy(func() {
					History(func() {
						CompressAtMaxInputTokens(120000)
						KeepMaxInputTokens(40000)
						KeepMaxTurns(10)
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.NotNil(t, policy.History)
	require.Equal(t, agentsexpr.HistoryModeCompress, policy.History.Mode)
	require.Equal(t, 120000, policy.History.CompressAtMaxInputTokens)
	require.Equal(t, 40000, policy.History.KeepMaxInputTokens)
	require.Equal(t, 10, policy.History.KeepMaxTurns)
}

// TestCacheConfiguration verifies Cache with AfterSystem and AfterTools.
func TestCacheConfiguration(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				RunPolicy(func() {
					Cache(func() {
						AfterSystem()
						AfterTools()
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.NotNil(t, policy.Cache)
	require.True(t, policy.Cache.AfterSystem)
	require.True(t, policy.Cache.AfterTools)
}

// TestOnMissingFields verifies OnMissingFields DSL.
func TestOnMissingFields(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("svc", func() {
			Agent("agent", "desc", func() {
				RunPolicy(func() {
					OnMissingFields("await_clarification")
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	policy := agentsexpr.Root.Agents[0].RunPolicy
	require.NotNil(t, policy)
	require.Equal(t, "await_clarification", policy.OnMissingFields)
}
