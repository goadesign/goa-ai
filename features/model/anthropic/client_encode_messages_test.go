package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestEncodeMessagesReplaysHistoricalToolUseUnchanged(t *testing.T) {
	messages, _, err := encodeMessages([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "atlas_read_count_events",
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
	require.Equal(t, "atlas_read_count_events", use.Name)
	input, err := json.Marshal(use.Input)
	require.NoError(t, err)
	require.JSONEq(t, `{"from":"2026-02-06T00:00:00Z"}`, string(input))
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
