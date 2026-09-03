// Package codegen_test checks that agent generation uses the design and owners saved by
// the current generation command.
package codegen_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goadsl "goa.design/goa/v3/dsl"
)

func TestRegistryClientsUsePassedAgentRoot(t *testing.T) {
	tests := []struct {
		name       string
		globalRoot func() *agentexpr.RootExpr
	}{
		{name: "nil global root"},
		{
			name: "different global root",
			globalRoot: func() *agentexpr.RootExpr {
				return &agentexpr.RootExpr{Registries: []*agentexpr.RegistryExpr{
					{Name: "other", URL: "https://other.example"},
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			genpkg, roots := testhelpers.RunDesign(t, registryClientIsolationDesign())
			exactRoot := agentexpr.Root
			if test.globalRoot == nil {
				agentexpr.Root = nil
			} else {
				agentexpr.Root = test.globalRoot()
			}
			t.Cleanup(func() { agentexpr.Root = exactRoot })

			files, err := codegen.BuildFilesForTest(genpkg, roots, false)
			require.NoError(t, err)
			require.NotEmpty(t, testhelpers.FileContent(
				t,
				files,
				"gen/alpha/registry/exact/client.go",
			))
			for _, file := range files {
				require.NotEqual(t, "gen/alpha/registry/other/client.go", file.Path)
			}
		})
	}
}

// TestMCPToolsUseTheRootReturnedByRunDesign checks that the shared test runner
// passes the MCP definitions from the design it just evaluated.
func TestMCPToolsUseTheRootReturnedByRunDesign(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, testscenarios.MCPUse())
	exactRoot := mcpexpr.Root
	mcpexpr.Root = nil
	t.Cleanup(func() { mcpexpr.Root = exactRoot })

	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	require.NotEmpty(t, testhelpers.FileContent(
		t,
		files,
		"gen/calc/toolsets/core/specs.go",
	))
}

func TestRegistryClientResolvesGeneratedNameCollision(t *testing.T) {
	design := func() {
		goadsl.API("registry_collision", func() {})
		scheme := goadsl.APIKeySecurity("auth", func() {})
		registry := Registry("exact", func() {
			goadsl.URL("https://exact.example")
			goadsl.Security(scheme)
		})
		tools := Toolset(FromRegistry(registry, "tools"))
		goadsl.Service("alpha", func() {
			Agent("worker", "Worker", func() {
				Use(tools)
			})
		})
	}
	genpkg, roots := testhelpers.RunDesign(t, design)
	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	options := testhelpers.FileContent(t, files, "gen/alpha/registry/exact/options.go")
	require.Contains(t, options, "func WithAuth2(key string) Option")
}

func TestSharedToolSpecsKeepExportIdentityOnReferences(t *testing.T) {
	design := func() {
		goadsl.API("tool_owner", func() {})
		var PingInput = goadsl.Type("PingInput", func() {
			goadsl.Attribute("message", goadsl.String, "Message to ping")
			goadsl.Required("message")
		})
		shared := Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(PingInput)
				Return(goadsl.String)
			})
		})
		goadsl.Service("alpha", func() {
			Agent("consumer", "Consumer", func() {
				Use(shared)
			})
		})
		goadsl.Service("bravo", func() {
			Agent("provider", "Provider", func() {
				Export(shared)
			})
		})
	}
	genpkg, roots := testhelpers.RunDesign(t, design)
	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	specs := testhelpers.FileContent(
		t,
		files,
		"gen/bravo/agents/provider/exports/shared/specs.go",
	)
	require.NotContains(t, specs, "IsAgentTool")
	require.NotContains(t, specs, "bravo.provider")
	aggregate := testhelpers.FileContent(t, files, "gen/alpha/agents/consumer/specs/specs.go")
	require.Contains(t, aggregate, "spec.IsAgentTool = true")
	require.Contains(t, aggregate, `spec.AgentID = "bravo.provider"`)
}

// TestCollidingServiceNamesUseGoaPlannedPaths verifies every generated agent
// path follows the service directory chosen by Goa after it resolves a name
// collision.
func TestCollidingServiceNamesUseGoaPlannedPaths(t *testing.T) {
	design := func() {
		goadsl.API("path_collision", func() {})
		goadsl.Service("read_value", func() {
			Agent("underscore", "Underscore service agent", func() {
				Use("underscore_tools", func() {
					Tool("read", "Read a value", func() {})
				})
				Export("underscore_export", func() {
					Tool("write", "Write a value", func() {})
				})
			})
		})
		goadsl.Service("read-value", func() {
			Agent("dash", "Dash service agent", func() {
				Use("dash_tools", func() {
					Tool("read", "Read a value", func() {})
				})
				Export("dash_export", func() {
					Tool("write", "Write a value", func() {})
				})
			})
		})
	}
	genpkg, roots := testhelpers.RunDesign(t, design)
	data, err := codegen.BuildDataForTest(genpkg, roots)
	require.NoError(t, err)
	require.Len(t, data.Services, 2)

	services := make(map[string]*codegen.AgentData, len(data.Services))
	for _, service := range data.Services {
		require.Len(t, service.Agents, 1)
		services[service.Service.Name] = service.Agents[0]
	}
	assertAgentPaths(t, services["read-value"], "read_value", "dash_tools")
	assertAgentPaths(t, services["read_value"], "read_value2", "underscore_tools")
}

// assertAgentPaths checks the directories and imports generated beneath one
// Goa service directory.
func assertAgentPaths(t *testing.T, agent *codegen.AgentData, servicePath, toolsetPath string) {
	t.Helper()
	require.NotNil(t, agent)
	require.Equal(t, servicePath, agent.Service.PathName)
	require.Equal(t, "gen/"+servicePath+"/agents/"+agent.PathName, agent.Dir)
	require.Equal(t, "goa.design/goa-ai/gen/"+servicePath+"/agents/"+agent.PathName, agent.ImportPath)
	require.Equal(t, agent.ImportPath+"/specs", agent.ToolSpecsImportPath)
	require.Equal(t, agent.Dir+"/specs", agent.ToolSpecsDir)
	require.Len(t, agent.UsedToolsets, 1)
	toolset := agent.UsedToolsets[0]
	require.Equal(t, agent.ImportPath+"/"+toolsetPath, toolset.PackageImportPath)
	require.Equal(t, agent.Dir+"/"+toolsetPath, toolset.Dir)
	require.Equal(t, "goa.design/goa-ai/gen/"+servicePath+"/toolsets/"+toolsetPath, toolset.SpecsImportPath)
	require.Equal(t, "gen/"+servicePath+"/toolsets/"+toolsetPath, toolset.SpecsDir)
	require.Len(t, agent.ExportedToolsets, 1)
	exported := agent.ExportedToolsets[0]
	exportPath := agent.PathName + "_export"
	require.Equal(t, agent.ImportPath+"/"+exportPath, exported.PackageImportPath)
	require.Equal(t, agent.Dir+"/"+exportPath, exported.Dir)
	require.Equal(t, agent.ImportPath+"/exports/"+exportPath, exported.SpecsImportPath)
	require.Equal(t, agent.Dir+"/exports/"+exportPath, exported.SpecsDir)
	require.Equal(t, agent.ImportPath+"/agenttools/"+exportPath, exported.AgentToolsImportPath)
	require.Equal(t, agent.Dir+"/agenttools/"+exportPath, exported.AgentToolsDir)
}

// registryClientIsolationDesign declares one registry client under alpha.
func registryClientIsolationDesign() func() {
	return func() {
		goadsl.API("registry_isolation", func() {})
		registry := Registry("exact", func() {
			goadsl.URL("https://exact.example")
		})
		tools := Toolset(FromRegistry(registry, "tools"))
		goadsl.Service("alpha", func() {
			Agent("worker", "Worker", func() {
				Use(tools)
			})
		})
	}
}
