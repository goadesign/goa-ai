package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestPlanActivityOutput_UnmarshalJSON(t *testing.T) {
	t.Run("modern transcript", func(t *testing.T) {
		const payload = `{
			"Result": null,
			"Transcript": [{
				"Role": "assistant",
				"Meta": {"trace": "abc"},
				"Parts": [
					{"Text": "hi there"},
					{"Name": "search", "Input": {"q": "golang"}},
					{"ToolUseID": "tool-call-1", "Content": {"items": 1}, "IsError": false}
				]
			}]
		}`

		var out PlanActivityOutput
		require.NoError(t, json.Unmarshal([]byte(payload), &out))
		require.Len(t, out.Transcript, 1)

		msg := out.Transcript[0]
		require.Equal(t, model.ConversationRoleAssistant, msg.Role)
		require.Equal(t, map[string]any{"trace": "abc"}, msg.Meta)
		require.Len(t, msg.Parts, 3)

		if tp, ok := msg.Parts[0].(model.TextPart); ok {
			require.Equal(t, "hi there", tp.Text)
		} else {
			t.Fatalf("unexpected part[0]: %#v", msg.Parts[0])
		}

		if tu, ok := msg.Parts[1].(model.ToolUsePart); ok {
			require.Equal(t, "search", tu.Name)
			args, ok := tu.Input.(map[string]any)
			require.True(t, ok, "expected Input to be a map")
			require.Equal(t, map[string]any{"q": "golang"}, args)
		} else {
			t.Fatalf("unexpected part[1]: %#v", msg.Parts[1])
		}

		if tr, ok := msg.Parts[2].(model.ToolResultPart); ok {
			require.Equal(t, "tool-call-1", tr.ToolUseID)
			require.False(t, tr.IsError)
			require.Equal(t, map[string]any{"items": float64(1)}, tr.Content)
		} else {
			t.Fatalf("unexpected part[2]: %#v", msg.Parts[2])
		}
	})

	t.Run("legacy args field", func(t *testing.T) {
		const legacy = `{
			"Result": null,
			"Transcript": [{
				"Role": "assistant",
				"Parts": [
					{"Name": "legacy-tool", "Args": {"q": "old"}}
				]
			}]
		}`

		var out PlanActivityOutput
		require.NoError(t, json.Unmarshal([]byte(legacy), &out))
		require.Len(t, out.Transcript, 1)

		msg := out.Transcript[0]
		require.Len(t, msg.Parts, 1)

		tu, ok := msg.Parts[0].(model.ToolUsePart)
		require.True(t, ok, "expected first part to be ToolUsePart")
		require.Equal(t, map[string]any{"q": "old"}, tu.Input)
	})
}
