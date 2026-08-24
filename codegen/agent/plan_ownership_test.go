// Package codegen_test checks that agent generation uses the design and owners saved by
// the current generation command.
package codegen_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	aidsl "goa.design/goa-ai/dsl"
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
		registry := aidsl.Registry("exact", func() {
			goadsl.URL("https://exact.example")
			goadsl.Security(scheme)
		})
		tools := aidsl.Toolset(aidsl.FromRegistry(registry, "tools"))
		goadsl.Service("alpha", func() {
			aidsl.Agent("worker", "Worker", func() {
				aidsl.Use(tools)
			})
		})
	}
	genpkg, roots := testhelpers.RunDesign(t, design)
	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	options := testhelpers.FileContent(t, files, "gen/alpha/registry/exact/options.go")
	require.Contains(t, options, "func WithAuth2(key string) Option")
}

func TestSharedToolSpecsUseExportOwner(t *testing.T) {
	design := func() {
		goadsl.API("tool_owner", func() {})
		shared := aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Ping", func() {
				aidsl.Args(goadsl.String)
				aidsl.Return(goadsl.String)
			})
		})
		goadsl.Service("alpha", func() {
			aidsl.Agent("consumer", "Consumer", func() {
				aidsl.Use(shared)
			})
		})
		goadsl.Service("bravo", func() {
			aidsl.Agent("provider", "Provider", func() {
				aidsl.Export(shared)
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
	require.Contains(t, specs, "IsAgentTool: true")
	require.Contains(t, specs, `AgentID:     "bravo.provider"`)
}

// registryClientIsolationDesign declares one registry client under alpha.
func registryClientIsolationDesign() func() {
	return func() {
		goadsl.API("registry_isolation", func() {})
		registry := aidsl.Registry("exact", func() {
			goadsl.URL("https://exact.example")
		})
		tools := aidsl.Toolset(aidsl.FromRegistry(registry, "tools"))
		goadsl.Service("alpha", func() {
			aidsl.Agent("worker", "Worker", func() {
				aidsl.Use(tools)
			})
		})
	}
}
