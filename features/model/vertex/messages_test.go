package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeContentsSystemAndRoles(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hello"}}},
	}
	system, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	require.NotNil(t, system)
	assert.Equal(t, "be terse", system.Parts[0].Text)
	require.Len(t, contents, 2)
	assert.Equal(t, "user", contents[0].Role)
	assert.Equal(t, "model", contents[1].Role)
}

func TestEncodeContentsToolLoop(t *testing.T) {
	canonToProv := map[string]string{"feed/find_duplicates": "feed_find_duplicates"}
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: map[string]any{"title": "picnic"}},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: []any{"m1"}},
		}},
	}
	_, contents, err := encodeContents(msgs, canonToProv)
	require.NoError(t, err)
	require.Len(t, contents, 2)
	fc := contents[0].Parts[0].FunctionCall
	require.NotNil(t, fc)
	assert.Equal(t, "feed_find_duplicates", fc.Name)
	assert.Equal(t, "picnic", fc.Args["title"])
	fr := contents[1].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Contains(t, fr.Response, "output") // non-object content wrapped
}

func TestEncodeContentsThinkingEcho(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "c2ln", Final: true},
			model.TextPart{Text: "answer"},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	parts := contents[0].Parts
	require.Len(t, parts, 2)
	assert.True(t, parts[0].Thought)
	assert.NotEmpty(t, parts[0].ThoughtSignature)
}
