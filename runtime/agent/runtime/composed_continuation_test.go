// These tests exercise first calls and subsequent pages under one public name.
// Equal inputs and cursors remain separate chains through exact source call IDs.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

const composedExecutionSchema = `{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"]},{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"},"cursor":{"type":"string"}},"required":["query","cursor"]}]}`

func TestComposedContinuationKeepsExactChainsAndModelInput(t *testing.T) {
	rt, spec := composedContinuationRuntime(t)
	outputs := make([]*planner.ToolOutput, 0, 3)
	outputs = append(outputs,
		sourceContinuationOutput(spec.Name, "first", `{"query":"active"}`, "shared"),
		sourceContinuationOutput(spec.Name, "second", `{"query":"active"}`, "shared"),
	)
	actions, err := rt.availableContinuationActions("svc.agent", outputs, false)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.NotEqual(t, actions[0].modelName, actions[1].modelName)
	assert.NotContains(t, actions[0].description, "shared")
	calls, err := rt.compilePlannerToolCalls([]planner.ToolRequest{{Name: actions[0].modelName, Payload: rawjson.Message(`{}`), ModelToolCallID: "model"}}, actions, map[string]model.ToolCall{"model": {ID: "model", Name: actions[0].modelName, Payload: rawjson.Message(`{}`)}})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, spec.Name, calls[0].Name)
	assert.Equal(t, "first", calls[0].ContinuationRootToolCallID)
	assert.JSONEq(t, `{}`, string(calls[0].ModelPayload))
	assert.JSONEq(t, `{"query":"active","cursor":"shared"}`, string(calls[0].Payload))
	outputs = append(outputs, &planner.ToolOutput{Name: spec.Name, ToolCallID: "page", ContinuationRootToolCallID: "first", Payload: calls[0].Payload, Bounds: &agent.Bounds{Returned: 1}})
	actions, err = rt.availableContinuationActions("svc.agent", outputs, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "second", actions[0].state.rootToolCallID)
}

func TestComposedContinuationValidatesPageAndProgress(t *testing.T) {
	for _, test := range []struct {
		name, payload, root, next string
		invalid                   bool
	}{
		{"progress", `{"query":"active","cursor":"next"}`, "first", "third", false},
		{"no progress", `{"query":"active","cursor":"next"}`, "first", "next", true},
		{"wrong cursor", `{"query":"active","cursor":"other"}`, "first", "third", true},
		{"unknown chain", `{"query":"active","cursor":"next"}`, "unknown", "third", true},
		{"invalid page", `{"cursor":"next"}`, "first", "third", true},
		{"first input with page identity", `{"query":"active"}`, "first", "third", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, spec := composedContinuationRuntime(t)
			outputs := []*planner.ToolOutput{sourceContinuationOutput(spec.Name, "first", `{"query":"active"}`, "next"), {Name: spec.Name, ToolCallID: "page", ContinuationRootToolCallID: test.root, Payload: rawjson.Message(test.payload), Bounds: &agent.Bounds{NextCursor: &test.next}}}
			actions, err := rt.availableContinuationActions("svc.agent", outputs, false)
			if test.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, actions, 1)
			plan, ok := rt.automaticContinuationPlan(run.Context{}, actions)
			require.True(t, ok)
			assert.JSONEq(t, `{"query":"active","cursor":"third"}`, string(plan.ToolCalls[0].Payload))
		})
	}
}

func TestComposedContinuationCodeAuthoredPageAndFailedSibling(t *testing.T) {
	rt, spec := composedContinuationRuntime(t)
	spec.Payload.Schema = rawjson.Message(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"]}`)
	var err error
	spec.Payload.Codec, err = schema.Codec(spec.Payload.Schema)
	require.NoError(t, err)
	lookup := func(name tools.Ident) (tools.ToolSpec, bool) { return spec, name == spec.Name }
	for _, input := range []string{`{"query":"active"}`, `{"query":"active","cursor":"next"}`} {
		require.NoError(t, validatePlannerResultPayloadCodecs(lookup, &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(input)}}}, nil))
	}
	require.Error(t, validatePlannerResultPayloadCodecs(lookup, &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{"query":"active","cursor":"next"}`), ModelToolCallID: "model"}}}, nil))
	outputs := []*planner.ToolOutput{sourceContinuationOutput(spec.Name, "first", `{"query":"active"}`, "next"), {Name: spec.Name, ToolCallID: "failed", Payload: rawjson.Message(`{"invalid":true}`), Failure: testToolFailure(planner.FailureUnavailable, planner.RecoveryFinish, "unavailable")}}
	actions, err := rt.availableContinuationActions("svc.agent", outputs, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "first", actions[0].state.rootToolCallID)
	unavailable := rt.recoveryUnavailableTools("svc.agent", outputs)
	assert.Contains(t, unavailable, spec.Name)
	assert.NotContains(t, unavailable, actions[0].modelName)
	names, err := historicalContinuationToolNames(map[tools.Ident]tools.ToolSpec{spec.Name: spec})
	require.NoError(t, err)
	assert.Contains(t, names, spec.Name.String())
}

func TestComposedContinuationPortableHistoryAndCurrentEligibility(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	registered := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	declaration := registered.Toolset.Tools[0]
	declaration.ConsumerContract.Confirmation = nil
	declaration.ExecutionPayloadSchema = []byte(composedExecutionSchema)
	declaration.PayloadSchema = []byte(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"]}`)
	declaration.ConsumerContract.Payload.SchemaWithoutRootExample = declaration.PayloadSchema
	declaration.ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Path: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("query")}}}}
	declaration.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{ContinueTool: &declaration.Name, CursorField: "cursor", NextCursorField: "next_cursor", RetainedFields: []string{"query"}}}
	resolved, err := registrycontract.Resolve(registered)
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	before := append([]byte(nil), binding.Resolution...)
	source := sourceContinuationOutput("records.find", "first", `{"query":"active"}`, "next")
	source.Registry = binding
	actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{source}, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.JSONEq(t, `{"query":"active","cursor":"next"}`, string(actions[0].executablePayload))
	page := &planner.ToolOutput{Name: source.Name, ToolCallID: "page", Registry: binding, ContinuationRootToolCallID: "first", Payload: actions[0].executablePayload, Bounds: &agent.Bounds{Returned: 1}}
	registration := rt.agents[definition.route.ID]
	registration.Definition.registryTools = nil
	rt.agents[definition.route.ID] = registration
	actions, err = rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{source, page}, false)
	require.NoError(t, err)
	assert.Empty(t, actions)
	assert.Equal(t, before, []byte(binding.Resolution))
	page.ContinuationRootToolCallID = "unknown"
	_, err = rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{source, page}, false)
	require.ErrorContains(t, err, "unknown source tool call")
}

func TestComposedContinuationReminderUsesOriginalQuery(t *testing.T) {
	rt, spec := composedContinuationRuntime(t)
	call := ToolCall{Name: spec.Name, ToolCallID: "page", ContinuationRootToolCallID: "first", Payload: rawjson.Message(`{"query":"active","cursor":"next"}`)}
	next := "third"
	result := &planner.ToolResult{Name: spec.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"items": []any{"record"}}, Bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: &next}}
	base := &workflowConversation{finalization: &workflowFinalizationState{}, RunContext: run.Context{RunID: "run"}}
	record := materializedToolRecordForTest(t, rt, "svc.agent", base, call, result)
	require.NoError(t, rt.appendUserToolRecordResults(t.Context(), "svc.agent", base, []stepToolRecord{record}, ""))
	require.Len(t, base.Messages, 2)
	text, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	assert.Contains(t, text.Text, continuationActionName(spec.Name, "first").String())
	assert.NotContains(t, text.Text, continuationActionName(spec.Name, "page").String())
	assert.NotContains(t, text.Text, "third")
}

func TestComposedContinuationSurvivesFailedSiblingInRunLoop(t *testing.T) {
	_, spec := composedContinuationRuntime(t)
	load := newAnyJSONSpec("catalog.load")
	actionName := continuationActionName(spec.Name, "source")
	resumes := 0
	h := newRecoveryHarness(t, "composed-recovery", []tools.ToolSpec{spec, load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == load.Name {
				return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "load failed")}, nil
			}
			result := successfulToolResult(call)
			page, err := continuationPageInput(spec, call.Payload)
			require.NoError(t, err)
			if !page {
				result.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next")}
			} else {
				assert.JSONEq(t, `{"query":"active","cursor":"next"}`, string(call.Payload))
				result.Bounds = &agent.Bounds{Returned: 1}
			}
			return result, nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if resumes > 1 {
				assertAdvertisedTools(t, input)
				return finalPlannerResult("all pages collected"), nil
			}
			assertAdvertisedTools(t, input, actionName)
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test", Tools: input.Agent.AdvertisedToolDefinitions()})
			require.NoError(t, err)
			request, err := planner.ToolRequestFromModelCall(response.ToolCalls()[0])
			require.NoError(t, err)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
		})
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
		return testModelResponse(nil, model.ToolCall{ID: "page", Name: actionName, Payload: rawjson.Message(`{}`)}), nil
	}})
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: spec.Name, ToolCallID: "source", Payload: rawjson.Message(`{"query":"active"}`)},
		{Name: load.Name, ToolCallID: "failed", Payload: rawjson.Message(`{}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 5}))
	require.NoError(t, err)
	assert.Equal(t, "all pages collected", out.Final.Text())
	assert.Equal(t, 2, resumes)
	assert.Equal(t, 3, out.ToolCount)
}

func TestPlannerContinuationInputPreservesOrdinaryAndInjectedFirstCalls(t *testing.T) {
	ordinary := newAnyJSONSpec("records.read")
	ordinary.Payload.Codec.FromJSON = func([]byte) (any, error) {
		t.Fatal("ordinary input must only be decoded by its existing preparation")
		return nil, nil
	}
	page, err := plannerContinuationInput(ordinary, rawjson.Message(`{}`))
	require.NoError(t, err)
	assert.False(t, page)
	_, composed := composedContinuationRuntime(t)
	composed.ExecutionPayloadSchema = rawjson.Message(`{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"scope":{"type":"string"},"query":{"type":"string"}},"required":["scope","query"]},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"]}]}`)
	composed.ExecutionPayloadCodec, err = schema.Codec(composed.ExecutionPayloadSchema)
	require.NoError(t, err)
	page, err = plannerContinuationInput(composed, rawjson.Message(`{"query":"active"}`))
	require.NoError(t, err)
	assert.False(t, page)
}

func composedContinuationRuntime(t *testing.T) (*Runtime, tools.ToolSpec) {
	t.Helper()
	rt, source, _ := continuationTestRuntime()
	source.Bounds.Paging.ContinueTool = source.Name
	source.Bounds.Paging.RetainedFields = []string{"query"}
	source.ExecutionPayloadSchema = rawjson.Message(composedExecutionSchema)
	var err error
	source.ExecutionPayloadCodec, err = schema.Codec(source.ExecutionPayloadSchema)
	require.NoError(t, err)
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{source.Name: source}
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"svc.agent": {source}}
	rt.toolDefinitions = mustToolDefinitions([]tools.ToolSpec{source})
	return rt, source
}
