package runtime

// These tests prove that prompt storage is read only by the child preparation
// activity and that workflow replay can consume its recorded result directly.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type countingPromptStore struct {
	*prompt.InMemoryStore
	resolveCalls atomic.Int32
}

func (s *countingPromptStore) Resolve(ctx context.Context, id prompt.Ident, scope prompt.Scope) (*prompt.Override, error) {
	s.resolveCalls.Add(1)
	return s.InMemoryStore.Resolve(ctx, id, scope)
}

func TestPrepareAgentChildUsesRecordedActivityOutputWithoutRendering(t *testing.T) {
	store := &countingPromptStore{InMemoryStore: prompt.NewInMemoryStore()}
	registry := prompt.NewRegistry(store)
	require.NoError(t, registry.Register(prompt.PromptSpec{
		ID:       "nested.request",
		AgentID:  "nested.agent",
		Role:     prompt.PromptRoleUser,
		Template: "inspect {{ .query }}",
		Version:  "v1",
	}))

	rt := &Runtime{
		toolsets:       make(map[string]ToolsetRegistration),
		toolSpecs:      make(map[tools.Ident]tools.ToolSpec),
		PromptRegistry: registry,
		logger:         telemetry.NoopLogger{},
	}
	toolName := tools.Ident("svc.agents.inspect")
	cfg := AgentToolConfig{
		Definition: testAgentDefinition(agent.Ident("nested.agent"), "nested.workflow", "nested.queue", nil, nil),
		AgentToolContent: AgentToolContent{
			PromptSpecs: map[tools.Ident]prompt.Ident{
				toolName: "nested.request",
			},
		},
	}
	registerAgentToolTestConfig(rt, cfg, "svc.agents", newAnyJSONSpec(toolName))
	call := ToolCall{
		Name:       toolName,
		AgentID:    "parent.agent",
		RunID:      "parent-run",
		SessionID:  "session-1",
		TurnID:     "turn-1",
		ToolCallID: "call-1",
		Payload:    rawjson.Message([]byte(`{"query":"compressor"}`)),
	}
	parentRun := run.Context{
		RunID:     call.RunID,
		SessionID: call.SessionID,
		TurnID:    call.TurnID,
	}

	activityOutput, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{
		Call:      call,
		ParentRun: parentRun,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, store.resolveCalls.Load())
	require.NotNil(t, activityOutput.Success)
	require.Len(t, activityOutput.Success.RenderedPrompts, 1)
	require.Equal(t, "v1", activityOutput.Success.RenderedPrompts[0].Version)

	wfCtx := &testWorkflowContext{
		ctx:              context.Background(),
		agentChildOutput: activityOutput,
	}
	request, err := rt.prepareAgentChild(wfCtx, call, nil, parentRun)
	require.NoError(t, err)
	require.Equal(t, 1, wfCtx.agentChildCalls)
	require.Equal(t, agentChildActivityName, wfCtx.lastAgentChildCall.Name)
	require.EqualValues(t, 1, store.resolveCalls.Load())
	require.Equal(t, activityOutput.Success.Messages, request.messages)
	require.Equal(t, agentChildRunContext(&call), request.runContext)
	require.Equal(t, activityOutput.Success.RenderedPrompts, request.renderedPrompts)
}

func TestPrepareAgentChildRejectsAmbiguousActivityOutput(t *testing.T) {
	call := ToolCall{Name: "svc.agents.inspect", RunID: "parent", ToolCallID: "call"}
	failure := &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.NewToolError("invalid child request"),
	}
	tests := []struct {
		name   string
		output *api.AgentChildActivityOutput
	}{
		{name: "no result", output: &api.AgentChildActivityOutput{}},
		{name: "success and failure", output: &api.AgentChildActivityOutput{
			Success: &api.AgentChildActivitySuccess{}, Failure: failure,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wfCtx := &testWorkflowContext{ctx: t.Context(), agentChildOutput: test.output}
			_, err := (&Runtime{}).prepareAgentChild(wfCtx, call, nil, run.Context{})
			require.ErrorContains(t, err, "exactly one of success or failure")
		})
	}
}
