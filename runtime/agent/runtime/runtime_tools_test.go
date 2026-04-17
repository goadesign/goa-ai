package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type projectedRuntimeResult struct {
	Results []string `json:"results"`
}

type projectedRuntimePayload struct {
	Query string `json:"query"`
}

func newProjectedResultSpec() tools.ToolSpec {
	payloadCodec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == jsonNullLiteral {
				return nil, nil
			}
			var out projectedRuntimePayload
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return &out, nil
		},
	}
	codec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == jsonNullLiteral {
				return nil, nil
			}
			var out projectedRuntimeResult
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return &out, nil
		},
	}
	return tools.ToolSpec{
		Name:    "tool",
		Toolset: "svc.ts",
		Payload: tools.TypeSpec{Name: "tool_payload", Codec: payloadCodec},
		Result:  tools.TypeSpec{Name: "tool_result", Codec: codec},
		Bounds: &tools.BoundsSpec{
			Paging: &tools.PagingSpec{
				CursorField:     "cursor",
				NextCursorField: "next_cursor",
			},
		},
	}
}

func TestExecuteToolActivityReturnsErrorAndHint(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		require.Equal(t, "tool-1", call.ToolCallID)
		require.Equal(t, "parent-1", call.ParentToolCallID)
		require.Equal(t, "run", call.RunID)
		return &planner.ToolResult{
			Name:  call.Name,
			Error: planner.NewToolError("invalid payload"),
			RetryHint: &planner.RetryHint{
				Reason: planner.RetryReasonInvalidArguments,
				Tool:   call.Name,
			},
		}, nil
	})}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", ParentToolCallID: "parent-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "invalid payload", out.Error)
	require.Nil(t, out.Payload)
	require.NotNil(t, out.RetryHint)
	require.Equal(t, planner.RetryReasonInvalidArguments, out.RetryHint.Reason)
}

func TestExecuteToolActivityPropagatesLabels(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		require.Equal(t, map[string]string{
			"aura.session.id": "sess-1",
			"kind":            "brief",
		}, call.Labels)
		return &planner.ToolResult{
			Name:   call.Name,
			Result: map[string]any{"ok": true},
		}, nil
	})}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	input := ToolInput{
		AgentID:    "agent",
		RunID:      "run",
		ToolName:   "tool",
		ToolCallID: "tool-1",
		Payload:    []byte("null"),
		Labels: map[string]string{
			"aura.session.id": "sess-1",
			"kind":            "brief",
		},
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.JSONEq(t, `{"ok":true}`, string(out.Payload))
}

func TestEnforceToolResultContractsRequiresExplicitBoundsForBoundedTool(t *testing.T) {
	rt := &Runtime{}
	spec := newAnyJSONSpec("tool", "svc.ts")
	spec.Bounds = &tools.BoundsSpec{
		Paging: &tools.PagingSpec{
			CursorField:     "cursor",
			NextCursorField: "next_cursor",
		},
	}
	call := planner.ToolRequest{Name: "tool", ToolCallID: "tool-1"}

	err := rt.enforceToolResultContracts(spec, call, &planner.ToolResult{
		Name:   call.Name,
		Result: map[string]any{"ok": true},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bounded tool")
	require.Contains(t, err.Error(), "without bounds")
}

func TestEnforceToolResultContractsAcceptsExplicitBoundsForBoundedTool(t *testing.T) {
	rt := &Runtime{}
	spec := newAnyJSONSpec("tool", "svc.ts")
	spec.Bounds = &tools.BoundsSpec{
		Paging: &tools.PagingSpec{
			CursorField:     "cursor",
			NextCursorField: "next_cursor",
		},
	}
	call := planner.ToolRequest{Name: "tool", ToolCallID: "tool-1"}

	err := rt.enforceToolResultContracts(spec, call, &planner.ToolResult{
		Name:   call.Name,
		Result: map[string]any{"ok": true},
		Bounds: &agent.Bounds{
			Returned:  1,
			Truncated: false,
		},
	})
	require.NoError(t, err)
}

func TestEnforceToolResultContractsRejectsTruncatedBoundsWithoutContinuation(t *testing.T) {
	rt := &Runtime{}
	spec := newAnyJSONSpec("tool", "svc.ts")
	spec.Bounds = &tools.BoundsSpec{
		Paging: &tools.PagingSpec{
			CursorField:     "cursor",
			NextCursorField: "next_cursor",
		},
	}
	call := planner.ToolRequest{Name: "tool", ToolCallID: "tool-1"}

	err := rt.enforceToolResultContracts(spec, call, &planner.ToolResult{
		Name:   call.Name,
		Result: map[string]any{"ok": true},
		Bounds: &agent.Bounds{
			Returned:  1,
			Truncated: true,
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "truncated result without next_cursor or refinement_hint")
}

func TestEnforceToolResultContractsRejectsNilToolResultWithoutCriticalPrefix(t *testing.T) {
	rt := &Runtime{}
	spec := newAnyJSONSpec("tool", "svc.ts")
	call := planner.ToolRequest{Name: "tool", ToolCallID: "tool-1"}

	err := rt.enforceToolResultContracts(spec, call, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil tool result")
	require.NotContains(t, err.Error(), "CRITICAL:")
}

func TestExecuteToolActivityPropagatesServerData(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result:     map[string]any{"ok": true},
			ServerData: rawjson.Message([]byte(`[{"kind":"example.evidence","data":[{"uri":"example://points/123","kind":"time_series"}]}]`)),
		}, nil
	})}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.JSONEq(t, `[{"kind":"example.evidence","data":[{"uri":"example://points/123","kind":"time_series"}]}]`, string(out.ServerData))
}

func TestExecuteToolActivityRunsResultMaterializer(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"ok": true,
				},
			}, nil
		}),
		ResultMaterializer: func(ctx context.Context, meta ToolCallMeta, call *planner.ToolRequest, result *planner.ToolResult) error {
			require.Equal(t, "tool-1", meta.ToolCallID)
			require.JSONEq(t, `{"input":"ok"}`, string(call.Payload))
			result.ServerData = rawjson.Message([]byte(`[{"kind":"example.materialized","data":{"source":"runtime"}}]`))
			return nil
		},
	}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newAnyJSONSpec("tool", "svc.ts"),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", Payload: []byte(`{"input":"ok"}`)}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.JSONEq(t, `[{"kind":"example.materialized","data":{"source":"runtime"}}]`, string(out.ServerData))
}

func TestExecuteToolActivityPropagatesBounds(t *testing.T) {
	total := 9
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result:     map[string]any{"ok": true},
			Bounds: &agent.Bounds{
				Returned:  7,
				Total:     &total,
				Truncated: false,
			},
		}, nil
	})}}}
	spec := newAnyJSONSpec("tool", "svc.ts")
	spec.Bounds = &tools.BoundsSpec{}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): spec,
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Bounds)
	require.Equal(t, 7, out.Bounds.Returned)
	require.NotNil(t, out.Bounds.Total)
	require.Equal(t, 9, *out.Bounds.Total)
	require.False(t, out.Bounds.Truncated)
}

func TestEncodeCanonicalToolResultProjectsBoundsIntoEncodedResult(t *testing.T) {
	total := 9
	cursor := "next-page"
	result, err := EncodeCanonicalToolResult(newProjectedResultSpec(), &projectedRuntimeResult{
		Results: []string{"alpha"},
	}, &agent.Bounds{
		Returned:       1,
		Total:          &total,
		Truncated:      true,
		NextCursor:     &cursor,
		RefinementHint: "narrow by source",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"results": ["alpha"],
		"returned": 1,
		"total": 9,
		"truncated": true,
		"next_cursor": "next-page",
		"refinement_hint": "narrow by source"
	}`, string(result))
}

func TestEncodeCanonicalToolResultRejectsRawJSONResult(t *testing.T) {
	_, err := EncodeCanonicalToolResult(newProjectedResultSpec(), json.RawMessage(`{"results":["alpha"]}`), &agent.Bounds{
		Returned:  1,
		Truncated: false,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "typed Go value")
}

func TestEncodeCanonicalToolResultRejectsRuntimeRawJSONResult(t *testing.T) {
	_, err := EncodeCanonicalToolResult(newProjectedResultSpec(), rawjson.Message(`{"results":["alpha"]}`), &agent.Bounds{
		Returned:  1,
		Truncated: false,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "typed Go value")
}

func TestExecuteToolActivityProjectsBoundsIntoEncodedResult(t *testing.T) {
	total := 9
	cursor := "next-page"
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result: &projectedRuntimeResult{
				Results: []string{"alpha"},
			},
			Bounds: &agent.Bounds{
				Returned:       1,
				Total:          &total,
				Truncated:      true,
				NextCursor:     &cursor,
				RefinementHint: "narrow by source",
			},
		}, nil
	})}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newProjectedResultSpec(),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.JSONEq(t, `{
		"results": ["alpha"],
		"returned": 1,
		"total": 9,
		"truncated": true,
		"next_cursor": "next-page",
		"refinement_hint": "narrow by source"
	}`, string(out.Payload))
}

func TestExecuteToolActivityDropsStaleOptionalBoundFieldsFromSemanticResult(t *testing.T) {
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result: map[string]any{
				"results":         []string{"alpha"},
				"total":           99,
				"next_cursor":     "stale",
				"refinement_hint": "stale hint",
			},
			Bounds: &agent.Bounds{
				Returned:  1,
				Truncated: false,
			},
		}, nil
	})}}}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{
		tools.Ident("tool"): newProjectedResultSpec(),
	}
	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: "tool", ToolCallID: "tool-1", Payload: []byte("null")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.JSONEq(t, `{
		"results": ["alpha"],
		"returned": 1,
		"truncated": false
	}`, string(out.Payload))
}

func TestPublishToolResultReceivedProjectsBoundsIntoResultPreview(t *testing.T) {
	toolName := tools.Ident("svc.tools.projected_preview")
	rthints.RegisterResultHint(toolName, template.Must(template.New("preview").Parse(
		`{{ .Args.Query }} / {{ index .Result.Results 0 }} / {{ .Bounds.Returned }} / {{ .Bounds.Total }}`,
	)))

	recorder := &recordingHooks{}
	exec := &toolBatchExec{
		r: &Runtime{
			Bus:           recorder,
			RunEventStore: runloginmem.New(),
			toolSpecs: map[tools.Ident]tools.ToolSpec{
				toolName: newProjectedResultSpec(),
			},
		},
		runID:     "run-1",
		agentID:   "agent-1",
		sessionID: "session-1",
		turnID:    "turn-1",
	}
	total := 9
	call := planner.ToolRequest{
		Name:       toolName,
		ToolCallID: "tool-1",
		Payload:    rawjson.Message(`{"query":"status"}`),
	}
	tr := &planner.ToolResult{
		Name:       toolName,
		ToolCallID: "tool-1",
		Result: &projectedRuntimeResult{
			Results: []string{"alpha"},
		},
		Bounds: &agent.Bounds{
			Returned:  1,
			Total:     &total,
			Truncated: true,
		},
	}

	err := exec.publishToolResultReceived(context.Background(), call, tr, nil, 0)
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)

	resultEvt, ok := recorder.events[0].(*hooks.ToolResultReceivedEvent)
	require.True(t, ok)
	require.Equal(t, "status / alpha / 1 / 9", resultEvt.ResultPreview)
}

func TestRegisterToolset_RejectsAgentToolsetWithoutSpecs(t *testing.T) {
	rt := New()
	reg := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: "svc.agent",
		Name:    "svc.tools",
		Route: AgentRoute{
			ID:               "svc.agent",
			WorkflowName:     "wf",
			DefaultTaskQueue: "default",
		},
	})

	err := rt.RegisterToolset(reg)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Contains(t, err.Error(), "requires tool specs")
}

func TestToolsetTaskQueueOverrideUsed(t *testing.T) {
	childSpec := newAnyJSONSpec("child", "svc.export")
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.export": {TaskQueue: "q1", Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		return &planner.ToolResult{
			Name: call.Name,
		}, nil
	})}}, Bus: noopHooks{}}
	seedTestToolSpecs(rt, childSpec)
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("child")}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, time.Time{}, 2, "", nil, nil, 0)
	require.NoError(t, err)
	require.Equal(t, "q1", wfCtx.lastToolCall.Options.Queue)
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, "svc.export", wfCtx.lastToolCall.Input.ToolsetName)
}

func TestPreserveModelProvidedToolCallID(t *testing.T) {
	toolSpec := newAnyJSONSpec("tool", "svc.ts")
	rt := &Runtime{toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		// Ensure the ID provided by the planner/model flows into the executor unchanged
		require.Equal(t, "model-123", call.ToolCallID)
		return &planner.ToolResult{Name: call.Name}, nil
	})}}, Bus: noopHooks{}}
	seedTestToolSpecs(rt, toolSpec)
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}, planResult: &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, hasPlanResult: true}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	// Planner supplies an explicit ToolCallID from the model
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("tool"), ToolCallID: "model-123"}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, time.Time{}, 2, "", nil, nil, 0)
	require.NoError(t, err)
	// Activity input should carry the same ID
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, "model-123", wfCtx.lastToolCall.Input.ToolCallID)
}

func TestActivityToolExecutorExecute(t *testing.T) {
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("123")}}
	exec := &ActivityToolExecutor{activityName: "execute"}
	input := ToolInput{RunID: "r1"}
	out, err := exec.Execute(context.Background(), wfCtx, &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, []byte("123"), []byte(out.Payload))
}

func TestRunLoopPauseResumeEmitsEvents(t *testing.T) {
	recorder := &recordingHooks{}
	toolSpec := newAnyJSONSpec("tool", "svc.ts")
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		SessionStore:  sessioninmem.New(),
		toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name,
			}, nil
		})}},
	}
	seedTestToolSpecs(rt, toolSpec)
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null")},
		barrier:     make(chan struct{}, 1),
	}
	wfCtx.ensureSignals()
	// Allow tests to enqueue pause/resume before async completes
	go func() {
		time.Sleep(5 * time.Millisecond)
		wfCtx.pauseCh <- &api.PauseRequest{RunID: "run-1", Reason: "human"}
		wfCtx.resumeCh <- &api.ResumeRequest{RunID: "run-1", Notes: "resume"}
		wfCtx.barrier <- struct{}{}
	}()
	wfCtx.hasPlanResult = true
	wfCtx.planResult = &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	_, err := rt.CreateSession(context.Background(), input.SessionID)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, rt.SessionStore.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   string(input.AgentID),
		RunID:     input.RunID,
		SessionID: input.SessionID,
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}))
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tools.Ident("tool")}}}
	ctrl := interrupt.NewController(wfCtx)
	_, err = rt.runLoop(wfCtx, AgentRegistration{
		ID:                  input.AgentID,
		Planner:             &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, nil, model.TokenUsage{}, policy.CapsState{MaxToolCalls: 1, RemainingToolCalls: 1}, time.Time{}, time.Time{}, 2, input.TurnID, nil, ctrl, 0)
	require.NoError(t, err)
	var sawPause, sawResume bool
	for _, evt := range recorder.events {
		switch evt.(type) {
		case *hooks.RunPausedEvent:
			sawPause = true
		case *hooks.RunResumedEvent:
			sawResume = true
		}
	}
	require.True(t, sawPause)
	require.True(t, sawResume)
}

func TestServiceToolEventsUseChildRunContext(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.fetch_time_series"): newAnyJSONSpec("svc.tools.fetch_time_series", "svc.tools"),
		},
	}
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null")},
	}
	parentCtx := &run.Context{
		RunID:            "child-run",
		SessionID:        "session-1",
		TurnID:           "turn-1",
		ParentRunID:      "parent-run",
		ParentAgentID:    "parent.agent",
		ParentToolCallID: "tool-parent",
	}
	calls := []planner.ToolRequest{{
		Name:       tools.Ident("svc.tools.fetch_time_series"),
		ToolCallID: "child-call",
	}}
	_, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, "child.agent", parentCtx, nil, calls, 0, nil, time.Time{})
	require.NoError(t, err)

	var scheduled *hooks.ToolCallScheduledEvent
	var resultEvt *hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		switch e := evt.(type) {
		case *hooks.ToolCallScheduledEvent:
			scheduled = e
		case *hooks.ToolResultReceivedEvent:
			resultEvt = e
		}
	}
	require.NotNil(t, scheduled, "expected ToolCallScheduledEvent")
	require.Equal(t, "child-run", scheduled.RunID())
	require.Equal(t, "child.agent", scheduled.AgentID())
	require.Equal(t, "tool-parent", scheduled.ParentToolCallID)

	require.NotNil(t, resultEvt, "expected ToolResultReceivedEvent")
	require.Equal(t, "child-run", resultEvt.RunID())
	require.Equal(t, "child.agent", resultEvt.AgentID())
	require.Equal(t, "tool-parent", resultEvt.ParentToolCallID)
}

func TestServiceToolEventsPropagateServerData(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.example"): newAnyJSONSpec("svc.tools.example", "svc.tools"),
		},
	}
	server := rawjson.Message([]byte(`[{"kind":"example.evidence","data":[{"uri":"example://points/123","kind":"time_series"}]}]`))
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null"), ServerData: server},
	}
	parentCtx := &run.Context{
		RunID:            "child-run",
		SessionID:        "session-1",
		TurnID:           "turn-1",
		ParentRunID:      "parent-run",
		ParentAgentID:    "parent.agent",
		ParentToolCallID: "tool-parent",
	}
	calls := []planner.ToolRequest{{
		Name:       tools.Ident("svc.tools.example"),
		ToolCallID: "child-call",
	}}
	_, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, "child.agent", parentCtx, nil, calls, 0, nil, time.Time{})
	require.NoError(t, err)

	var resultEvt *hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			resultEvt = e
		}
	}
	require.NotNil(t, resultEvt, "expected ToolResultReceivedEvent")
	require.JSONEq(t, string(server), string(resultEvt.ServerData))
}

func TestConsumeProvidedToolResultsRunsResultMaterializer(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {
				ResultMaterializer: func(ctx context.Context, meta ToolCallMeta, call *planner.ToolRequest, result *planner.ToolResult) error {
					require.Equal(t, "tool-call-1", meta.ToolCallID)
					require.JSONEq(t, `{"input":"ok"}`, string(call.Payload))
					result.Result = map[string]any{
						"ok":           true,
						"materialized": true,
					}
					result.ServerData = rawjson.Message([]byte(`[{"kind":"example.materialized","data":{"source":"await"}}]`))
					return nil
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.example"): newAnyJSONSpec("svc.tools.example", "svc.tools"),
		},
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "session-1",
			TurnID:    "turn-1",
		},
	}
	state := newRunLoopState(nil, nil, model.TokenUsage{}, policy.CapsState{}, 1)
	allowed := []planner.ToolRequest{
		{
			Name:       tools.Ident("svc.tools.example"),
			ToolCallID: "tool-call-1",
			Payload:    rawjson.Message([]byte(`{"input":"ok"}`)),
		},
	}
	results, err := rt.consumeProvidedToolResults(
		context.Background(),
		&RunInput{
			AgentID:   "agent",
			RunID:     "run-1",
			SessionID: "session-1",
			TurnID:    "turn-1",
		},
		base,
		state,
		"turn-1",
		&api.ToolResultsSet{
			RunID: "run-1",
			ID:    "await-1",
			Results: []*api.ProvidedToolResult{
				{
					Name:       tools.Ident("svc.tools.example"),
					ToolCallID: "tool-call-1",
					Result:     rawjson.Message([]byte(`{"ok":true}`)),
				},
			},
		},
		allowed,
		map[string]struct{}{"tool-call-1": {}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.JSONEq(t, `[{"kind":"example.materialized","data":{"source":"await"}}]`, string(results[0].ServerData))

	var resultEvt *hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			resultEvt = e
		}
	}
	require.NotNil(t, resultEvt, "expected ToolResultReceivedEvent")
	require.JSONEq(t, `{"ok":true,"materialized":true}`, string(resultEvt.ResultJSON))
	require.JSONEq(t, `[{"kind":"example.materialized","data":{"source":"await"}}]`, string(resultEvt.ServerData))
}

func TestConsumeProvidedToolResultsRejectsAmbiguousErrorAndResult(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.example"): newAnyJSONSpec("svc.tools.example", "svc.tools"),
		},
	}
	call := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.example"),
		ToolCallID: "tool-call-1",
	}
	_, _, err := rt.decodeProvidedToolResult(context.Background(), rt.toolSpecs[call.Name], call, &api.ProvidedToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     rawjson.Message([]byte(`{"ok":true}`)),
		Error:      planner.NewToolError("failed"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "error and result are both set")
}

func TestEnforceToolResultContractsRejectsAmbiguousErrorAndResult(t *testing.T) {
	rt := &Runtime{}
	call := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.example"),
		ToolCallID: "tool-call-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"ok": true},
		Error:      planner.NewToolError("failed"),
	}

	err := rt.enforceToolResultContracts(newAnyJSONSpec(call.Name, "svc.tools"), call, tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error and result are both set")
}

func TestConsumeProvidedToolResultsRejectsTruncatedBoundsWithoutContinuation(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.example"): {
				Name:    tools.Ident("svc.tools.example"),
				Toolset: "svc.tools",
				Payload: newAnyJSONSpec("svc.tools.example", "svc.tools").Payload,
				Result:  newAnyJSONSpec("svc.tools.example", "svc.tools").Result,
				Bounds: &tools.BoundsSpec{
					Paging: &tools.PagingSpec{
						CursorField:     "cursor",
						NextCursorField: "next_cursor",
					},
				},
			},
		},
	}
	call := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.example"),
		ToolCallID: "tool-call-1",
	}
	_, _, err := rt.decodeProvidedToolResult(context.Background(), rt.toolSpecs[call.Name], call, &api.ProvidedToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     rawjson.Message([]byte(`{"ok":true}`)),
		Bounds: &agent.Bounds{
			Returned:  1,
			Truncated: true,
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "truncated result without next_cursor or refinement_hint")
}

func TestServiceToolEventsPropagateBounds(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.example"): {
				Name:    tools.Ident("svc.tools.example"),
				Toolset: "svc.tools",
				Payload: tools.TypeSpec{},
				Result:  tools.TypeSpec{},
				Bounds:  &tools.BoundsSpec{},
			},
		},
	}
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{
			Payload: []byte("null"),
			Bounds: &agent.Bounds{
				Returned:  1,
				Truncated: false,
			},
		},
	}
	parentCtx := &run.Context{
		RunID:            "child-run",
		SessionID:        "session-1",
		TurnID:           "turn-1",
		ParentRunID:      "parent-run",
		ParentAgentID:    "parent.agent",
		ParentToolCallID: "tool-parent",
	}
	calls := []planner.ToolRequest{{
		Name:       tools.Ident("svc.tools.example"),
		ToolCallID: "child-call",
	}}
	results, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, "child.agent", parentCtx, nil, calls, 0, nil, time.Time{})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].ToolResult)
	require.NotNil(t, results[0].ToolResult.Bounds)
	require.Equal(t, 1, results[0].ToolResult.Bounds.Returned)
	require.False(t, results[0].ToolResult.Bounds.Truncated)

	var resultEvt *hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			resultEvt = e
		}
	}
	require.NotNil(t, resultEvt, "expected ToolResultReceivedEvent")
	require.NotNil(t, resultEvt.Bounds)
	require.Equal(t, 1, resultEvt.Bounds.Returned)
	require.False(t, resultEvt.Bounds.Truncated)
}

func TestInlineToolsetEmitsParentToolEvents(t *testing.T) {
	recorder := &recordingHooks{}
	inlineSpec := newAnyJSONSpec("child.get_time_series", "child.tools")
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}
	rt.toolsets = map[string]ToolsetRegistration{
		"child.tools": {
			Inline: true,
			Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
				require.NotNil(t, call)
				require.Equal(t, tools.Ident("child.get_time_series"), call.Name)
				require.Equal(t, "tool-parent", call.ToolCallID)
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Result:     map[string]any{"ok": true},
				}, nil
			}),
		},
	}
	seedTestToolSpecs(rt, inlineSpec)
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		},
		hasPlanResult: true,
	}
	input := &RunInput{AgentID: "parent.agent", RunID: "run-inline", SessionID: "sess-1", TurnID: "turn-1"}
	base := &planner.PlanInput{
		RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID},
		Agent:      newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID}),
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:       tools.Ident("child.get_time_series"),
		ToolCallID: "tool-parent",
	}}}
	_, err := rt.runLoop(
		wfCtx,
		AgentRegistration{
			ID:                  input.AgentID,
			Planner:             &stubPlanner{},
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxToolCalls: 2, RemainingToolCalls: 2},
		time.Time{},
		time.Time{},
		2,
		input.TurnID,
		nil,
		nil,
		0,
	)
	require.NoError(t, err)

	var scheduled *hooks.ToolCallScheduledEvent
	var resultEvt *hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		switch e := evt.(type) {
		case *hooks.ToolCallScheduledEvent:
			if e.ToolCallID == "tool-parent" {
				scheduled = e
			}
		case *hooks.ToolResultReceivedEvent:
			if e.ToolCallID == "tool-parent" {
				resultEvt = e
			}
		}
	}
	require.NotNil(t, scheduled, "expected ToolCallScheduledEvent for inline tool")
	require.Equal(t, input.RunID, scheduled.RunID())
	require.Equal(t, tools.Ident("child.get_time_series"), scheduled.ToolName)
	require.Empty(t, scheduled.ParentToolCallID)

	require.NotNil(t, resultEvt, "expected ToolResultReceivedEvent for inline tool")
	require.Equal(t, input.RunID, resultEvt.RunID())
	require.Equal(t, tools.Ident("child.get_time_series"), resultEvt.ToolName)
	require.JSONEq(t, `{"ok":true}`, string(resultEvt.ResultJSON))
	require.Empty(t, resultEvt.ParentToolCallID)
}
