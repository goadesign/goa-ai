package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestToolsetExpr_EvalName(t *testing.T) {
	ts := &ToolsetExpr{Name: "my-toolset"}
	require.Equal(t, `toolset "my-toolset"`, ts.EvalName())
}

func TestToolsetExpr_Validate_ProviderMCP(t *testing.T) {
	// Set up Goa root with a service for MCP provider validation
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.Root.Services = []*goaexpr.ServiceExpr{
		{Name: "existing-service"},
	}

	t.Run("valid MCP provider", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "mcp-tools",
			Provider: &ProviderExpr{
				Kind:       ProviderMCP,
				MCPService: "existing-service",
				MCPToolset: "mcp-server",
			},
		}
		err := ts.Validate()
		require.NoError(t, err)
	})

	t.Run("MCP provider missing toolset name", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "mcp-tools",
			Provider: &ProviderExpr{
				Kind:       ProviderMCP,
				MCPService: "existing-service",
				MCPToolset: "",
			},
		}
		err := ts.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "MCP server name is required")
	})

	t.Run("MCP provider with non-existent service", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "mcp-tools",
			Provider: &ProviderExpr{
				Kind:       ProviderMCP,
				MCPService: "non-existent-service",
				MCPToolset: "mcp-server",
			},
		}
		err := ts.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "FromMCP could not resolve service")
	})
}

func TestToolsetExpr_Validate_ProviderRegistry(t *testing.T) {
	t.Run("valid registry provider", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "registry-tools",
			Provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				Registry:    &RegistryExpr{Name: "corp-registry"},
				ToolsetName: "data-tools",
			},
		}
		err := ts.Validate()
		require.NoError(t, err)
	})

	t.Run("registry provider missing registry", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "registry-tools",
			Provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				Registry:    nil,
				ToolsetName: "data-tools",
			},
		}
		err := ts.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "registry is required for FromRegistry provider")
	})

	t.Run("registry provider missing toolset name", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "registry-tools",
			Provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				Registry:    &RegistryExpr{Name: "corp-registry"},
				ToolsetName: "",
			},
		}
		err := ts.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "toolset name is required for FromRegistry provider")
	})
}

func TestToolsetExpr_Validate_ProviderLocal(t *testing.T) {
	t.Run("local provider with nil Provider", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name:     "local-tools",
			Provider: nil,
		}
		err := ts.Validate()
		require.NoError(t, err)
	})

	t.Run("local provider with explicit ProviderLocal kind", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "local-tools",
			Provider: &ProviderExpr{
				Kind: ProviderLocal,
			},
		}
		err := ts.Validate()
		require.NoError(t, err)
	})
}

func TestToolsetExpr_ProviderResolution(t *testing.T) {
	t.Run("toolset without provider is local", func(t *testing.T) {
		ts := &ToolsetExpr{Name: "local"}
		// Provider is nil, which means local toolset with inline schemas
		require.Nil(t, ts.Provider)
	})

	t.Run("toolset with MCP provider", func(t *testing.T) {
		ts := &ToolsetExpr{
			Name: "mcp-backed",
			Provider: &ProviderExpr{
				Kind:       ProviderMCP,
				MCPService: "svc",
				MCPToolset: "mcp-server",
			},
		}
		require.NotNil(t, ts.Provider)
		require.Equal(t, ProviderMCP, ts.Provider.Kind)
		require.Equal(t, "svc", ts.Provider.MCPService)
		require.Equal(t, "mcp-server", ts.Provider.MCPToolset)
	})

	t.Run("toolset with registry provider", func(t *testing.T) {
		reg := &RegistryExpr{Name: "corp-registry", URL: "https://registry.corp.internal"}
		ts := &ToolsetExpr{
			Name: "registry-backed",
			Provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				Registry:    reg,
				ToolsetName: "enterprise-tools",
				Version:     "1.2.3",
			},
		}
		require.NotNil(t, ts.Provider)
		require.Equal(t, ProviderRegistry, ts.Provider.Kind)
		require.Equal(t, reg, ts.Provider.Registry)
		require.Equal(t, "enterprise-tools", ts.Provider.ToolsetName)
		require.Equal(t, "1.2.3", ts.Provider.Version)
	})
}

func TestToolsetExpr_WalkSets(t *testing.T) {
	tool1 := &ToolExpr{Name: "tool1"}
	tool2 := &ToolExpr{Name: "tool2"}
	ts := &ToolsetExpr{
		Name:  "test-toolset",
		Tools: []*ToolExpr{tool1, tool2},
	}

	var walked []eval.Expression
	ts.WalkSets(func(set eval.ExpressionSet) {
		for _, e := range set {
			walked = append(walked, e)
		}
	})

	require.Len(t, walked, 2)
	require.Equal(t, tool1, walked[0])
	require.Equal(t, tool2, walked[1])
}
