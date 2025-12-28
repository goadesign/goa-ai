package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestAppendUserToolResults_IncludesErrorInToolResultContent(t *testing.T) {
	rt := &Runtime{}
	base := &planner.PlanInput{}
	led := transcript.NewLedger()

	call := planner.ToolRequest{
		Name:       tools.Ident("atlas.commands.change_setpoint"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      planner.NewToolError("access denied: missing controlleddevices.write privilege"),
	}

	rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led, nil)

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 1)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, part.IsError)

	content, ok := part.Content.(map[string]any)
	require.True(t, ok)

	_, hasResult := content["result"]
	require.False(t, hasResult)

	errVal, ok := content["error"].(*planner.ToolError)
	require.True(t, ok)
	require.Equal(t, "access denied: missing controlleddevices.write privilege", errVal.Message)
}

func TestAppendUserToolResults_IncludesResultAndErrorWhenBothPresent(t *testing.T) {
	rt := &Runtime{}
	base := &planner.PlanInput{}
	led := transcript.NewLedger()

	call := planner.ToolRequest{
		Name:       tools.Ident("atlas.commands.change_setpoint"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"ok": false},
		Error:      planner.NewToolError("permission denied"),
	}

	rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led, nil)

	require.Len(t, base.Messages, 1)
	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, part.IsError)

	content, ok := part.Content.(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"ok": false}, content["result"])

	errVal, ok := content["error"].(*planner.ToolError)
	require.True(t, ok)
	require.Equal(t, "permission denied", errVal.Message)
}

func TestAppendUserToolResults_AppendsBoundsReminderAfterToolResults(t *testing.T) {
	rt := &Runtime{}
	base := &planner.PlanInput{}
	led := transcript.NewLedger()

	call := planner.ToolRequest{
		Name:       tools.Ident("atlas.read.list_devices"),
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

	rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led, nil)

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call returned a bounded/truncated result.")
	require.Contains(t, txt.Text, "Next cursor: opaque-cursor")
}

func TestAppendUserToolResults_AppendsRetryHintReminderAfterToolResults(t *testing.T) {
	rt := &Runtime{}
	base := &planner.PlanInput{}
	led := transcript.NewLedger()

	call := planner.ToolRequest{
		Name:       tools.Ident("atlas.read.atlas_aggregate"),
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
			ExampleInput: map[string]any{
				"dataset": "alarms",
			},
		},
	}

	rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led, nil)

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call failed and provided a RetryHint.")
	require.Contains(t, txt.Text, "Tool: atlas.read.atlas_aggregate")
}
