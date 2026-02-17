package runtime

import (
	"encoding/json"
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

	require.NoError(t, rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led))

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
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("atlas.commands.change_setpoint"): {
				Name: tools.Ident("atlas.commands.change_setpoint"),
				Result: tools.TypeSpec{
					Codec: tools.JSONCodec[any]{
						ToJSON: json.Marshal,
					},
				},
			},
		},
	}
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

	require.NoError(t, rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led))

	require.Len(t, base.Messages, 1)
	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, part.IsError)

	content, ok := part.Content.(map[string]any)
	require.True(t, ok)
	rawResult, ok := content["result"].(json.RawMessage)
	require.True(t, ok)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rawResult, &decoded))
	require.Equal(t, map[string]any{"ok": false}, decoded)

	errVal, ok := content["error"].(*planner.ToolError)
	require.True(t, ok)
	require.Equal(t, "permission denied", errVal.Message)
}

func TestAppendUserToolResults_AppendsBoundsReminderAfterToolResults(t *testing.T) {
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("atlas.read.list_devices"): {
				Name: tools.Ident("atlas.read.list_devices"),
				Result: tools.TypeSpec{
					Codec: tools.JSONCodec[any]{
						ToJSON: json.Marshal,
					},
				},
			},
		},
	}
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

	require.NoError(t, rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led))

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

	require.NoError(t, rt.appendUserToolResults(base, []planner.ToolRequest{call}, []*planner.ToolResult{tr}, led))

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call failed and provided a RetryHint.")
	require.Contains(t, txt.Text, "Tool: atlas.read.atlas_aggregate")
}
