package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func appendUserToolResultsForTest(t *testing.T, rt *Runtime, agentID agent.Ident, base *planner.PlanInput, calls []planner.ToolRequest, results []*planner.ToolResult) {
	t.Helper()
	records := stepToolRecordsForTest(t, calls, results)
	require.NoError(t, rt.appendUserToolRecordResults(t.Context(), agentID, base, records, ""))
}

func stepToolRecordsForTest(t *testing.T, calls []planner.ToolRequest, results []*planner.ToolResult) []stepToolRecord {
	t.Helper()
	records, err := stepToolRecordsFromCallsAndResults("test step tool records", calls, results)
	require.NoError(t, err)
	return records
}

// stepToolRecordsFromCallsAndResults pairs test fixtures by canonical tool-call
// identity so tests can exercise runtime record consumers directly.
func stepToolRecordsFromCallsAndResults(context string, calls []planner.ToolRequest, results []*planner.ToolResult) ([]stepToolRecord, error) {
	if len(calls) == 0 && len(results) == 0 {
		return nil, nil
	}
	if len(calls) != len(results) {
		return nil, fmt.Errorf("%s: calls/results length mismatch (%d != %d)", context, len(calls), len(results))
	}

	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, fmt.Errorf("%s: nil tool result", context)
		}
		if result.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing result tool_call_id for %s", context, result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, fmt.Errorf("%s: duplicate result tool_call_id %s", context, result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}

	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing call tool_call_id for %s", context, call.Name)
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("%s: missing result for tool_call_id %s", context, call.ToolCallID)
		}
		if result.Name != "" && result.Name != call.Name {
			return nil, fmt.Errorf("%s: result name %s does not match call %s", context, result.Name, call.Name)
		}
		records = append(records, stepToolRecord{
			call:   call,
			result: result,
		})
	}
	return records, nil
}

func TestStepToolRecordsFromExecutionsRestoresCanonicalCallOrder(t *testing.T) {
	calls := []planner.ToolRequest{
		{Name: "svc.first", ToolCallID: "call-1"},
		{Name: "svc.second", ToolCallID: "call-2"},
		{Name: "svc.third", ToolCallID: "call-3"},
	}
	outcomes := []*ToolExecutionResult{
		{ToolResult: &planner.ToolResult{Name: "svc.first", ToolCallID: "call-1"}},
		{ToolResult: &planner.ToolResult{Name: "svc.third", ToolCallID: "call-3"}},
		{ToolResult: &planner.ToolResult{Name: "svc.second", ToolCallID: "call-2"}},
	}

	records, err := stepToolRecordsFromExecutions(calls, outcomes)
	require.NoError(t, err)
	require.Equal(t, "call-1", records[0].result.ToolCallID)
	require.Equal(t, "call-2", records[1].result.ToolCallID)
	require.Equal(t, "call-3", records[2].result.ToolCallID)
}

func TestCommitSelectedModelResponsePreservesCanonicalParts(t *testing.T) {
	rt := New()
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	transcript := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
			model.CitationsPart{Text: "answer", Citations: []model.Citation{{Title: "source"}}},
			model.ToolUsePart{
				ID:               "call-1",
				Name:             "svc.lookup",
				Input:            rawjson.Message(`{"q":"status"}`),
				ThoughtSignature: "tool-sig",
			},
		},
	}}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(),
		agentID,
		base,
		"turn-1",
		&planner.PlanResult{},
		transcript,
	))

	require.Equal(t, transcript, base.Messages)
}

func TestCommitSelectedModelResponseBuildsPlannerAuthoredModelIdentity(t *testing.T) {
	rt := New()
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:         "atlas.read.get_time_series",
		Payload:      rawjson.Message(`{"mode":"chart"}`),
		ModelName:    "fetch_chart_signal_series",
		ModelPayload: rawjson.Message(`{"from":"2026-06-12T00:00:00Z"}`),
		ToolCallID:   "tooluse_1",
	}}}

	require.NoError(t, rt.appendSelectedModelResponse(t.Context(), agentID, base, "turn-1", result, nil))

	require.Len(t, base.Messages, 1)
	require.Equal(t, []model.Part{model.ToolUsePart{
		ID:    "tooluse_1",
		Name:  "fetch_chart_signal_series",
		Input: rawjson.Message(`{"from":"2026-06-12T00:00:00Z"}`),
	}}, base.Messages[0].Parts)
}

func TestAppendUserToolResults_IncludesErrorInToolResultContent(t *testing.T) {
	rt := New()
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := planner.ToolRequest{
		Name:       tools.Ident("svc.commands.adjust_setpoint"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      planner.NewToolError("access denied: missing controlleddevices.write privilege"),
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 1)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, part.IsError)
	require.Equal(t, "access denied: missing controlleddevices.write privilege", part.Content)
}

func TestAppendUserToolResults_DecodesSuccessfulResultContent(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.commands.adjust_setpoint"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := planner.ToolRequest{
		Name:       tools.Ident("svc.commands.adjust_setpoint"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result: map[string]any{
			"ok": false,
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 1)
	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.False(t, part.IsError)
	content, ok := part.Content.(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"ok": false}, content)
}

func TestAppendUserToolResults_MatchesReplayProjection(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.commands.adjust_setpoint"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	agentID := agent.Ident("agent-1")
	call := planner.ToolRequest{
		Name:       tools.Ident("svc.commands.adjust_setpoint"),
		ToolCallID: "tc-1",
	}

	cases := []struct {
		name string
		tr   *planner.ToolResult
	}{
		{
			name: "success",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"ok": true,
				},
			},
		},
		{
			name: "error",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Error:      planner.NewToolError("permission denied"),
			},
		},
		{
			name: "omitted",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"blob": strings.Repeat("x", transcript.MaxToolResultContentBytes+1024),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}

			appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tc.tr})
			require.Len(t, base.Messages, 1)

			livePart, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
			require.True(t, ok)

			resultJSON := ""
			if tc.tr.Result != nil {
				raw, err := rt.marshalToolValue(t.Context(), tc.tr.Name, tc.tr.Result, tc.tr.Bounds)
				require.NoError(t, err)
				resultJSON = string(raw)
			}
			errorMessage := ""
			if tc.tr.Error != nil {
				errorMessage = tc.tr.Error.Error()
			}
			preview, err := formatToolResultPreviewForCall(t.Context(), rt, &call, tc.tr)
			require.NoError(t, err)
			replayContent, err := transcript.ProjectToolResultContent(
				rawjson.Message(resultJSON),
				tc.tr.Bounds,
				preview,
				errorMessage,
			)
			require.NoError(t, err)
			require.Equal(t, livePart.Content, replayContent)
		})
	}
}

func TestAppendUserToolResults_AppendsBoundsReminderAfterToolResults(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.read.list_devices"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := planner.ToolRequest{
		Name:       tools.Ident("svc.read.list_devices"),
		ToolCallID: "tc-1",
	}
	cursor := "opaque-cursor"
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"devices": []any{}},
		Bounds: &agent.Bounds{
			Returned:   10,
			Total:      func() *int { v := 42; return &v }(),
			Truncated:  true,
			NextCursor: &cursor,
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call returned a bounded/truncated result.")
	require.Contains(t, txt.Text, "Next cursor: opaque-cursor")
}

func TestAppendUserToolResults_AppendsRetryHintReminderAfterToolResults(t *testing.T) {
	rt := New()
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := planner.ToolRequest{
		Name:       tools.Ident("svc.read.aggregate"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      planner.NewToolError("invalid_argument"),
		RetryHint: &planner.RetryHint{
			Reason:         planner.RetryReasonInvalidArguments,
			Tool:           call.Name,
			RestrictToTool: true,
			Message:        "Unsupported filter field.",
			ExampleJSON:    rawjson.Message(`{"dataset":"alarms"}`),
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call failed and provided a RetryHint.")
	require.Contains(t, txt.Text, "Tool: svc.read.aggregate")
	require.Contains(t, txt.Text, "Restriction: retry the corrected call to svc.read.aggregate.")
	require.Contains(t, txt.Text, "finish the run through its normal completion path")
}

func TestAppendUserToolResultsPreservesBookkeepingResults(t *testing.T) {
	rt := New()
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.tools.read", "svc.tools"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.set_step_status", "tasks.progress")
			spec.Bookkeeping = true
			return spec
		}(),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	calls := []planner.ToolRequest{
		{Name: "svc.tools.read", ToolCallID: "call-1"},
		{Name: "tasks.progress.set_step_status", ToolCallID: "call-2"},
	}
	results := []*planner.ToolResult{
		{
			Name:       "svc.tools.read",
			ToolCallID: "call-1",
			Result:     map[string]any{"value": 1},
		},
		{
			Name:       "tasks.progress.set_step_status",
			ToolCallID: "call-2",
			Result:     map[string]any{"ok": true},
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, calls, results)
	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 2)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", part.ToolUseID)
	bookkeeping, ok := base.Messages[0].Parts[1].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-2", bookkeeping.ToolUseID)
}

func TestAppendUserToolResults_ReplaysRetryableBookkeepingFailures(t *testing.T) {
	rt := New()
	seedTestToolSpecs(
		rt,
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.complete", "tasks.progress")
			spec.Bookkeeping = true
			spec.TerminalRun = true
			return spec
		}(),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := planner.ToolRequest{
		Name:       "tasks.progress.complete",
		ToolCallID: "call-1",
		Payload:    rawjson.Message(`{"title":"Final brief"}`),
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      planner.NewToolError("brief.summary length must be <= 600"),
		RetryHint: &planner.RetryHint{
			Reason:             planner.RetryReasonInvalidArguments,
			Tool:               call.Name,
			ClarifyingQuestion: "Please resend tasks.progress.complete with a payload that satisfies: brief.summary length must be <= 600.",
		},
	}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(),
		agentID,
		base,
		"",
		&planner.PlanResult{ToolCalls: []planner.ToolRequest{call}},
		[]*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:               "call-1",
				Name:             string(call.Name),
				Input:            call.Payload,
				ThoughtSignature: "opaque-provider-signature",
			}},
		}},
	))
	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, base.Messages[1].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[2].Role)

	usePart, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", usePart.ID)
	require.Equal(t, "opaque-provider-signature", usePart.ThoughtSignature)

	resultPart, ok := base.Messages[1].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", resultPart.ToolUseID)
	require.True(t, resultPart.IsError)
}
