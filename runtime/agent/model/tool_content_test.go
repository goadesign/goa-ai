// These tests preserve typed content across history copies and JSON replay, and
// prove that media uses the same budget as the rest of one model request.
package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	content "goa.design/goa-ai/runtime/content"
)

func TestToolContentSurvivesMessageCopyAndJSONReplay(t *testing.T) {
	t.Parallel()
	blocks := content.Blocks{
		&content.TextContent{Text: "", Meta: json.RawMessage(`{"source":9007199254740993}`)},
		&content.ImageContent{MIMEType: "image/png", Data: "AQI="},
		&content.AudioContent{MIMEType: "audio/wav", Data: ""},
		&content.ResourceLink{Name: "guide", URI: "doc://guide", Icons: []content.Icon{{Src: "https://example.com/icon"}}},
		&content.EmbeddedResource{Resource: &content.TextResourceContents{URI: "doc://inline", Text: ""}},
	}
	original := []*Message{{Role: ConversationRoleUser, Parts: []Part{ToolResultPart{
		ToolUseID: "call-1",
		Content:   "rejected",
		Blocks:    blocks,
		IsError:   true,
	}}}}
	cloned, err := CloneMessages(original)
	require.NoError(t, err)
	encoded, err := json.Marshal(cloned[0])
	require.NoError(t, err)
	var restored Message
	require.NoError(t, json.Unmarshal(encoded, &restored))
	part := restored.Parts[0].(ToolResultPart)
	assert.Equal(t, "call-1", part.ToolUseID)
	assert.True(t, part.IsError)
	assert.Equal(t, blocks, part.Blocks)
	assert.Equal(t, "rejected", part.Content)
	assert.Contains(t, string(encoded), "9007199254740993")
	cloned[0].Parts[0].(ToolResultPart).Blocks[0].(*content.TextContent).Text = "changed"
	cloned[0].Parts[0].(ToolResultPart).Blocks[3].(*content.ResourceLink).Icons[0].Src = "changed"
	assert.Empty(t, blocks[0].(*content.TextContent).Text)
	assert.Equal(t, "https://example.com/icon", blocks[3].(*content.ResourceLink).Icons[0].Src)
}

func TestToolContentSharesCompleteRequestBudget(t *testing.T) {
	t.Parallel()
	half := strings.Repeat("x", maxDynamicValueBytes/2+1)
	request := &Request{Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
		TextPart{Text: half},
		ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{&content.TextContent{Text: half}}},
	}}}}
	_, err := cloneRequest(request)
	require.ErrorContains(t, err, "maximum byte size")
	// Two separate requests remain valid. One media item does not create a
	// lifetime budget shared by every request in an agent run.
	request.Messages[0].Parts = request.Messages[0].Parts[1:]
	for range 2 {
		_, err := cloneRequest(request)
		require.NoError(t, err)
	}
}

func TestToolContentRejectsMalformedCallerAndSavedValues(t *testing.T) {
	t.Parallel()
	for _, block := range []content.ContentBlock{
		nil,
		(*content.TextContent)(nil),
		&content.ImageContent{Data: "%%%", MIMEType: "image/png"},
		&content.EmbeddedResource{},
	} {
		_, err := CloneMessages([]*Message{{Parts: []Part{ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{block}}}}})
		require.Error(t, err)
	}
	var message Message
	err := json.Unmarshal([]byte(`{"role":"user","parts":[{"kind":"tool_result","tool_use_id":"call-1","content":null,"is_error":false,"blocks":[{"type":"image"}]}]}`), &message)
	assert.ErrorContains(t, err, "image content requires")
}

func TestToolContentParticipatesInCharacterEstimate(t *testing.T) {
	t.Parallel()
	plain := ToolResultPart{ToolUseID: "call-1", Content: "structured"}
	rich := plain
	rich.Blocks = content.Blocks{&content.TextContent{Text: "additional text"}}
	assert.Greater(t, partCharacterCount(rich), partCharacterCount(plain))
}
