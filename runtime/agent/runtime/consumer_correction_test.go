// Corrected arguments must use the activity's input view after the runtime
// proves the original provider still has a registered executable contract.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestConsumerCorrectionRetainsExecutorAndUsesLocalInput(t *testing.T) {
	rt := New(newTestStore())
	original, err := contract.Compile(consumerResource("owner").Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "provider.records", Specs: []tools.ToolSpec{original}, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		t.Fatal("planning must not execute the corrected call")
		return nil, nil
	}}))
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil).WithInputBinding([]tools.Ident{original.Name}, func(_ context.Context, catalog *RegistryCatalog) error {
		return catalog.BindInput(original, consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"}))
	})
	rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{original}
	modelInput := rawjson.Message(`{ "query": "corrected" }`)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		visible := input.Agent.AdvertisedToolDefinitions()
		require.Len(t, visible, 1)
		assert.NotContains(t, string(visible[0].Input.Contract().Schema), `"scope"`)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: original.Name, Payload: modelInput}}}, nil
	}}}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))}
	call := ToolCall{Name: original.Name, ToolCallID: "failed", Payload: rawjson.Message(`{"scope":{"id":"accepted"},"query":"invalid"}`), ModelPayload: rawjson.Message(`{"query":"invalid"}`)}
	require.NoError(t, rt.publishHookErr(t.Context(), newToolCallScheduledEvent("run", subject, "session", call, "", "", 0), "turn"))
	require.NoError(t, rt.publishHookErr(t.Context(), hooks.NewToolResultReceivedEvent("run", subject, "session", "run", call.Name, call.ToolCallID, "", nil, nil, nil, "", nil, 0, nil, testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "choose another query")), "turn"))
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "run", RunContext: run.Context{RunID: "run", SessionID: "session"}, RecoveryToolCallIDs: []string{"failed"}, ToolOutputs: []*api.ToolOutputRef{{CallRunID: "run", ResultRunID: "run", ToolCallID: "failed"}}}, nil)
	output, err := rt.PlanResumeActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.Len(t, output.Result.ToolCalls, 1)
	assert.Equal(t, modelInput, output.Result.ToolCalls[0].ModelPayload)
	assert.JSONEq(t, `{"scope":{"id":"accepted"},"query":"corrected"}`, string(output.Result.ToolCalls[0].Payload))
	require.NoError(t, rt.normalizePlanResultForExecution(output.Result, run.Context{RunID: "run"}, definition.route.ID))
	delete(rt.toolsets, "provider.records")
	_, err = rt.PlanResumeActivity(t.Context(), input)
	require.ErrorContains(t, err, "no executable toolset registration")
}
