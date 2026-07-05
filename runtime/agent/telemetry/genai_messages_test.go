package telemetry_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestGenAIInputMessagesAttrEmpty(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIInputMessagesAttr(nil)

	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, attribute.KeyValue{}, attr)
}

func TestGenAIOutputMessagesAttrEmpty(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIOutputMessagesAttr(nil, "stop")

	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, attribute.KeyValue{}, attr)
}

func TestGenAIInputMessagesAttrSerializesOrderedTranscript(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIInputMessagesAttr([]*model.Message{
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "find reports"},
			},
		},
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call-1",
					Name:  "reports.search",
					Input: rawjson.Message(`{"query":"status","limit":2}`),
				},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{
					ToolUseID: "call-1",
					Content:   rawjson.Message(`{"hits":[{"id":"r1"}]}`),
				},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, telemetry.AttrGenAIInputMessages, attr.Key)
	require.JSONEq(t, `[
		{
			"role": "user",
			"parts": [
				{"type": "text", "content": "find reports"}
			]
		},
		{
			"role": "assistant",
			"parts": [
				{
					"type": "tool_call",
					"id": "call-1",
					"name": "reports.search",
					"arguments": {"query": "status", "limit": 2}
				}
			]
		},
		{
			"role": "user",
			"parts": [
				{
					"type": "tool_call_response",
					"id": "call-1",
					"response": {"hits": [{"id": "r1"}]}
				}
			]
		}
	]`, attr.Value.AsString())
}

func TestGenAIOutputMessagesAttrIncludesFinishReason(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIOutputMessagesAttr([]model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: "done"},
			},
		},
	}, "end_turn")

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, telemetry.AttrGenAIOutputMessages, attr.Key)
	require.JSONEq(t, `[
		{
			"role": "assistant",
			"finish_reason": "end_turn",
			"parts": [
				{"type": "text", "content": "done"}
			]
		}
	]`, attr.Value.AsString())
}

func TestGenAIMessageAttrsExcludeReasoning(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIOutputMessagesAttr([]model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "thinking in plaintext"},
				model.ThinkingPart{Redacted: []byte{0x01, 0x02}, Signature: "signed", Final: true},
				model.TextPart{Text: "done"},
			},
		},
	}, "stop")

	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `[
		{
			"role": "assistant",
			"finish_reason": "stop",
			"parts": [
				{"type": "text", "content": "done"}
			]
		}
	]`, attr.Value.AsString())
	require.NotContains(t, attr.Value.AsString(), "thinking in plaintext")
	require.NotContains(t, attr.Value.AsString(), "AQI=")
}

func TestGenAIOutputMessagesAttrOmitsEmptyFinishReason(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIOutputMessagesAttr([]model.Message{
		{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		},
	}, "")

	require.NoError(t, err)
	require.True(t, ok)
	require.NotContains(t, attr.Value.AsString(), "finish_reason")
}

func TestGenAIMessageAttrsSkipReasoningOnlyMessages(t *testing.T) {
	t.Parallel()

	_, ok, err := telemetry.GenAIOutputMessagesAttr([]model.Message{
		{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.ThinkingPart{Text: "only thinking"}},
		},
	}, "stop")

	require.NoError(t, err)
	require.False(t, ok)
}

func TestGenAIInputMessagesAttrEmbedsRawJSONValues(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIInputMessagesAttr([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					Name:  "reports.search",
					Input: rawjson.Message(`{"filters":{"state":"open"}}`),
				},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, ok)

	var messages []map[string]any
	require.NoError(t, json.Unmarshal([]byte(attr.Value.AsString()), &messages))
	parts := messages[0]["parts"].([]any)
	toolCall := parts[0].(map[string]any)
	require.Equal(t, map[string]any{"filters": map[string]any{"state": "open"}}, toolCall["arguments"])
	require.NotEqual(t, `{"filters":{"state":"open"}}`, toolCall["arguments"])
}

func TestGenAIInputMessagesAttrSerializesMultimodalFallbacks(t *testing.T) {
	t.Parallel()

	attr, ok, err := telemetry.GenAIInputMessagesAttr([]*model.Message{
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ImagePart{
					Format: model.ImageFormatPNG,
					Bytes:  []byte{0x01, 0x02},
				},
				model.DocumentPart{
					Name:   "spec",
					Format: model.DocumentFormatMD,
					Chunks: []string{"first", "second"},
					Cite:   true,
				},
				model.CacheCheckpointPart{},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `[
		{
			"role": "user",
			"parts": [
				{
					"type": "blob",
					"modality": "image",
					"mime_type": "image/png",
					"content": "AQI="
				},
				{
					"type": "text",
					"modality": "document",
					"name": "spec",
					"mime_type": "text/markdown",
					"content": "first\n\nsecond",
					"chunks": ["first", "second"],
					"cite": true
				},
				{"type": "cache_checkpoint"}
			]
		}
	]`, attr.Value.AsString())
}
