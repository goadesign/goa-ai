package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestPlanActivityOutputUnmarshalJSON(t *testing.T) {
	t.Run("canonical transcript", func(t *testing.T) {
		const payload = `{
			"Result": null,
			"Transcript": [{
				"role": "assistant",
				"meta": {"trace": "abc"},
				"parts": [
					{"kind": "text", "text": "hi there"},
					{"kind": "tool_use", "id": "tool-call-1", "name": "search", "input": {"z":9007199254740993,"a":1}},
					{"kind": "tool_result", "tool_use_id": "tool-call-1", "content": {"items": 1}, "is_error": false}
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
			require.Equal(t, `{"z":9007199254740993,"a":1}`, string(tu.Input))
		} else {
			t.Fatalf("unexpected part[1]: %#v", msg.Parts[1])
		}

		if tr, ok := msg.Parts[2].(model.ToolResultPart); ok {
			require.Equal(t, "tool-call-1", tr.ToolUseID)
			require.False(t, tr.IsError)
			require.Equal(t, map[string]any{"items": json.Number("1")}, tr.Content)
		} else {
			t.Fatalf("unexpected part[2]: %#v", msg.Parts[2])
		}
	})

	t.Run("missing kind", func(t *testing.T) {
		const invalid = `{
			"Result": null,
			"Transcript": [{
				"role": "assistant",
				"parts": [
					{"id": "tool-call-1", "name": "search", "input": {"q": "status"}}
				]
			}]
		}`

		var out PlanActivityOutput
		require.ErrorContains(t, json.Unmarshal([]byte(invalid), &out), "message part requires kind")
	})
}
