package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderKind_String(t *testing.T) {
	tests := []struct {
		kind ProviderKind
		want string
	}{
		{ProviderLocal, "local"},
		{ProviderMCP, "mcp"},
		{ProviderRegistry, "registry"},
		{ProviderKind(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			require.Equal(t, tt.want, tt.kind.String())
		})
	}
}

func TestProviderExpr_EvalName(t *testing.T) {
	tests := []struct {
		name     string
		provider *ProviderExpr
		want     string
	}{
		{
			name:     "local provider",
			provider: &ProviderExpr{Kind: ProviderLocal},
			want:     "local provider",
		},
		{
			name: "MCP provider",
			provider: &ProviderExpr{
				Kind:       ProviderMCP,
				MCPService: "assistant-service",
				MCPToolset: "assistant-mcp",
			},
			want: `MCP provider (service="assistant-service", toolset="assistant-mcp")`,
		},
		{
			name: "registry provider with registry",
			provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				Registry:    &RegistryExpr{Name: "corp-registry"},
				ToolsetName: "data-tools",
			},
			want: `registry provider (registry="corp-registry", toolset="data-tools")`,
		},
		{
			name: "registry provider without registry",
			provider: &ProviderExpr{
				Kind:        ProviderRegistry,
				ToolsetName: "data-tools",
			},
			want: `registry provider (registry="", toolset="data-tools")`,
		},
		{
			name:     "unknown kind defaults to local",
			provider: &ProviderExpr{Kind: ProviderKind(99)},
			want:     "local provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.provider.EvalName())
		})
	}
}

func TestProviderExpr_KindDetection(t *testing.T) {
	t.Run("local provider has ProviderLocal kind", func(t *testing.T) {
		p := &ProviderExpr{Kind: ProviderLocal}
		require.Equal(t, ProviderLocal, p.Kind)
	})

	t.Run("MCP provider has ProviderMCP kind", func(t *testing.T) {
		p := &ProviderExpr{
			Kind:       ProviderMCP,
			MCPService: "svc",
			MCPToolset: "toolset",
		}
		require.Equal(t, ProviderMCP, p.Kind)
	})

	t.Run("registry provider has ProviderRegistry kind", func(t *testing.T) {
		p := &ProviderExpr{
			Kind:        ProviderRegistry,
			Registry:    &RegistryExpr{Name: "reg"},
			ToolsetName: "tools",
		}
		require.Equal(t, ProviderRegistry, p.Kind)
	})
}
