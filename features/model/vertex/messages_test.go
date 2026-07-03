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
	assert.Equal(t, "c1", fc.ID)
	assert.Equal(t, "feed_find_duplicates", fc.Name)
	assert.Equal(t, "picnic", fc.Args["title"])
	fr := contents[1].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, "c1", fr.ID)
	assert.Equal(t, "feed_find_duplicates", fr.Name) // recovered from the tool use
	assert.Contains(t, fr.Response, "output")        // non-object content wrapped
}

func TestEncodeContentsToolResultWithoutToolUse(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "orphan", Content: map[string]any{"ok": true}},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	fr := contents[0].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, "orphan", fr.ID)
	assert.Equal(t, "orphan", fr.Name) // no matching tool use: Name falls back to the ID
}

func TestEncodeContentsToolResultErrorDoesNotMutateContent(t *testing.T) {
	content := map[string]any{"detail": "boom"}
	msgs := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: content, IsError: true},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	fr := contents[0].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, true, fr.Response["error"])
	assert.NotContains(t, content, "error") // caller-owned map must stay untouched
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
	assert.Equal(t, []byte("sig"), parts[0].ThoughtSignature) // "c2ln" is base64("sig")
}

func TestEncodeContentsToolResultOrphanNameSanitized(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "feed/find dup!", Content: map[string]any{"ok": true}},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	fr := contents[0].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, sanitizeToolName("feed/find dup!"), fr.Name)
	assert.NotContains(t, fr.Name, "/")
	assert.NotContains(t, fr.Name, " ")
}

func TestEncodeContentsRedactedOnlyThinkingSkipped(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
			model.TextPart{Text: "answer"},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	// The redacted-only thinking part is dropped entirely, leaving only the
	// text part.
	require.Len(t, contents[0].Parts, 1)
	assert.Equal(t, "answer", contents[0].Parts[0].Text)
}

func TestEncodeContentsThinkingSignatureInvalidBase64(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "not*base64!", Final: true},
		}},
	}
	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	part := contents[0].Parts[0]
	require.True(t, part.Thought)
	assert.Equal(t, []byte("not*base64!"), part.ThoughtSignature) // raw-bytes fallback
}
