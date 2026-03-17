package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestBuildToolsetOwners_ServiceExportWins(t *testing.T) {
	shared := &agentsExpr.ToolsetExpr{Name: "shared"}
	bravo := &goaexpr.ServiceExpr{Name: "bravo"}
	alpha := &goaexpr.ServiceExpr{Name: "alpha"}
	agent := &agentsExpr.AgentExpr{
		Name:    "consumer",
		Service: alpha,
		Used: &agentsExpr.ToolsetGroupExpr{
			Toolsets: []*agentsExpr.ToolsetExpr{
				{Name: "shared", Origin: shared},
			},
		},
	}
	roots := []eval.Root{
		&goaexpr.RootExpr{Services: []*goaexpr.ServiceExpr{alpha, bravo}},
		&agentsExpr.RootExpr{
			Agents: []*agentsExpr.AgentExpr{agent},
			ServiceExports: []*agentsExpr.ServiceExportsExpr{
				{
					Service:  bravo,
					Toolsets: []*agentsExpr.ToolsetExpr{shared},
				},
			},
			Toolsets: []*agentsExpr.ToolsetExpr{shared},
		},
	}
	owners, err := buildToolsetOwners(roots)

	require.NoError(t, err)
	owner := owners["shared"]
	require.Equal(t, toolsetOwnerKindService, owner.kind)
	require.Equal(t, "bravo", owner.serviceName)
	require.Equal(t, "bravo", owner.servicePathName)
}

func TestBuildToolsetOwners_MCPProviderUsesProviderService(t *testing.T) {
	assistant := &goaexpr.ServiceExpr{Name: "assistant"}
	consumer := &goaexpr.ServiceExpr{Name: "consumer"}
	remote := &agentsExpr.ToolsetExpr{
		Name: "remote-tools",
		Provider: &agentsExpr.ProviderExpr{
			Kind:       agentsExpr.ProviderMCP,
			MCPService: assistant.Name,
			MCPToolset: "assistant-mcp",
		},
	}
	roots := []eval.Root{
		&goaexpr.RootExpr{Services: []*goaexpr.ServiceExpr{assistant, consumer}},
		&agentsExpr.RootExpr{
			Agents: []*agentsExpr.AgentExpr{
				{
					Name:    "planner",
					Service: consumer,
					Used: &agentsExpr.ToolsetGroupExpr{
						Toolsets: []*agentsExpr.ToolsetExpr{
							{Name: "remote-tools", Origin: remote},
						},
					},
				},
			},
			Toolsets: []*agentsExpr.ToolsetExpr{remote},
		},
	}
	owners, err := buildToolsetOwners(roots)

	require.NoError(t, err)
	owner := owners["remote-tools"]
	require.Equal(t, toolsetOwnerKindService, owner.kind)
	require.Equal(t, "assistant", owner.serviceName)
	require.Equal(t, "assistant", owner.servicePathName)
}

func TestBuildToolsetOwners_RejectsOwnerScopedSanitizedCollisions(t *testing.T) {
	consumer := &goaexpr.ServiceExpr{Name: "consumer"}
	planner := &agentsExpr.AgentExpr{Name: "planner", Service: consumer}
	runner := &agentsExpr.AgentExpr{Name: "runner", Service: consumer}
	planner.Used = &agentsExpr.ToolsetGroupExpr{
		Toolsets: []*agentsExpr.ToolsetExpr{
			{Name: "remote-tools", Agent: planner},
		},
	}
	runner.Used = &agentsExpr.ToolsetGroupExpr{
		Toolsets: []*agentsExpr.ToolsetExpr{
			{Name: "remote_tools", Agent: runner},
		},
	}
	roots := []eval.Root{
		&goaexpr.RootExpr{Services: []*goaexpr.ServiceExpr{consumer}},
		&agentsExpr.RootExpr{
			Agents: []*agentsExpr.AgentExpr{planner, runner},
		},
	}

	_, err := buildToolsetOwners(roots)

	require.Error(t, err)
	require.ErrorContains(t, err, `owner-scoped generated path`)
	require.ErrorContains(t, err, `remote_tools`)
}
