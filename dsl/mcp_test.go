package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestMCPBasicConfiguration(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["calculator"]
	require.NotNil(t, mcp)
	require.Equal(t, "calc", mcp.Name)
	require.Equal(t, "1.0.0", mcp.Version)
	require.Equal(t, "calculator", mcp.Service.Name)
}

func TestMCPWithProtocolVersion(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["calculator"]
	require.NotNil(t, mcp)
	require.Equal(t, "2025-06-18", mcp.ProtocolVersion)
}

func TestMCPResource(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("docs", func() {
			MCP("docs-server", "1.0")
			Method("readme", func() {
				Result(String)
				Resource("readme", "file:///docs/README.md", "text/markdown")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["docs"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Resources, 1)
	res := mcp.Resources[0]
	require.Equal(t, "readme", res.Name)
	require.Equal(t, "file:///docs/README.md", res.URI)
	require.Equal(t, "text/markdown", res.MimeType)
	require.False(t, res.Watchable)
}

func TestMCPWatchableResource(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("status", func() {
			MCP("status-server", "1.0")
			Method("system_status", func() {
				Result(func() {
					Attribute("status", String)
					Attribute("uptime", Int)
				})
				WatchableResource("status", "status://system", "application/json")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["status"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Resources, 1)
	res := mcp.Resources[0]
	require.Equal(t, "status", res.Name)
	require.Equal(t, "status://system", res.URI)
	require.Equal(t, "application/json", res.MimeType)
	require.True(t, res.Watchable)
}

func TestMCPStaticPrompt(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("assistant", func() {
			MCP("assistant", "1.0")
			StaticPrompt("greeting", "Friendly greeting",
				"system", "You are a helpful assistant",
				"user", "Hello!")
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["assistant"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Prompts, 1)
	prompt := mcp.Prompts[0]
	require.Equal(t, "greeting", prompt.Name)
	require.Equal(t, "Friendly greeting", prompt.Description)
	require.Len(t, prompt.Messages, 2)
	require.Equal(t, "system", prompt.Messages[0].Role)
	require.Equal(t, "You are a helpful assistant", prompt.Messages[0].Content)
	require.Equal(t, "user", prompt.Messages[1].Role)
	require.Equal(t, "Hello!", prompt.Messages[1].Content)
}

func TestMCPDynamicPrompt(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("assistant", func() {
			MCP("assistant", "1.0")
			Method("code_review", func() {
				Payload(func() {
					Attribute("language", String)
					Attribute("code", String)
				})
				Result(ArrayOf(String))
				DynamicPrompt("code_review", "Generate code review prompt")
			})
		})
	})

	require.Len(t, mcpexpr.Root.DynamicPrompts, 1)
	prompts := mcpexpr.Root.DynamicPrompts["assistant"]
	require.Len(t, prompts, 1)
	prompt := prompts[0]
	require.Equal(t, "code_review", prompt.Name)
	require.Equal(t, "Generate code review prompt", prompt.Description)
	require.NotNil(t, prompt.Method)
	require.Equal(t, "code_review", prompt.Method.Name)
}

func TestMCPNotification(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("tasks", func() {
			MCP("tasks-server", "1.0")
			Method("progress_update", func() {
				Payload(func() {
					Attribute("task_id", String)
					Attribute("progress", Int)
				})
				Notification("progress", "Task progress notification")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["tasks"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Notifications, 1)
	notif := mcp.Notifications[0]
	require.Equal(t, "progress", notif.Name)
	require.Equal(t, "Task progress notification", notif.Description)
	require.NotNil(t, notif.Method)
}

func TestMCPSubscription(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("status", func() {
			MCP("status-server", "1.0")
			Method("system_status", func() {
				Result(String)
				WatchableResource("status", "status://system", "application/json")
			})
			Method("subscribe_status", func() {
				Payload(func() {
					Attribute("uri", String)
				})
				Result(String)
				Subscription("status")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["status"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Subscriptions, 1)
	sub := mcp.Subscriptions[0]
	require.Equal(t, "status", sub.ResourceName)
	require.NotNil(t, sub.Method)
}

func TestMCPSubscriptionMonitor(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("status", func() {
			MCP("status-server", "1.0")
			Method("watch_subscriptions", func() {
				StreamingResult(func() {
					Attribute("resource", String)
					Attribute("event", String)
				})
				SubscriptionMonitor("subscriptions")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["status"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.SubscriptionMonitors, 1)
	monitor := mcp.SubscriptionMonitors[0]
	require.Equal(t, "subscriptions", monitor.Name)
	require.NotNil(t, monitor.Method)
}

func TestMCPToolInMethod(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int)
					Attribute("b", Int)
				})
				Result(func() {
					Attribute("sum", Int)
				})
				Tool("add", "Add two numbers")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["calculator"]
	require.NotNil(t, mcp)
	require.Len(t, mcp.Tools, 1)
	tool := mcp.Tools[0]
	require.Equal(t, "add", tool.Name)
	require.Equal(t, "Add two numbers", tool.Description)
	require.NotNil(t, tool.Method)
}

func runMCPDSL(t *testing.T, dsl func()) {
	t.Helper()

	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)

	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	mcpexpr.Root = mcpexpr.NewRoot()
	require.NoError(t, eval.Register(mcpexpr.Root))

	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}

	require.True(t, eval.Execute(dsl, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
}
