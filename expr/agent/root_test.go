package agent

import (
	"strings"
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

func TestRootExprValidateRejectsAgentNamesWithoutSanitizedIdentifier(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	root := &RootExpr{
		Agents: []*AgentExpr{
			{Name: "!!!", Service: service},
		},
	}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `agent name "!!!" must contain at least one letter or digit after sanitization`)
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

func TestRootExprValidateRejectsToolsetNamesWithoutSanitizedIdentifier(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "assistant"}
	agent := &AgentExpr{Name: "planner", Service: service}
	toolset := &ToolsetExpr{Name: "!!!", Agent: agent}
	agent.Used = &ToolsetGroupExpr{
		Agent:    agent,
		Toolsets: []*ToolsetExpr{toolset},
	}
	root := &RootExpr{Agents: []*AgentExpr{agent}}

	err := root.Validate()

	require.Error(t, err)
	require.ErrorContains(t, err, `toolset name "!!!" must contain at least one letter or digit after sanitization`)
}

func TestCompletionExprValidateStructuredOutputNameContract(t *testing.T) {
	cases := []struct {
		name           string
		completionName string
		wantErr        bool
	}{
		{name: "snake case", completionName: "draft_from_transcript"},
		{name: "hyphenated", completionName: "draft-task"},
		{name: "alphanumeric", completionName: "task1"},
		{name: "max length", completionName: strings.Repeat("a", 64)},
		{name: "empty", completionName: "", wantErr: true},
		{name: "invalid leading hyphen", completionName: "-draft", wantErr: true},
		{name: "invalid leading underscore", completionName: "_draft", wantErr: true},
		{name: "space", completionName: "draft from transcript", wantErr: true},
		{name: "too long", completionName: strings.Repeat("a", 65), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completion := &CompletionExpr{
				Name:    tc.completionName,
				Service: &goaexpr.ServiceExpr{Name: "tasks"},
				Return:  &goaexpr.AttributeExpr{Type: goaexpr.String},
			}

			err := completion.Validate()

			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, "must be 1-64 ASCII characters")
				return
			}
			require.NoError(t, err)
		})
	}
}
