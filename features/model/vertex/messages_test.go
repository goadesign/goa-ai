package vertex

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
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

func TestEncodeContentsRejectsUnsupportedSystemPart(t *testing.T) {
	_, _, err := encodeContents([]*model.Message{{
		Role:  model.ConversationRoleSystem,
		Parts: []model.Part{model.CacheCheckpointPart{}},
	}}, nil)
	require.EqualError(t, err, "vertex: unsupported system message part model.CacheCheckpointPart")
}

func TestEncodeContentsPreservesDocumentContent(t *testing.T) {
	msgs := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.DocumentPart{
			Name:   "manual",
			Format: model.DocumentFormatPDF,
			Bytes:  []byte("pdf"),
		}},
	}}

	_, contents, err := encodeContents(msgs, nil)
	require.NoError(t, err)
	require.Equal(t, "application/pdf", contents[0].Parts[0].InlineData.MIMEType)
	require.Equal(t, []byte("pdf"), contents[0].Parts[0].InlineData.Data)
}

func TestEncodeContentsRejectsUnsupportedDocumentMetadata(t *testing.T) {
	msgs := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.DocumentPart{
			Name: "manual",
			Text: "content",
			Cite: true,
		}},
	}}

	_, _, err := encodeContents(msgs, nil)
	require.ErrorContains(t, err, `document "manual" citation configuration is not supported`)
}

func TestEncodeContentsToolLoop(t *testing.T) {
	canonToProv := map[string]string{"feed/find_duplicates": "feed_find_duplicates"}
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: rawjson.Message(`{"title":"picnic"}`)},
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
	_, _, err := encodeContents(msgs, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tool result "orphan" has no matching tool use`)
}

func TestEncodeContentsReplaysHistoricalToolUseUnchanged(t *testing.T) {
	system, contents, err := encodeContents([]*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "call-1",
				Name:  "removed.tool",
				Input: rawjson.Message(`{"reading":9007199254740993}`),
			}},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{model.ToolResultPart{
				ToolUseID: "call-1",
				Content:   map[string]any{"error": "unknown tool"},
				IsError:   true,
			}},
		},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, system)
	require.Len(t, contents, 2)
	call := contents[0].Parts[0].FunctionCall
	require.NotNil(t, call)
	assert.Equal(t, "removed.tool", call.Name)
	assert.Equal(t, json.Number("9007199254740993"), call.Args["reading"])
	result := contents[1].Parts[0].FunctionResponse
	require.NotNil(t, result)
	assert.Equal(t, "removed.tool", result.Name)
}

func TestToResponseMapPreservesLargeIntegers(t *testing.T) {
	response, err := toResponseMap(struct {
		Reading json.Number `json:"reading"`
	}{
		Reading: json.Number("9007199254740993"),
	}, false)
	require.NoError(t, err)
	assert.Equal(t, json.Number("9007199254740993"), response["reading"])
}

func TestEncodeContentsToolResultErrorDoesNotMutateContent(t *testing.T) {
	content := map[string]any{"detail": "boom"}
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: rawjson.Message(`{}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: content, IsError: true},
		}},
	}
	_, contents, err := encodeContents(msgs, map[string]string{"feed/find_duplicates": "feed_find_duplicates"})
	require.NoError(t, err)
	require.Len(t, contents, 2)
	fr := contents[1].Parts[0].FunctionResponse
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

func TestEncodeContentsRejectsRedactedThinking(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
			model.TextPart{Text: "answer"},
		}},
	}
	_, _, err := encodeContents(msgs, nil)
	require.EqualError(t, err, "vertex: redacted thinking is not supported")
}

func TestEncodeContentsThinkingSignatureInvalidBase64(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "not*base64!", Final: true},
		}},
	}
	_, _, err := encodeContents(msgs, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex: encode thinking part: signature is not valid base64")
}

func TestEncodeContentsToolUseThoughtSignatureRoundTrips(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{
				ID:               "c1",
				Name:             "feed/find_duplicates",
				Input:            rawjson.Message(`{"title":"picnic"}`),
				ThoughtSignature: "c2ln", // base64("sig")
			},
		}},
	}
	_, contents, err := encodeContents(msgs, map[string]string{"feed/find_duplicates": "feed_find_duplicates"})
	require.NoError(t, err)
	require.Len(t, contents, 1)
	fc := contents[0].Parts[0]
	require.NotNil(t, fc.FunctionCall)
	assert.Equal(t, []byte("sig"), fc.ThoughtSignature)
}

func TestEncodeContentsToolUseThoughtSignatureInvalidBase64(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{
				ID:               "c1",
				Name:             "feed/find_duplicates",
				Input:            rawjson.Message(`{}`),
				ThoughtSignature: "not*base64!",
			},
		}},
	}
	_, _, err := encodeContents(msgs, map[string]string{"feed/find_duplicates": "feed_find_duplicates"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `vertex: encode tool use "feed/find_duplicates": thought signature is not valid base64`)
}

func TestEncodeContentsToolUseNonObjectInputErrors(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: rawjson.Message(`"not-an-object"`)},
		}},
	}
	_, _, err := encodeContents(msgs, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `vertex: encode tool use "feed/find_duplicates": tool input must be a JSON object`)
}

func TestEncodeContentsToolUseMissingInputErrors(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: nil},
		}},
	}
	_, _, err := encodeContents(msgs, nil)
	require.ErrorContains(t, err, `vertex: encode tool use "feed/find_duplicates": tool input must be a JSON object`)
}

func TestEncodeContentsToolResultNilContentError(t *testing.T) {
	// Gemini requires an object, so nil is represented explicitly under output.
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: rawjson.Message(`{}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: nil, IsError: true},
		}},
	}
	_, contents, err := encodeContents(msgs, map[string]string{"feed/find_duplicates": "feed_find_duplicates"})
	require.NoError(t, err)
	fr := contents[1].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, map[string]any{"output": nil, "error": true}, fr.Response)
}

func TestEncodeContentsToolResultRejectsEmptyRawContent(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "c1", Name: "feed/find_duplicates", Input: rawjson.Message(`{}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "c1", Content: rawjson.Message{}},
		}},
	}
	_, _, err := encodeContents(msgs, map[string]string{"feed/find_duplicates": "feed_find_duplicates"})
	require.ErrorContains(t, err, "rawjson: non-nil message is empty")
}
