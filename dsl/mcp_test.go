// This file verifies how MCP declarations are recorded and rejected through
// the public design language.
package dsl_test

import (
	"errors"
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
			JSONRPC(func() {
				POST("/mcp")
			})
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
			JSONRPC(func() {
				POST("/mcp")
			})
		})
	})

	require.Len(t, mcpexpr.Root.MCPServers, 1)
	mcp := mcpexpr.Root.MCPServers["calculator"]
	require.NotNil(t, mcp)
	require.Equal(t, "2025-06-18", mcp.ProtocolVersion)
}

func TestGoaJSONRPCNotificationRemainsAvailable(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("records", func() {
			JSONRPC(func() {
				POST("/rpc")
			})
			Method("record", func() {
				Payload(func() {
					Attribute("message", String)
					Required("message")
				})
				JSONRPC(func() {
					Notification()
				})
			})
		})
	})

	endpoint := goaexpr.Root.API.JSONRPC.Services[0].HTTPEndpoints[0]
	require.True(t, endpoint.IsJSONRPCNotification())
}

func TestMCPAcceptsOrdinaryHTTPMethodRoutes(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			JSONRPC(func() {
				POST("/mcp")
			})
			Method("add", func() {
				Result(Int)
				Tool("add", "Add numbers")
				HTTP(func() {
					GET("/add")
				})
			})
		})
	})
}

func TestMCPAcceptsOrdinaryHTTPFileServers(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("documents", func() {
			MCP("documents", "1.0.0")
			JSONRPC(func() {
				POST("/mcp")
			})
			Files("/assets/{*path}", "public/assets")
		})
	})
}

func TestMCPAcceptsServiceHTTPSettingsWithoutRoutes(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			JSONRPC(func() {
				POST("/mcp")
			})
			HTTP(func() {
				Path("/calculator")
			})
		})
	})
}

func TestMCPAcceptsJSONRPCAndGRPC(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			JSONRPC(func() {
				POST("/mcp")
			})
			Method("status", func() {
				Result(String)
				Tool("status", "Read status")
				JSONRPC(func() {})
				GRPC(func() {})
			})
		})
	})
}

func TestMCPRequiresServiceJSONRPCPost(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			Method("status", func() {
				Result(String)
				Tool("status", "Read status")
			})
		})
	})

	require.ErrorContains(t, err, `service "calculator" must declare JSONRPC(func(){ POST(...) }) with a service-level path`)
}

func TestMCPRejectsServiceJSONRPCGet(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			JSONRPC(func() {
				GET("/mcp")
			})
			Method("status", func() {
				Result(String)
				Tool("status", "Read status")
			})
		})
	})

	require.ErrorContains(t, err, `service "calculator" must declare JSONRPC(func(){ POST(...) }) with a service-level path; found GET "/mcp"`)
}

func TestMCPResource(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("docs", func() {
			MCP("docs-server", "1.0")
			JSONRPC(func() {
				POST("/mcp")
			})
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
}

func TestMCPStaticPrompt(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("assistant", func() {
			MCP("assistant", "1.0")
			JSONRPC(func() {
				POST("/mcp")
			})
			StaticPrompt("greeting", "Friendly greeting",
				"user", "You are a helpful assistant",
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
	require.Equal(t, "user", prompt.Messages[0].Role)
	require.Equal(t, "You are a helpful assistant", prompt.Messages[0].Content)
	require.Equal(t, "user", prompt.Messages[1].Role)
	require.Equal(t, "Hello!", prompt.Messages[1].Content)
}

func TestMCPStaticPromptRejectsOddMessageList(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("assistant", func() {
			MCP("assistant", "1.0")
			StaticPrompt("greeting", "Friendly greeting",
				"user", "You are a helpful assistant",
				"user")
		})
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "StaticPrompt requires role/content pairs")
}

func TestMCPToolInMethod(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("calculator", func() {
			MCP("calc", "1.0.0")
			JSONRPC(func() {
				POST("/mcp")
			})
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

func runMCPDSLWithError(t *testing.T, dsl func()) error {
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

	if !eval.Execute(dsl, nil) {
		return errors.New(eval.Context.Error())
	}
	return eval.RunDSL()
}
