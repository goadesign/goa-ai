package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	require.NotNil(t, payload.RenderUI)
	assert.False(t, *payload.RenderUI)
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
		remoteValue, err := remote.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active"}`))
		require.NoError(t, err)
		remoteJSON, err := remote.ExecutionPayloadCodec.ToJSON(remoteValue)
		require.NoError(t, err)
		assert.JSONEq(t, `{"query":"active","render_ui":false,"render_summary":false}`, string(remoteJSON))
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
		assert.True(t, run.IsTextOnly(ctx))
		assert.JSONEq(t, `{"query":"active","render_ui":false,"render_summary":false}`, string(call.Payload))
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
		assert.True(t, run.IsTextOnly(ctx))
		assert.JSONEq(t, `{"query":"active","render_ui":false,"render_summary":false}`, string(call.Payload))
		return Executed(&planner.ToolResult{Name: genrecords.Read, Result: &genrecords.ReadResult{Count: 2}}), nil
	}}))
	execution := &toolBatchExec{r: rt, runID: "run", agentID: "records.reader", sessionID: "session", turnID: "turn", runCtx: &run.Context{RunID: "run", TextOnly: true}}
	batch, err := execution.dispatchToolCalls(&testWorkflowContext{ctx: t.Context(), hookRuntime: rt}, []ToolCall{{Name: genrecords.Read, ToolCallID: "read", Payload: []byte(`{"query":"active"}`)}})
	require.NoError(t, err)
	assert.True(t, called)
	require.NotNil(t, batch.inlineByID["read"])
	assert.Empty(t, batch.inlineByID["read"].ToolResult.ServerData)
}

func TestTextOnlyLimitFinalizationChecksInlineArguments(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  string
		rejected bool
	}{
		{name: "omitted control", payload: `{"query":"active"}`},
		{name: "disabled control", payload: `{"query":"active","render_ui":false}`},
		{name: "enabled control", payload: `{"query":"active","render_ui":true}`, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := New(newTestStore())
			spec := genrecords.SpecRead()
			spec.TerminalRun = true
			spec.Bookkeeping = true
			executions := 0
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "records", Inline: true, Specs: []tools.ToolSpec{spec},
				Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					executions++
					assert.True(t, run.IsTextOnly(ctx))
					assert.JSONEq(t, `{"query":"active","render_ui":false,"render_summary":false}`, string(call.Payload))
					return Executed(&planner.ToolResult{Name: call.Name, Result: &genrecords.ReadResult{Count: 2}}), nil
				},
			}))
			reg := AgentRegistration{Definition: testRegistrationDefinition("records.reader", engine.WorkflowDefinition{}, []tools.ToolSpec{spec})}
			rt.agents[reg.Definition.route.ID] = reg
			input := &RunInput{AgentID: reg.Definition.route.ID, RunID: "run", SessionID: "session", TurnID: "turn", Policy: &PolicyOverrides{TextOnly: true}}
			seedRunMeta(t, rt, input)
			_, err := createSessionForTest(t.Context(), rt.Store, input.SessionID)
			require.NoError(t, err)
			base := &workflowConversation{RunContext: run.Context{RunID: input.RunID, TextOnly: true, SessionID: input.SessionID}}
			wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt}
			output, err := rt.finishLimitTerminalCall(wf, reg, input, base, nil, nil, model.TokenUsage{}, initialCaps(RunPolicy{}), 1, input.TurnID, LimitTerminalCall{Name: spec.Name, Payload: rawjson.Message(test.payload)}, planner.TerminationReasonToolCap, time.Time{})
			if test.rejected {
				require.ErrorContains(t, err, "text-only execution arguments")
				assert.Nil(t, output)
				assert.Zero(t, executions)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, output)
			assert.Equal(t, 1, executions)
		})
	}
}

func TestTextOnlyContinuationKeepsRenderingControlsPrivate(t *testing.T) {
	rt, source, continuation := continuationTestRuntime()
	source.Payload.Fields = append(source.Payload.Fields, tools.FieldMetadata{Path: []tools.FieldPathSegment{tools.FixedField("render_ui")}, JSONType: "boolean"})
	source.TextOnly = &tools.ModelContract{Payload: source.Payload, ExecutionCodec: source.ExecutionPayloadCodec, ExecutionSchema: source.ExecutionPayloadSchema}
	source.TextOnly.Payload.Fields = source.Payload.Fields[:len(source.Payload.Fields)-1]
	continuation.TextOnly = &tools.ModelContract{Payload: continuation.Payload, ExecutionCodec: continuation.ExecutionPayloadCodec, ExecutionSchema: continuation.ExecutionPayloadSchema}
	rt.toolSpecs[source.Name] = source
	rt.toolSpecs[continuation.Name] = continuation
	rt.agentToolSpecs["svc.agent"] = []tools.ToolSpec{source, continuation}
	outputs := []*planner.ToolOutput{sourceContinuationOutput(source.Name, "source", `{"query":"active","render_ui":false}`, "next")}
	ordinary, err := rt.availableContinuationActions("svc.agent", outputs, false)
	require.NoError(t, err)
	require.Len(t, ordinary, 1)
	assert.Contains(t, ordinary[0].description, "render_ui")
	text, err := rt.availableContinuationActions("svc.agent", outputs, true)
	require.NoError(t, err)
	require.Len(t, text, 1)
	assert.Contains(t, text[0].description, "active")
	assert.NotContains(t, text[0].description, "render_ui")
	rt.toolConfirmation = &ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{continuation.Name: {}}}
	text, err = rt.availableContinuationActions("svc.agent", outputs, true)
	require.NoError(t, err)
	assert.Empty(t, text)
}

// TestTextOnlyNativeDefaultPrompt keeps rendering controls out of the child
// message while preserving the execution arguments and original model input.
func TestTextOnlyNativeDefaultPrompt(t *testing.T) {
	generated := genrecords.SpecRead()
	var portable tools.ToolSpec
	for _, declaration := range genrecords.ToolSchemas() {
		if declaration.Name == generated.Name.String() {
			var err error
			portable, err = contract.Compile(declaration)
			require.NoError(t, err)
		}
	}
	for _, spec := range []tools.ToolSpec{generated, portable} {
		t.Run(spec.Payload.Name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
			rt.toolSpecs[spec.Name] = spec
			original := rawjson.Message(`{"query":"explain render_ui as domain text"}`)
			payload, err := prepareTextOnlyExecutionPayload(spec, original)
			require.NoError(t, err)
			call := ToolCall{Name: spec.Name, Payload: payload, ModelPayload: append(rawjson.Message(nil), original...), TextOnly: true}
			request, err := rt.buildAgentChildRequest(t.Context(), &AgentToolConfig{}, &call, nil, nil)
			require.NoError(t, err)
			require.Len(t, request.messages, 1)
			assert.JSONEq(t, string(original), firstText(request.messages[0]))
			assert.JSONEq(t, `{"query":"explain render_ui as domain text","render_ui":false,"render_summary":false}`, string(request.runContext.ToolArgs))
			assert.Equal(t, original, call.ModelPayload)
			assert.Equal(t, payload, call.Payload)

			// An authored renderer still receives complete arguments and owns its
			// text. The framework never rewrites that author's instructions.
			cfg := &AgentToolConfig{AgentToolContent: AgentToolContent{Prompt: func(_ tools.Ident, value any) string {
				encoded, err := spec.ExecutionPayloadCodec.ToJSON(value)
				require.NoError(t, err)
				assert.JSONEq(t, string(payload), string(encoded))
				return "authored chart instructions"
			}}}
			request, err = rt.buildAgentChildRequest(t.Context(), cfg, &call, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, "authored chart instructions", firstText(request.messages[0]))

			call.TextOnly = false
			call.Payload = rawjson.Message("{ \"query\": \"ordinary\", \"render_ui\": true }")
			request, err = rt.buildAgentChildRequest(t.Context(), &AgentToolConfig{}, &call, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, string(call.Payload), firstText(request.messages[0]))
		})
	}
}

func TestTextOnlyNativeDefaultPromptPreservesDomainShapes(t *testing.T) {
	for _, input := range []string{`"domain render_ui text"`, `["chart",{"nested":[1,2]}]`, `{"arbitrary":{"keys":["render_ui",false]}}`} {
		t.Run(input, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
			spec := newAnyJSONSpec("service.tools.delegate")
			spec.TextOnly = &tools.ModelContract{Payload: spec.Payload, ExecutionCodec: spec.ExecutionPayloadCodec}
			rt.toolSpecs[spec.Name] = spec
			call := ToolCall{Name: spec.Name, Payload: rawjson.Message(input), TextOnly: true}
			request, err := rt.buildAgentChildRequest(t.Context(), &AgentToolConfig{}, &call, nil, nil)
			require.NoError(t, err)
			assert.JSONEq(t, input, firstText(request.messages[0]))
			assert.Equal(t, call.Payload, request.runContext.ToolArgs)
		})
	}
}

func TestTextOnlyRegistrationRequiresModelEncoder(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	spec := genrecords.SpecRead()
	spec.TextOnly.Payload.Codec.ToJSON = nil
	err := rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		return &ToolExecutionResult{}, nil
	}})
	assert.ErrorContains(t, err, "text-only codecs are required")
}
