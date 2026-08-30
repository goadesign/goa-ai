// Package codegen tests that templates only print decisions made by the
// generator. This keeps design inspection out of the rendering phase.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen/service"
)

// TestRegistryFileDataClassifiesToolsets verifies that exported agent tools do
// not enter direct registration and MCP toolsets use the remote path only.
func TestRegistryFileDataClassifiesToolsets(t *testing.T) {
	direct := &ToolsetData{Name: "direct"}
	remote := &ToolsetData{Name: "remote", MCP: &MCPToolsetMeta{SuiteName: "RemoteSuite"}}
	agentTool := &ToolsetData{Name: "agent", AgentToolsImportPath: "example.com/agenttools"}

	data := newAgentRegistryFileData(&AgentData{
		UsedToolsets: []*ToolsetData{agentTool, direct, remote},
		AllToolsets:  []*ToolsetData{agentTool, direct, remote},
	})

	require.Equal(t, []*ToolsetData{remote}, data.MCPToolsets)
	require.Equal(t, []*ToolsetData{direct}, data.DirectToolsets)
}

// TestQuickstartDataPreparesProviderFacts verifies that the guide receives
// display-ready MCP labels, caller names, and provider-section selection.
func TestQuickstartDataPreparesProviderFacts(t *testing.T) {
	remoteMeta := &MCPToolsetMeta{
		ServiceName: "catalog",
		SuiteName:   "RemoteSuite",
	}
	remote := &ToolsetData{
		Name:          "search",
		QualifiedName: "catalog.search",
		MCP:           remoteMeta,
	}
	method := &ToolsetData{
		Name:          "local",
		SpecsDir:      "gen/catalog/toolsets/local",
		SourceService: &service.Data{},
		Tools:         []*ToolData{{IsMethodBacked: true}},
	}
	agent := &AgentData{
		UsedToolsets: []*ToolsetData{method, remote},
		AllToolsets:  []*ToolsetData{method, remote},
		MCPToolsets:  []*MCPToolsetMeta{remoteMeta},
	}

	data := agentQuickstartData(&GeneratorData{Services: []*ServiceAgentsData{{
		Agents: []*AgentData{agent},
	}}}, nil)

	require.NotNil(t, data)
	require.True(t, data.HasServiceProviders)
	require.Len(t, data.Services, 1)
	require.Len(t, data.Services[0].Agents, 1)
	quickstartAgent := data.Services[0].Agents[0]
	require.Equal(t, "catalog.search", quickstartAgent.UsedToolsets[1].ProviderLabel)
	require.Equal(t, "remotesuite", quickstartAgent.MCPToolsets[0].CallerName)
}
