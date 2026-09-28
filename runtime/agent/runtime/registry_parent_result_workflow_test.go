package runtime

// These tests carry a selected registry result through the real planner
// callback and workflow validation. Replacing the live declaration must not
// change the result accepted for the earlier call.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestRegistryParentResultCompletesWorkflowAfterLiveReplacement(t *testing.T) {
	rt := New(newTestStore())
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{}).WithAgentExecutors(&child)
	current := testNativeRegistryResolution("revision/1")
	reads := 0
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
		reads++
		return current, nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	catalog, err := rt.resolveRegistryCatalog(t.Context(), parent)
	require.NoError(t, err)
	selection, exists := catalog.selections["records.find"]
	require.True(t, exists)
	binding, err := selection.resolution.Select(selection.registry, "records.find")
	require.NoError(t, err)
	call := ToolCall{
		Name: "records.find", AgentID: parent.route.ID, RunID: "parent-run",
		SessionID: "session-1", TurnID: "turn-1", ToolCallID: "selected-call",
		Payload: rawjson.Message(`{"value":1}`), Registry: binding,
	}

	// The next catalog really changes both configuration and result shape.
	// The earlier call must still accept an integer, even though new calls require text.
	current = testNativeRegistryResolution("revision/2")
	current.RegistrationToken = strings.Repeat("b", 64)
	schema := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	current.Toolset.Tools[0].ResultSchema = schema
	current.Toolset.Tools[0].ConsumerContract.Result.SchemaWithoutRootExample = schema
	replacement, err := rt.resolveRegistryCatalog(t.Context(), parent)
	require.NoError(t, err)
	replacementSpec, exists := replacement.specs[call.Name]
	require.True(t, exists)
	_, err = replacementSpec.Result.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
	require.Error(t, err)
	_, err = replacementSpec.Result.Codec.FromJSON([]byte(`{"value":"new result"}`))
	require.NoError(t, err)
	retained, err := registrycontract.Read(binding)
	require.NoError(t, err)
	assert.Equal(t, "revision/1", retained.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	_, exists = rt.toolSpec(call.Name)
	require.False(t, exists)

	planned := 0
	registration := AgentRegistration{Definition: child, Planner: &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			planned++
			require.NotNil(t, input.ParentTool)
			assert.Equal(t, call.Name, input.ParentTool.Name)
			value, err := input.ParentTool.Result.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
			require.NoError(t, err)
			encoded, err := input.ParentTool.Result.Codec.ToJSON(value)
			require.NoError(t, err)
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: encoded}}, nil
		},
	}}
	rt.agents[child.route.ID] = registration
	nested := agentChildRunContext(&call)
	input := seedTestPlanInput(t, rt, PlanActivityInput{
		AgentID: child.route.ID, RunID: nested.RunID, RunContext: nested,
	}, nil)
	wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
	base := &workflowConversation{RunContext: nested, HistoryEndID: input.HistoryEndID}
	activity, err := rt.runPlanActivity(wf, "plan", engine.ActivityOptions{}, *input, base, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, activity.Result)
	require.Nil(t, activity.OutputContractFailure)
	output, err := rt.runLoopWithState(wf, registration, &RunInput{
		AgentID: child.route.ID, RunID: nested.RunID, SessionID: nested.SessionID,
	}, base, &runLoopState{
		Result: activity.Result, ResponseID: activity.PublicationBatchID,
		Transcript: activity.Transcript, Caps: initialCaps(RunPolicy{}),
	}, time.Time{}, time.Time{}, nested.TurnID, nil)
	require.NoError(t, err)
	require.NotNil(t, output.FinalToolResult)
	assert.Equal(t, nested.RunID, output.RunID)
	assert.Equal(t, call.Name, output.FinalToolResult.Name)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(output.FinalToolResult.Result))
	result, err := rt.adaptAgentChildOutput(&AgentToolConfig{Definition: child}, &call, nested, output)
	require.NoError(t, err)
	assert.Equal(t, call.ToolCallID, result.ToolCallID)
	assert.Equal(t, json.Number("9007199254740993"), result.Result.(map[string]any)["value"])
	assert.Equal(t, 1, planned)
	assert.Equal(t, 2, reads, "workflow validation must not resolve the live registry")
	_, exists = rt.toolSpec(call.Name)
	assert.False(t, exists)
}

func TestWorkflowParentResultPreservesContractRejections(t *testing.T) {
	rt := New(newTestStore())
	call := testNativeRegistryCall(t, "revision/1")
	nested := agentChildRunContext(&call)
	result := &PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":"wrong"}`)}}
	err := rt.normalizePlanResultForExecution(result, nested, "generic.agent")
	var rejected *planner.OutputContractError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, planner.OutputContractOriginPlanner, rejected.Origin())
	require.ErrorContains(t, rejected.Unwrap(), "planner final tool result:")

	result.FinalToolResult.Result = rawjson.Message(`{"value":1}`)
	_, err = rt.normalizePlanResultContract(result, nested, "wrong.agent")
	require.ErrorAs(t, err, &rejected)
	require.ErrorContains(t, rejected.Unwrap(), "does not target Agent executor")

	wrongName := nested
	wrongName.Tool = "records.other"
	_, err = rt.normalizePlanResultContract(result, wrongName, "generic.agent")
	require.ErrorAs(t, err, &rejected)
	require.ErrorContains(t, rejected.Unwrap(), `saved registry contract contains no tool "records.other"`)

	corruptCall := cloneToolCall(call)
	corruptCall.Registry.Resolution[0] = '!'
	_, err = rt.normalizePlanResultContract(result, agentChildRunContext(&corruptCall), "generic.agent")
	require.ErrorAs(t, err, &rejected)
	require.Error(t, rejected.Unwrap())

	static := nested
	static.ToolRegistry = nil
	_, err = rt.normalizePlanResultContract(result, static, "generic.agent")
	require.ErrorAs(t, err, &rejected)
	require.ErrorContains(t, rejected.Unwrap(), `planner final tool result references unregistered parent tool "records.find"`)
	seedTestToolSpecs(rt, newAnyJSONSpec(call.Name))
	err = rt.normalizePlanResultForExecution(result, static, "generic.agent")
	require.NoError(t, err)
}
