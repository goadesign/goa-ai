package runtime

// These tests prove that prompt storage is read only by the child preparation
// activity and that workflow replay can consume its recorded result directly.

import (
	"context"
	"sync/atomic"
	"testing"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
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
		Store:          newTestStore(),
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
		Name:    toolName,
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))},

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

	historyEndID := testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parentRun, nil)
	activityOutput, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{
		Origin:       genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})},
		Call:         call,
		ParentRun:    parentRun,
		HistoryEndID: historyEndID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, store.resolveCalls.Load())
	require.NotNil(t, activityOutput.Success)
	childRun := agentChildRunContext(&call)
	seed, err := rt.Store.LoadRunSeed(t.Context(), childRun.RunID, activityOutput.Success.SeedEndID)
	require.NoError(t, err)
	require.Len(t, seed.Declaration.RenderedPrompts, 1)
	require.Equal(t, "v1", seed.Declaration.RenderedPrompts[0].Version)

	wfCtx := &testWorkflowContext{
		ctx:              context.Background(),
		agentChildOutput: activityOutput,
	}
	rt.agents = map[agent.Ident]AgentRegistration{
		"parent.agent": {Definition: testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil)},
	}
	request, stopped, err := rt.prepareAgentChild(wfCtx, call, historyEndID, parentRun)
	require.Nil(t, stopped)
	require.NoError(t, err)
	require.Equal(t, 1, wfCtx.agentChildCalls)
	require.Equal(t, "runtime.prepare_agent_child.parent.agent", wfCtx.lastAgentChildCall.Name)
	require.EqualValues(t, 1, store.resolveCalls.Load())
	require.Equal(t, activityOutput.Success.SeedEndID, request.seedEndID)
	require.Empty(t, request.messages)
	require.Equal(t, agentChildRunContext(&call), request.runContext)
	require.Empty(t, request.renderedPrompts)
}

func TestPrepareAgentChildRejectsAmbiguousActivityOutput(t *testing.T) {
	call := ToolCall{Name: "svc.agents.inspect", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, RunID: "parent", ToolCallID: "call"}
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
			rt := New(newTestStore())
			rt.agents["parent.agent"] = AgentRegistration{Definition: testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil)}
			_, _, err := rt.prepareAgentChild(wfCtx, call, "", run.Context{})
			require.ErrorContains(t, err, "exactly one of success, failure or stopped")
		})
	}
}

func TestPrepareAgentChildReturnsOwnerStop(t *testing.T) {
	call := ToolCall{Name: "svc.agents.inspect", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, RunID: "parent", ToolCallID: "call"}
	parent := run.Context{RunID: "parent"}
	stopped := &api.CancellationRequest{RunID: agentChildRunContext(&call).RunID, Reason: "custom-stop-reason"}
	wf := &testWorkflowContext{ctx: t.Context(), agentChildOutput: &api.AgentChildActivityOutput{Stopped: stopped}}
	rt := New(newTestStore())
	_, actual, err := rt.prepareAgentChild(wf, call, "", parent)
	require.NoError(t, err)
	require.Equal(t, stopped, actual)
	stopped.RunID = "another-child"
	_, _, err = rt.prepareAgentChild(wf, call, "", parent)
	require.ErrorContains(t, err, "another run")
}
