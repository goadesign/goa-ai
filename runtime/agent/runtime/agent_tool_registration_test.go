// This file verifies that public toolset registration rejects agent tools that
// cannot execute through one complete, identity-consistent child-workflow route.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRegisterToolsetValidatesAgentToolExecution(t *testing.T) {
	const otherAgentID = "service.other"

	tests := []struct {
		name    string
		mutate  func(*ToolsetRegistration)
		wantErr string
	}{
		{
			name: "valid generated registration",
		},
		{
			name: "missing execution configuration",
			mutate: func(registration *ToolsetRegistration) {
				registration.AgentTool = nil
			},
			wantErr: "requires agent-tool execution configuration",
		},
		{
			name: "missing generated agent-tool marker",
			mutate: func(registration *ToolsetRegistration) {
				registration.Specs[0].IsAgentTool = false
			},
			wantErr: `agent toolset "service.tools" requires tool "service.tools.run" to be marked as an agent tool`,
		},
		{
			name: "activity execution mode",
			mutate: func(registration *ToolsetRegistration) {
				registration.Inline = false
			},
			wantErr: "requires inline child-agent execution",
		},
		{
			name: "missing spec agent id",
			mutate: func(registration *ToolsetRegistration) {
				registration.Specs[0].AgentID = ""
			},
			wantErr: "requires a generated agent id",
		},
		{
			name: "missing generated definition",
			mutate: func(registration *ToolsetRegistration) {
				registration.AgentTool.Definition = AgentDefinition{}
			},
			wantErr: "requires a generated agent definition",
		},
		{
			name: "spec and registration agent ids differ",
			mutate: func(registration *ToolsetRegistration) {
				registration.Specs[0].AgentID = otherAgentID
			},
			wantErr: `agent id "service.other" does not match definition "service.worker"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := New(newTestStore())
			registration := agentToolRegistrationFixture(runtime)
			if test.mutate != nil {
				test.mutate(&registration)
			}

			err := runtime.RegisterToolset(registration)
			if test.wantErr == "" {
				require.NoError(t, err)
				require.Contains(t, runtime.ListToolsets(), registration.Name)
				return
			}
			require.ErrorIs(t, err, ErrInvalidConfig)
			require.ErrorContains(t, err, test.wantErr)
			require.NotContains(t, runtime.ListToolsets(), registration.Name)
			_, registered := runtime.ToolSpec(registration.Specs[0].Name)
			require.False(t, registered)
		})
	}
}

func TestRegisterToolsetLeavesNonAgentToolsUnchanged(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("service.tools.lookup")

	err := runtime.RegisterToolset(ToolsetRegistration{
		Name:  "service.tools",
		Specs: []tools.ToolSpec{spec},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		}),
	})

	require.NoError(t, err)
	require.Contains(t, runtime.ListToolsets(), "service.tools")
}

func TestRegisterToolsetRejectsInvalidAgentToolAtomically(t *testing.T) {
	runtime := New(newTestStore())
	registration := agentToolRegistrationFixture(runtime)
	invalid := registration.Specs[0]
	invalid.Name = "service.tools.invalid"
	invalid.IsAgentTool = false
	registration.Specs = append(registration.Specs, invalid)

	err := runtime.RegisterToolset(registration)

	require.ErrorIs(t, err, ErrInvalidConfig)
	require.ErrorContains(
		t,
		err,
		`agent toolset "service.tools" requires tool "service.tools.invalid" to be marked as an agent tool`,
	)
	require.NotContains(t, runtime.ListToolsets(), registration.Name)
	_, toolsetPublished := runtime.toolsets[registration.Name]
	require.False(t, toolsetPublished)
	for _, spec := range registration.Specs {
		_, specPublished := runtime.ToolSpec(spec.Name)
		require.False(t, specPublished)
	}
}

func agentToolRegistrationFixture(runtime *Runtime) ToolsetRegistration {
	const agentID = agent.Ident("service.worker")
	spec := newAnyJSONSpec("service.tools.run")
	spec.IsAgentTool = true
	spec.AgentID = string(agentID)
	registration := NewAgentToolsetRegistration(runtime, AgentToolConfig{
		Definition: testAgentDefinition(agentID, "service.worker.workflow", "service.worker.queue", nil, nil),
		Name:       "service.tools",
	})
	registration.Specs = []tools.ToolSpec{spec}
	return registration
}
