package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
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

func appendRetryableBookkeepingToolUsesForTest(t *testing.T, rt *Runtime, agentID agent.Ident, base *planner.PlanInput, calls []planner.ToolRequest, results []*planner.ToolResult) {
	t.Helper()
	appendRetryableBookkeepingToolUsesWithSignaturesForTest(t, rt, agentID, base, calls, results, nil)
}

func appendRetryableBookkeepingToolUsesWithSignaturesForTest(t *testing.T, rt *Runtime, agentID agent.Ident, base *planner.PlanInput, calls []planner.ToolRequest, results []*planner.ToolResult, signatures map[string]string) {
	t.Helper()
	records := stepToolRecordsForTest(t, calls, results)
	require.NoError(t, rt.appendRetryableBookkeepingToolUses(t.Context(), agentID, base, records, signatures, ""))
}

func stepToolRecordsForTest(t *testing.T, calls []planner.ToolRequest, results []*planner.ToolResult) []stepToolRecord {
	t.Helper()
	records, err := stepToolRecordsFromCallsAndResults("test step tool records", calls, results)
	require.NoError(t, err)
	return records
}

func TestRecordAssistantTurnRebuildsToolUsesFromPlannerFacingCalls(t *testing.T) {
	rt := New()
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.tools.read", "svc.tools"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("tasks.progress.update", "tasks.progress")
			spec.Bookkeeping = true
			return spec
		}(),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	transcriptMsgs := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.TextPart{Text: "Working."},
			model.ToolUsePart{ID: "provider-budgeted", Name: "svc.tools.read", Input: rawjson.Message(`{"q":"old"}`)},
			model.ToolUsePart{ID: "provider-bookkeeping", Name: "tasks.progress.update", Input: rawjson.Message(`{"ok":true}`)},
		},
	}}
	calls := []planner.ToolRequest{
		{Name: "svc.tools.read", ToolCallID: "runtime-budgeted", Payload: rawjson.Message(`{"q":"new"}`)},
		{Name: "tasks.progress.update", ToolCallID: "runtime-bookkeeping", Payload: rawjson.Message(`{"ok":true}`)},
	}

	require.NoError(t, rt.recordAssistantTurn(t.Context(), agentID, base, transcriptMsgs, calls, nil, "turn-1"))

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 2)
	text, ok := base.Messages[0].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, "Working.", text.Text)
	use, ok := base.Messages[0].Parts[1].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "runtime-budgeted", use.ID)
	require.Equal(t, "svc.tools.read", use.Name)
	require.Equal(t, rawjson.Message(`{"q":"new"}`), use.Input)
}

// TestRecordAssistantTurnAttachesToolCallSignatureByID is the lookup-side
// counterpart to signature capture: recordAssistantTurn must attach a
// captured thought signature to the rebuilt ToolUsePart by ToolCallID lookup,
// never by reading a signature field off planner.ToolRequest (which has none).
func TestRecordAssistantTurnAttachesToolCallSignatureByID(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.read", "svc.tools"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	calls := []planner.ToolRequest{
		{
			Name:       "svc.tools.read",
			ToolCallID: "runtime-1",
			Payload:    rawjson.Message(`{"q":"new"}`),
		},
	}
	signatures := map[string]string{"runtime-1": "opaque-provider-signature"}

	require.NoError(t, rt.recordAssistantTurn(t.Context(), agentID, base, nil, calls, signatures, "turn-1"))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
}

// TestRecordAssistantTurnLeavesSignatureEmptyWhenUncaptured verifies the
// "missing key means absent" contract: a tool call with no entry in the
// signatures map (or a nil map) must record an empty ThoughtSignature rather
// than panicking or fabricating a value.
func TestRecordAssistantTurnLeavesSignatureEmptyWhenUncaptured(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.read", "svc.tools"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	calls := []planner.ToolRequest{
		{
			Name:       "svc.tools.read",
			ToolCallID: "runtime-1",
			Payload:    rawjson.Message(`{"q":"new"}`),
		},
	}

	require.NoError(t, rt.recordAssistantTurn(t.Context(), agentID, base, nil, calls, nil, "turn-1"))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Empty(t, use.ThoughtSignature)
}

func TestRecordAssistantTurnUsesModelFacingToolIdentityForCompiledCalls(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("atlas.read.get_time_series", "atlas.read"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	calls := []planner.ToolRequest{{
		Name:         "atlas.read.get_time_series",
		Payload:      rawjson.Message(`{"mode":"chart","sources":[{"id":"source-1"}]}`),
		ModelName:    "fetch_chart_signal_series",
		ModelPayload: rawjson.Message(`{"from":"2026-06-12T00:00:00Z","to":"2026-06-13T00:00:00Z"}`),
		ToolCallID:   "tooluse_1",
	}}

	require.NoError(t, rt.recordAssistantTurn(t.Context(), agentID, base, nil, calls, nil, "turn-1"))

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "tooluse_1", use.ID)
	require.Equal(t, "fetch_chart_signal_series", use.Name)
	require.Equal(t, rawjson.Message(`{"from":"2026-06-12T00:00:00Z","to":"2026-06-13T00:00:00Z"}`), use.Input)
}

func TestRecordAssistantTurnDropsBookkeepingOnlyProviderToolUse(t *testing.T) {
	rt := New()
	bookkeeping := newAnyJSONSpec("tasks.progress.update", "tasks.progress")
	bookkeeping.Bookkeeping = true
	seedTestToolSpecs(rt, bookkeeping)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	transcriptMsgs := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ToolUsePart{ID: "provider-bookkeeping", Name: "tasks.progress.update", Input: rawjson.Message(`{"ok":true}`)},
		},
	}}
	calls := []planner.ToolRequest{{
		Name:       "tasks.progress.update",
		ToolCallID: "runtime-bookkeeping",
		Payload:    rawjson.Message(`{"ok":true}`),
	}}

	require.NoError(t, rt.recordAssistantTurn(t.Context(), agentID, base, transcriptMsgs, calls, nil, "turn-1"))

	require.Empty(t, base.Messages)
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
			replayed := transcript.BuildMessagesFromEvents([]memory.Event{
				memory.NewEvent(time.Now(), memory.AssistantMessageData{
					Message: "calling tool",
				}, nil),
				memory.NewEvent(time.Now(), memory.ToolCallData{
					ToolCallID:  call.ToolCallID,
					ToolName:    call.Name,
					PayloadJSON: rawjson.Message(`{"x":1}`),
				}, nil),
				memory.NewEvent(time.Now(), memory.ToolResultData{
					ToolCallID:   call.ToolCallID,
					ToolName:     call.Name,
					ResultJSON:   rawjson.Message(resultJSON),
					Preview:      preview,
					Bounds:       tc.tr.Bounds,
					ErrorMessage: errorMessage,
				}, nil),
			})
			require.Len(t, replayed, 2)

			replayPart, ok := replayed[1].Parts[0].(model.ToolResultPart)
			require.True(t, ok)
			require.Equal(t, livePart.IsError, replayPart.IsError)
			require.Equal(t, livePart.Content, replayPart.Content)
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
}

func TestAppendUserToolResults_SkipsBookkeepingResults(t *testing.T) {
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
	require.Len(t, base.Messages[0].Parts, 1)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", part.ToolUseID)
}

// TestAppendRetryableBookkeepingToolUsesAttachesToolCallSignatureByID is the
// lookup-side counterpart to signature capture for the retryable-bookkeeping
// path: the rebuilt ToolUsePart must get its signature from an ID lookup
// against the captured signatures map, not from planner.ToolRequest.
func TestAppendRetryableBookkeepingToolUsesAttachesToolCallSignatureByID(t *testing.T) {
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
	signatures := map[string]string{"call-1": "opaque-provider-signature"}

	appendRetryableBookkeepingToolUsesWithSignaturesForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, signatures)

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
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

	appendRetryableBookkeepingToolUsesForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})
	appendUserToolResultsForTest(t, rt, agentID, base, []planner.ToolRequest{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, base.Messages[1].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[2].Role)

	usePart, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", usePart.ID)

	resultPart, ok := base.Messages[1].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", resultPart.ToolUseID)
	require.True(t, resultPart.IsError)
}
