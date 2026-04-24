//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRunPlanActivityUsesOptions(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}},
			},
		},
	}

	opts := engine.ActivityOptions{
		Queue:               "custom_queue",
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2},
	}
	_, err := rt.runPlanActivity(wf, "calc.agent.plan", opts, PlanActivityInput{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, opts.Queue, wf.lastPlannerCall.Options.Queue)
	require.Equal(t, opts.StartToCloseTimeout, wf.lastPlannerCall.Options.StartToCloseTimeout)
	require.Equal(t, opts.RetryPolicy, wf.lastPlannerCall.Options.RetryPolicy)
}

func TestRunPlanActivityAcceptsTerminalFinalToolResult(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &planner.PlanResult{
			FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message([]byte(`{"status":"ok"}`)),
			},
		},
	}

	out, err := rt.runPlanActivity(wf, "calc.agent.plan", engine.ActivityOptions{}, PlanActivityInput{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Result)
	require.NotNil(t, out.Result.FinalToolResult)
	require.JSONEq(t, `{"status":"ok"}`, string(out.Result.FinalToolResult.Result))
}

func TestRunPlanActivityRejectsNilPlanResultWithoutCriticalPrefix(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx: context.Background(),
	}

	_, err := rt.runPlanActivity(wf, "calc.agent.plan", engine.ActivityOptions{}, PlanActivityInput{}, time.Time{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil PlanResult")
	require.NotContains(t, err.Error(), "CRITICAL:")
}

func TestPlanStartActivityInvokesPlanner(t *testing.T) {
	called := false
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.Equal(t, run.Context{RunID: "run-123"}, input.RunContext)
		require.Len(t, input.Messages, 1)
		require.Equal(t, "hello", agentMessageText(input.Messages[0]))
		require.NotNil(t, input.Agent)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}}, RunContext: run.Context{RunID: "run-123"}}
	out, err := rt.PlanStartActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlanStartActivityAdvertisesPolicyFilteredTools(t *testing.T) {
	called := false
	pl := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			called = true
			definitions := input.Agent.AdvertisedToolDefinitions()
			require.Len(t, definitions, 1)
			require.Equal(t, "svc.tools.visible", definitions[0].Name)
			require.Equal(t, "Visible tool", definitions[0].Description)
			schema, ok := definitions[0].InputSchema.(map[string]any)
			require.True(t, ok)
			require.Equal(t, "object", schema["type"])
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  "assistant",
						Parts: []model.Part{model.TextPart{Text: "ok"}},
					},
				},
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	visible := newAnyJSONSpec("svc.tools.visible", "svc.tools")
	visible.Description = "Visible tool"
	visible.Payload.Schema = []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	visible.Tags = []string{"system", "profile"}
	blocked := newAnyJSONSpec("svc.tools.blocked", "svc.tools")
	blocked.Tags = []string{"system"}
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{
		"service.agent": {visible, blocked},
	}
	input := PlanActivityInput{
		AgentID:  "service.agent",
		RunID:    "run-123",
		Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
		RunContext: run.Context{
			RunID: "run-123",
		},
		Policy: &PolicyOverrides{
			TagClauses: []TagPolicyClause{{AllowedAny: []string{"profile"}}},
		},
	}
	out, err := rt.PlanStartActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlannerBoundaryOmitsToolResultsField(t *testing.T) {
	t.Parallel()

	planResumeInputType := reflect.TypeOf(planner.PlanResumeInput{})
	_, hasPlannerToolResults := planResumeInputType.FieldByName("ToolResults")
	require.False(t, hasPlannerToolResults, "PlanResumeInput must expose ToolOutputs as its only execution-history field")

	planActivityInputType := reflect.TypeOf(PlanActivityInput{})
	_, hasActivityToolResults := planActivityInputType.FieldByName("ToolResults")
	require.False(t, hasActivityToolResults, "PlanActivityInput must expose ToolOutputs as its only execution-history field")
}

func TestPlanResumeActivityPassesToolOutputs(t *testing.T) {
	called := false
	toolName := tools.Ident("svc.ts.tool")
	resultJSON := rawjson.Message([]byte(`{"status":"ok"}`))
	serverData := rawjson.Message([]byte(`[{"kind":"evidence"}]`))
	total := 17
	bounds := &agent.Bounds{Returned: 10, Total: &total, Truncated: true, RefinementHint: "narrow the window"}
	toolOutputs := []*api.ToolOutputRef{{ToolCallID: "call-1"}}
	pl := &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.Len(t, input.ToolOutputs, 1)
		require.Equal(t, toolName, input.ToolOutputs[0].Name)
		require.Equal(t, "call-1", input.ToolOutputs[0].ToolCallID)
		require.JSONEq(t, `{"from":"test"}`, string(input.ToolOutputs[0].Payload))
		require.JSONEq(t, `{"status":"ok"}`, string(input.ToolOutputs[0].Result))
		require.JSONEq(t, `[{"kind":"evidence"}]`, string(input.ToolOutputs[0].ServerData))
		require.Equal(t, len(resultJSON), input.ToolOutputs[0].ResultBytes)
		require.NotNil(t, input.ToolOutputs[0].Bounds)
		require.True(t, input.ToolOutputs[0].Bounds.Truncated)
		require.Equal(t, "narrow the window", input.ToolOutputs[0].Bounds.RefinementHint)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "svc.other.tool"}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			toolName,
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolResultReceivedEvent(
			"run-123",
			"service.agent",
			"",
			toolName,
			"call-1",
			"",
			resultJSON,
			len(resultJSON),
			false,
			"",
			serverData,
			"preview",
			bounds,
			50*time.Millisecond,
			nil,
			nil,
			nil,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID:     "service.agent",
		RunID:       "run-123",
		RunContext:  run.Context{RunID: "run-123", Attempt: 3},
		ToolOutputs: toolOutputs,
	}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.Len(t, out.Result.ToolCalls, 1)
}

func TestPlanResumeActivityFailsWhenCanonicalToolResultIsMissing(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			"svc.ts.tool",
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID: "service.agent",
		RunID:   "run-123",
		RunContext: run.Context{
			RunID:   "run-123",
			Attempt: 1,
		},
		ToolOutputs: []*api.ToolOutputRef{{ToolCallID: "call-1"}},
	}

	_, err := rt.PlanResumeActivity(context.Background(), &input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing canonical tool result in run log")
}

func TestPlanResumeActivityHydratesOmittedResultMetadataFromCanonicalRunlog(t *testing.T) {
	called := false
	pl := &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.Len(t, input.ToolOutputs, 1)
		require.Equal(t, "call-1", input.ToolOutputs[0].ToolCallID)
		require.True(t, input.ToolOutputs[0].ResultOmitted)
		require.Equal(t, "workflow_budget", input.ToolOutputs[0].ResultOmittedReason)
		require.Equal(t, 12345, input.ToolOutputs[0].ResultBytes)
		require.Nil(t, input.ToolOutputs[0].Result)
		require.JSONEq(t, `[{"kind":"evidence"}]`, string(input.ToolOutputs[0].ServerData))
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "svc.other.tool"}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolCallScheduledEvent(
			"run-123",
			"service.agent",
			"",
			"svc.ts.tool",
			"call-1",
			rawjson.Message([]byte(`{"from":"test"}`)),
			"queue",
			"",
			0,
		),
		"",
	))
	require.NoError(t, rt.publishHookErr(
		context.Background(),
		hooks.NewToolResultReceivedEvent(
			"run-123",
			"service.agent",
			"",
			"svc.ts.tool",
			"call-1",
			"",
			nil,
			12345,
			true,
			"workflow_budget",
			rawjson.Message([]byte(`[{"kind":"evidence"}]`)),
			"preview",
			nil,
			0,
			nil,
			nil,
			nil,
		),
		"",
	))
	input := PlanActivityInput{
		AgentID:     "service.agent",
		RunID:       "run-123",
		RunContext:  run.Context{RunID: "run-123", Attempt: 2},
		ToolOutputs: []*api.ToolOutputRef{{ToolCallID: "call-1"}},
	}

	_, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
}

func TestBuildPlannerToolOutputsPreservesOmittedResultMetadata(t *testing.T) {
	t.Parallel()

	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.ts.tool", "svc.tools"))

	outputs, err := rt.buildPlannerToolOutputs(
		context.Background(),
		[]planner.ToolRequest{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Payload:    rawjson.Message([]byte(`{"from":"test"}`)),
			},
		},
		[]*planner.ToolResult{
			{
				Name:                "svc.ts.tool",
				ToolCallID:          "call-1",
				ResultOmitted:       true,
				ResultOmittedReason: "workflow_budget",
				ResultBytes:         12345,
				ServerData:          rawjson.Message([]byte(`[{"kind":"evidence"}]`)),
			},
		},
	)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.True(t, outputs[0].ResultOmitted)
	require.Equal(t, "workflow_budget", outputs[0].ResultOmittedReason)
	require.Equal(t, 12345, outputs[0].ResultBytes)
	require.Empty(t, outputs[0].Result)
	require.JSONEq(t, `[{"kind":"evidence"}]`, string(outputs[0].ServerData))
}

func TestBuildPlannerToolOutputsSkipsBookkeepingResults(t *testing.T) {
	t.Parallel()

	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.ts.tool", "svc.tools"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.set_step_status", "tasks.progress")
			spec.Bookkeeping = true
			return spec
		}(),
	)

	outputs, err := rt.buildPlannerToolOutputs(
		context.Background(),
		[]planner.ToolRequest{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Payload:    rawjson.Message([]byte(`{"from":"test"}`)),
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Payload:    rawjson.Message([]byte(`{"step":"verify"}`)),
			},
		},
		[]*planner.ToolResult{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Result:     map[string]any{"status": "ok"},
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Result:     map[string]any{"ok": true},
			},
		},
	)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, "call-1", outputs[0].ToolCallID)
	require.Equal(t, tools.Ident("svc.ts.tool"), outputs[0].Name)
}

func TestBuildPlannerToolOutputsKeepsPlannerVisibleBookkeepingResults(t *testing.T) {
	t.Parallel()

	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.ts.tool", "svc.tools"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.set_step_status", "tasks.progress")
			spec.Bookkeeping = true
			spec.PlannerVisible = true
			return spec
		}(),
	)

	outputs, err := rt.buildPlannerToolOutputs(
		context.Background(),
		[]planner.ToolRequest{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Payload:    rawjson.Message([]byte(`{"from":"test"}`)),
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Payload:    rawjson.Message([]byte(`{"step":"verify"}`)),
			},
		},
		[]*planner.ToolResult{
			{
				Name:       "svc.ts.tool",
				ToolCallID: "call-1",
				Result:     map[string]any{"status": "ok"},
			},
			{
				Name:       "tasks.progress.set_step_status",
				ToolCallID: "call-2",
				Result:     map[string]any{"ok": true},
			},
		},
	)
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	require.Equal(t, "call-2", outputs[1].ToolCallID)
	require.Equal(t, tools.Ident("tasks.progress.set_step_status"), outputs[1].Name)
}

func TestBuildNextResumeRequestRejectsNilToolOutputEntry(t *testing.T) {
	t.Parallel()

	rt := &Runtime{}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-123",
			SessionID: "sess-1",
		},
	}
	nextAttempt := 1

	_, err := rt.buildNextResumeRequest(
		"svc.agent",
		base,
		nil,
		[]*planner.ToolOutput{nil},
		&nextAttempt,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil tool output")
}

func TestBuildNextResumeRequestUsesProviderNeutralTranscriptValidation(t *testing.T) {
	t.Parallel()

	rt := &Runtime{}
	base := &planner.PlanInput{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "call-1", Name: "svc.tool"},
			},
		}},
		RunContext: run.Context{
			RunID:     "run-123",
			SessionID: "sess-1",
		},
	}
	nextAttempt := 1

	_, err := rt.buildNextResumeRequest(
		"svc.agent",
		base,
		nil,
		nil,
		&nextAttempt,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid resume transcript")
	require.NotContains(t, err.Error(), "Bedrock")
}

func TestPlanResumeActivityPreservesEmptyRawJSONPayloads(t *testing.T) {
	pl := &stubPlanner{
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{
					{
						Name:    "svc.other.tool",
						Payload: rawjson.Message([]byte{}),
					},
				},
				Await: planner.NewAwait(
					planner.AwaitQuestionsItem(&planner.AwaitQuestions{
						ID:         "await-q",
						ToolName:   "chat.ask_question.ask_question",
						ToolCallID: "call-q",
						Payload:    rawjson.Message([]byte{}),
					}),
					planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
						ID: "await-ext",
						Items: []planner.AwaitToolItem{
							{
								Name:       "external.one",
								ToolCallID: "call-ext",
								Payload:    rawjson.Message([]byte{}),
							},
						},
					}),
				),
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.Len(t, out.Result.ToolCalls, 1)
	require.NotNil(t, out.Result.ToolCalls[0].Payload)
	require.Empty(t, out.Result.ToolCalls[0].Payload)
	require.NotNil(t, out.Result.Await)
	require.Len(t, out.Result.Await.Items, 2)
	require.NotNil(t, out.Result.Await.Items[0].Questions)
	require.NotNil(t, out.Result.Await.Items[0].Questions.Payload)
	require.Empty(t, out.Result.Await.Items[0].Questions.Payload)
	require.NotNil(t, out.Result.Await.Items[1].ExternalTools)
	require.Len(t, out.Result.Await.Items[1].ExternalTools.Items, 1)
	require.NotNil(t, out.Result.Await.Items[1].ExternalTools.Items[0].Payload)
	require.Empty(t, out.Result.Await.Items[1].ExternalTools.Items[0].Payload)
}

func TestNormalizeTranscriptRawJSONNormalizesEmptyRawMessageValues(t *testing.T) {
	messages := []*model.Message{
		{
			Role: "assistant",
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call-1",
					Name:  "tool.one",
					Input: json.RawMessage{},
				},
				model.ToolResultPart{
					ToolUseID: "call-1",
					Content: map[string]any{
						"payload": json.RawMessage{},
					},
				},
			},
			Meta: map[string]any{
				"raw": json.RawMessage{},
			},
		},
	}

	normalizeTranscriptRawJSON(messages)

	toolUse, ok := messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Nil(t, toolUse.Input)
	toolResult, ok := messages[0].Parts[1].(model.ToolResultPart)
	require.True(t, ok)
	content, ok := toolResult.Content.(map[string]any)
	require.True(t, ok)
	require.Nil(t, content["payload"])
	require.Nil(t, messages[0].Meta["raw"])
}
