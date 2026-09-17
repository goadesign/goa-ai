package runtime

// These tests verify that execution and saved-result restoration use a selected
// registration even when no static tool implementation exists in the process.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestRegistryActivityPreservesSelectedAdmission(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	resolved, err := registrycontract.Resolve(testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)))
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	calls := 0
	client := &genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, value any) (any, error) {
		calls++
		payload := value.(*genregistry.CallResolvedToolPayload)
		assert.Equal(t, strings.Repeat("a", 64), payload.ExpectedRegistrationToken)
		assert.Equal(t, "provider.records", payload.Toolset)
		assert.Equal(t, "records.find", payload.Tool)
		assert.JSONEq(t, `{"value":9007199254740993}`, string(payload.PayloadJSON))
		assert.Equal(t, toolregistry.WireProtocolVersion, payload.WireProtocolVersion)
		assert.Equal(t, "call-1", payload.Meta.ToolCallID)
		return nil, genregistry.MakeCallNotAdmitted(errors.New("registration was replaced"))
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	output, err := rt.ExecuteToolActivity(t.Context(), &ToolInput{
		Registry: binding, AgentID: definition.route.ID, ToolName: "records.find",
		RunID: "run-1", SessionID: "session-1", ToolCallID: "call-1",
		Payload: rawjson.Message(`{"value":9007199254740993}`),
	})
	require.NoError(t, err)
	require.NotNil(t, output.Failure)
	assert.Equal(t, planner.FailureUnavailable, output.Failure.Kind)
	assert.Equal(t, planner.RecoveryReplan, output.Failure.Recovery.Action)
	assert.Equal(t, 1, calls)
}

func TestRegistryExecutionChecksCurrentConsumptionAndRequiredLabels(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	registered := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	registered.Toolset.Tools[0].ConsumerContract.RequiredLabels = []string{"facility"}
	resolved, err := registrycontract.Resolve(registered)
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	call := ToolCall{Name: "records.find", Registry: binding}
	_, err = rt.resolveRegistryExecution(definition.route.ID, call)
	require.ErrorContains(t, err, `requires run label "facility"`)
	call.Labels = map[string]string{"facility": "site-1"}
	_, err = rt.resolveRegistryExecution(definition.route.ID, call)
	require.NoError(t, err)
	call.Registry = binding.Clone()
	call.Registry.Registry = "unconsumed"
	_, err = rt.resolveRegistryExecution(definition.route.ID, call)
	require.ErrorContains(t, err, "does not consume")
}

func TestRegistryResultRestoresWithoutCurrentCatalog(t *testing.T) {
	rt := New(newTestStore())
	resolved, err := registrycontract.Resolve(testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)))
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	call := ToolCall{
		Name: "records.find", Registry: binding, ToolCallID: "call-1",
		Payload: rawjson.Message(`{"value":1}`),
	}
	scheduled := newToolCallScheduledEvent("run-1", "records.agent", "session-1", call, "", "", 0)
	result := hooks.NewToolResultReceivedEvent(
		"run-1", "records.agent", "session-1", "run-1", "records.find", "call-1", "",
		rawjson.Message(`{"value":9007199254740993}`), nil, "", nil, 0, nil, nil,
	)
	output, err := rt.plannerToolOutputFromCanonicalEvents("run-1", "run-1", "call-1",
		&canonicalToolEvents{scheduled: scheduled}, &canonicalToolEvents{result: result})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(output.Result))
	require.NotNil(t, output.Registry)
	assert.Equal(t, binding.Resolution, output.Registry.Resolution)
	output.Registry.Resolution[0] = '!'
	assert.NotEqual(t, output.Registry.Resolution, scheduled.Registry.Resolution)
}

func TestRegistryCorrectionUsesNewlyResolvedContract(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	old, err := registrycontract.Resolve(testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)))
	require.NoError(t, err)
	binding, err := old.Select("company", "records.find")
	require.NoError(t, err)
	call := ToolCall{Name: "records.find", ToolCallID: "failed", Registry: binding, Payload: rawjson.Message(`{"value":1}`)}
	require.NoError(t, rt.publishHookErr(t.Context(),
		newToolCallScheduledEvent("run", definition.route.ID, "session", call, "", "", 0), "turn"))
	require.NoError(t, rt.publishHookErr(t.Context(), hooks.NewToolResultReceivedEvent(
		"run", definition.route.ID, "session", "run", call.Name, call.ToolCallID, "",
		nil, nil, "", nil, 0, nil, testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "choose another value"),
	), "turn"))

	current := testRuntimeRegistryResolution("records.find", strings.Repeat("b", 64))
	schema := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	declaration := current.Toolset.Tools[0]
	declaration.PayloadSchema = schema
	declaration.ExecutionPayloadSchema = schema
	declaration.ConsumerContract.Payload.SchemaWithoutRootExample = schema
	reads := 0
	require.NoError(t, rt.RegisterRegistry("company", &genregistry.Client{
		ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
			reads++
			return current, nil
		},
	}, unusedRegistryPulse{}))
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.Len(t, input.Agent.AdvertisedToolDefinitions(), 1)
			require.Len(t, input.ToolOutputs, 1)
			assert.JSONEq(t, string(binding.Resolution), string(input.ToolOutputs[0].Registry.Resolution))
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name: "records.find", Payload: rawjson.Message(`{"value":"corrected"}`),
			}}}, nil
		},
	}}
	output, err := rt.PlanResumeActivity(t.Context(), &PlanActivityInput{
		AgentID: definition.route.ID, RunID: "run", RunContext: run.Context{RunID: "run", SessionID: "session"},
		ToolOutputs:         []*api.ToolOutputRef{{CallRunID: "run", ResultRunID: "run", ToolCallID: "failed"}},
		RecoveryToolCallIDs: []string{"failed"},
	})
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.Result)
	selected, err := registrycontract.Read(output.Result.ToolCalls[0].Registry)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("b", 64), selected.Registered.RegistrationToken)
	assert.Equal(t, 1, reads)
}
