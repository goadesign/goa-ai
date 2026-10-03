package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
	"goa.design/goa-ai/runtime/toolserverdata"
)

func TestTextOnlyGeneratedContracts(t *testing.T) {
	assert.Nil(t, genrecords.SpecShow().TextOnly)
	assert.Nil(t, genrecords.SpecErase().TextOnly)
	ordinary := genrecords.SpecRead()
	text := ordinary.ForTextOnly()
	assert.Contains(t, ordinary.Description, "card")
	assert.Contains(t, string(ordinary.Payload.Schema), "render_ui")
	_, err := ordinary.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","render_ui":true}`))
	require.NoError(t, err)
	assert.NotContains(t, text.Description, "card")
	assert.NotContains(t, string(text.Payload.Schema), "render_ui")
	assert.NotContains(t, string(text.Payload.SchemaWithoutRootExample), "render_ui")
	assert.NotContains(t, string(text.Payload.ExampleJSON), "render_ui")
	assert.NotContains(t, text.Search.Terms, "card")
	decoded, err := text.Payload.Codec.FromJSON([]byte(`{"query":"active"}`))
	require.NoError(t, err)
	payload, ok := decoded.(*genrecords.ReadPayload)
	require.True(t, ok)
	assert.True(t, payload.RenderUI == nil || !*payload.RenderUI)
	for _, value := range []string{"true", "false"} {
		_, err = text.Payload.Codec.FromJSON([]byte(`{"query":"active","render_ui":` + value + `}`))
		require.Error(t, err)
	}
	_, err = text.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","render_ui":false}`))
	require.NoError(t, err)
	_, err = text.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","render_ui":true}`))
	require.Error(t, err)
	for _, declaration := range genrecords.ToolSchemas() {
		spec, err := contract.Compile(declaration)
		require.NoError(t, err)
		if spec.Name != ordinary.Name {
			continue
		}
		remote := spec.ForTextOnly()
		assert.Equal(t, text.Description, remote.Description)
		assert.JSONEq(t, string(text.Payload.Schema), string(remote.Payload.Schema))
		_, err = remote.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","render_ui":true}`))
		require.Error(t, err)
	}
}

func TestTextOnlyCatalogAndExecution(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	specs := genrecords.Specs()
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"service.agent": specs}
	seedTestToolDefinitions(rt, specs...)
	ordinary := newAgentContext(agentContextOptions{runtime: rt, agentID: "service.agent"})
	assert.Len(t, ordinary.AdvertisedToolDefinitions(), 3)
	ctx := newAgentContext(agentContextOptions{runtime: rt, agentID: "service.agent", policy: compileToolPolicy(&PolicyOverrides{TextOnly: true})})
	definitions := ctx.AdvertisedToolDefinitions()
	require.Len(t, definitions, 1)
	assert.Equal(t, genrecords.Read.String(), definitions[0].Name)
	assert.NotContains(t, definitions[0].Description, "card")
	assert.NotContains(t, string(definitions[0].Input.Contract().Schema), "render_ui")
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
	}
	textRun := run.Context{RunID: "text", TextOnly: true}
	for _, spec := range []tools.ToolSpec{genrecords.SpecShow(), genrecords.SpecErase()} {
		err := rt.normalizePlanResultForExecution(&PlanResult{ToolCalls: []ToolCall{{Name: spec.Name, ToolCallID: "call", Payload: []byte(`{}`)}}}, textRun, "service.agent")
		require.Error(t, err)
	}
	for _, value := range []string{"true", "false"} {
		payload := []byte(`{"query":"active","render_ui":` + value + `}`)
		err := rt.normalizePlanResultForExecution(&PlanResult{ToolCalls: []ToolCall{{Name: genrecords.Read, ModelName: genrecords.Read, ModelPayload: payload, Payload: payload, ToolCallID: "call"}}}, textRun, "service.agent")
		require.Error(t, err)
	}
	err := rt.normalizePlanResultForExecution(&PlanResult{ToolCalls: []ToolCall{{Name: genrecords.Read, Payload: []byte(`{"query":"active","render_ui":false}`), ToolCallID: "compiled"}}}, textRun, "service.agent")
	require.NoError(t, err)
	err = rt.normalizePlanResultForExecution(&PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Which record?"}))}, textRun, "service.agent")
	require.Error(t, err)
}

func TestTextOnlyResultBoundary(t *testing.T) {
	rt := New(newTestStore())
	spec := genrecords.SpecRead()
	call := ToolCall{Name: genrecords.Read, ToolCallID: "read", TextOnly: true}
	result := &planner.ToolResult{Name: genrecords.Read, Result: &genrecords.ReadResult{Count: 2}, ServerData: []byte(`[{"kind":"record.card","audience":"timeline","data":{"count":2}}]`)}
	_, err := rt.materializeToolResultData(t.Context(), spec, call, result)
	require.ErrorContains(t, err, "violated text-only execution")
	result.ServerData = nil
	output, err := rt.materializeToolResultData(t.Context(), spec, call, result)
	require.NoError(t, err)
	assert.JSONEq(t, `{"count":2}`, string(output))
	require.NoError(t, toolserverdata.ValidateTextOnly([]byte(`[{"kind":"computation","audience":"internal","data":{"count":2}},{"kind":"provenance","audience":"evidence","data":{}}]`)))
	require.Error(t, toolserverdata.ValidateTextOnly([]byte(`[{"kind":"card","audience":"other","data":{}}]`)))
}

func TestTextOnlyChildAndCheckpointPolicy(t *testing.T) {
	definition := testRegistrationDefinition("service.child", engine.WorkflowDefinition{}, nil)
	for _, parent := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			policy := &PolicyOverrides{TextOnly: child}
			input, err := agentChildRunInput(definition, agentChildRequest{policy: policy, runContext: run.Context{RunID: "child", ParentRunID: "parent", ParentAgentID: "service.parent", ParentToolCallID: "call", Tool: genrecords.Read, TextOnly: parent}})
			require.NoError(t, err)
			assert.Equal(t, parent || child, input.Policy.TextOnly)
			assert.Equal(t, child, policy.TextOnly)
		}
	}
	saved := checkpointContextFromRun(run.Context{RunID: "first", TextOnly: true})
	restored := restoreCheckpointRunContext(saved, &RunInput{RunID: "resumed"})
	assert.True(t, restored.TextOnly)
	assert.False(t, restoreCheckpointRunContext(checkpointContextFromRun(run.Context{}), &RunInput{}).TextOnly)
}

func TestTextOnlyCompletedActivityFailsWithoutRetry(t *testing.T) {
	rt := New(newTestStore())
	calls := 0
	spec := genrecords.SpecRead()
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		calls++
		assert.True(t, call.TextOnly)
		assert.True(t, IsTextOnly(ctx))
		return &ToolExecutionResult{ToolResult: &planner.ToolResult{Name: genrecords.Read, Result: &genrecords.ReadResult{Count: 2}, ServerData: []byte(`[{"kind":"record.card","audience":"timeline","data":{"count":2}}]`)}}, nil
	}}))
	output, err := rt.ExecuteToolActivity(t.Context(), &ToolInput{ToolName: genrecords.Read, ToolsetName: "records", RunID: "run", ToolCallID: "read", Payload: []byte(`{"query":"active"}`), TextOnly: true})
	assert.Nil(t, output)
	require.Error(t, err)
	assert.True(t, engine.IsActivityErrorNonRetryable(err))
	assert.Equal(t, 1, calls)
}

func TestTextOnlyInlineExecutorReceivesAcceptedPolicy(t *testing.T) {
	rt := New(newTestStore())
	called := false
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Inline: true, Specs: []tools.ToolSpec{genrecords.SpecRead()}, Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		called = true
		assert.True(t, call.TextOnly)
		assert.True(t, IsTextOnly(ctx))
		return Executed(&planner.ToolResult{Name: genrecords.Read, Result: &genrecords.ReadResult{Count: 2}}), nil
	}}))
	execution := &toolBatchExec{r: rt, runID: "run", agentID: "records.reader", sessionID: "session", turnID: "turn", runCtx: &run.Context{RunID: "run", TextOnly: true}}
	batch, err := execution.dispatchToolCalls(&testWorkflowContext{ctx: t.Context(), hookRuntime: rt}, []ToolCall{{Name: genrecords.Read, ToolCallID: "read", Payload: []byte(`{"query":"active"}`)}})
	require.NoError(t, err)
	assert.True(t, called)
	require.NotNil(t, batch.inlineByID["read"])
	assert.Empty(t, batch.inlineByID["read"].ToolResult.ServerData)
}
