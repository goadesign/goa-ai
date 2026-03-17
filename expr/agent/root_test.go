package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	goaexpr "goa.design/goa/v3/expr"
)

func TestRootExprValidateRejectsSanitizedAgentCollisionsWithinService(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	root := &RootExpr{
		Agents: []*AgentExpr{
			{Name: "remote-tools", Service: service},
			{Name: "remote_tools", Service: service},
		},
	}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `sanitized agent name "remote_tools"`)
}

func TestRootExprValidateRejectsSanitizedToolsetCollisionsWithinOwner(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	agent := &AgentExpr{Name: "planner", Service: service}
	toolsetA := &ToolsetExpr{Name: "remote-tools", Agent: agent}
	toolsetB := &ToolsetExpr{Name: "remote_tools", Agent: agent}
	agent.Used = &ToolsetGroupExpr{
		Agent:    agent,
		Toolsets: []*ToolsetExpr{toolsetA, toolsetB},
	}
	root := &RootExpr{Agents: []*AgentExpr{agent}}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `sanitized toolset name "remote_tools"`)
}

func TestRootExprValidateRejectsSanitizedReferencedToolsetCollisionsWithinOwner(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	agent := &AgentExpr{Name: "planner", Service: service}
	originA := &ToolsetExpr{Name: "provider-a"}
	originB := &ToolsetExpr{Name: "provider-b"}
	toolsetA := &ToolsetExpr{Name: "remote-tools", Agent: agent, Origin: originA}
	toolsetB := &ToolsetExpr{Name: "remote_tools", Agent: agent, Origin: originB}
	agent.Used = &ToolsetGroupExpr{
		Agent:    agent,
		Toolsets: []*ToolsetExpr{toolsetA, toolsetB},
	}
	root := &RootExpr{Agents: []*AgentExpr{agent}}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `sanitized toolset name "remote_tools"`)
}

func TestRootExprValidateRejectsOwnerScopedDefiningToolsetCollisions(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	planner := &AgentExpr{Name: "planner", Service: service}
	runner := &AgentExpr{Name: "runner", Service: service}
	plannerToolset := &ToolsetExpr{Name: "remote-tools", Agent: planner}
	runnerToolset := &ToolsetExpr{Name: "remote_tools", Agent: runner}
	planner.Used = &ToolsetGroupExpr{
		Agent:    planner,
		Toolsets: []*ToolsetExpr{plannerToolset},
	}
	runner.Used = &ToolsetGroupExpr{
		Agent:    runner,
		Toolsets: []*ToolsetExpr{runnerToolset},
	}
	root := &RootExpr{Agents: []*AgentExpr{planner, runner}}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `sanitized toolset name "remote_tools"`)
	require.ErrorContains(t, err, "owner-scoped")
}
