package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestEncodeMessagesProjectsHistoryOnlyToolName(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "atlas.read.count_events",
					Input: rawjson.Message(`{"from":"2026-02-06T00:00:00Z"}`),
				},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{
					ToolUseID: "tu1",
					Content:   map[string]any{"error": "unknown tool"},
					IsError:   true,
				},
			},
		},
	}, nil, false)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[0].Content, 1)
	use := messages[0].Content[0].OfToolUse
	require.NotNil(t, use)
	require.Equal(t, toolname.Sanitize("atlas.read.count_events"), use.Name)
	input, err := json.Marshal(use.Input)
	require.NoError(t, err)
	require.JSONEq(t, `{"from":"2026-02-06T00:00:00Z"}`, string(input))
}

func TestEncodeMessagesMapsToolUseIDsBijectively(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{ID: "run/turn/call", Name: "lookup", Input: rawjson.Message(`{}`)},
				model.ToolUsePart{ID: "t1", Name: "lookup", Input: rawjson.Message(`{}`)},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "run/turn/call", Content: "first"},
				model.ToolResultPart{ToolUseID: "t1", Content: "second"},
			},
		},
	}, nil, false)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[0].Content, 2)
	require.Len(t, messages[1].Content, 2)

	require.Equal(t, "t2", messages[0].Content[0].OfToolUse.ID)
	require.Equal(t, "t1", messages[0].Content[1].OfToolUse.ID)
	require.Equal(t, "t2", messages[1].Content[0].OfToolResult.ToolUseID)
	require.Equal(t, "t1", messages[1].Content[1].OfToolResult.ToolUseID)
}

func TestEncodeMessagesThinkingVariants(t *testing.T) {
	tests := []struct {
		name    string
		part    model.ThinkingPart
		wantErr string
	}{
		{
			name: "signed plaintext",
			part: model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
		},
		{
			name: "redacted",
			part: model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
		},
		{
			name:    "missing signature",
			part:    model.ThinkingPart{Text: "reasoning", Final: true},
			wantErr: "anthropic: thinking part must contain exactly signed content or redacted content",
		},
		{
			name: "mixed variants",
			part: model.ThinkingPart{
				Text:      "reasoning",
				Signature: "sig",
				Redacted:  []byte("opaque"),
				Final:     true,
			},
			wantErr: "anthropic: thinking part must contain exactly signed content or redacted content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := encodeMessages([]*model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{test.part},
			}}, nil, false)

			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
