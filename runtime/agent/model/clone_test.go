// clone_test.go verifies canonical message copies isolate every mutable field
// that crosses planner and workflow ownership boundaries.
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestCloneMessagesIsolatesMutableCanonicalContent(t *testing.T) {
	const changed = "changed"

	location := &DocumentPageLocation{DocumentIndex: 1, Start: 2, End: 3}
	original := []*Message{{
		Role: ConversationRoleUser,
		Parts: []Part{
			ImagePart{Format: ImageFormatPNG, Bytes: []byte("image")},
			DocumentPart{Name: "doc", Format: DocumentFormatPDF, Bytes: []byte("document"), Chunks: []string{"chunk"}},
			CitationsPart{
				Text: "cited",
				Citations: []Citation{{
					SourceContent: []string{"source"},
					Location:      CitationLocation{DocumentPage: location},
				}},
			},
			ThinkingPart{Redacted: []byte("redacted"), Final: true},
			ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{"value":1}`)},
			ToolResultPart{
				ToolUseID: "call-1",
				Content:   map[string]any{"nested": []any{"result"}},
			},
		},
		Meta: map[string]any{"nested": map[string]any{"values": []any{"meta"}}},
	}}

	cloned, err := CloneMessages(original)
	require.NoError(t, err)

	cloned[0].Parts[0].(ImagePart).Bytes[0] = 'I'
	cloned[0].Parts[1].(DocumentPart).Bytes[0] = 'D'
	cloned[0].Parts[1].(DocumentPart).Chunks[0] = changed
	cloned[0].Parts[2].(CitationsPart).Citations[0].SourceContent[0] = changed
	cloned[0].Parts[2].(CitationsPart).Citations[0].Location.DocumentPage.Start = 9
	cloned[0].Parts[3].(ThinkingPart).Redacted[0] = 'R'
	cloned[0].Parts[4].(ToolUsePart).Input[9] = '2'
	cloned[0].Parts[5].(ToolResultPart).Content.(map[string]any)["nested"].([]any)[0] = changed
	cloned[0].Meta["nested"].(map[string]any)["values"].([]any)[0] = changed

	assert.Equal(t, []byte("image"), original[0].Parts[0].(ImagePart).Bytes)
	assert.Equal(t, []byte("document"), original[0].Parts[1].(DocumentPart).Bytes)
	assert.Equal(t, []string{"chunk"}, original[0].Parts[1].(DocumentPart).Chunks)
	assert.Equal(t, []string{"source"}, original[0].Parts[2].(CitationsPart).Citations[0].SourceContent)
	assert.Equal(t, 2, original[0].Parts[2].(CitationsPart).Citations[0].Location.DocumentPage.Start)
	assert.Equal(t, []byte("redacted"), original[0].Parts[3].(ThinkingPart).Redacted)
	assert.JSONEq(t, `{"value":1}`, string(original[0].Parts[4].(ToolUsePart).Input))
	assert.Equal(t, "result", original[0].Parts[5].(ToolResultPart).Content.(map[string]any)["nested"].([]any)[0])
	assert.Equal(t, "meta", original[0].Meta["nested"].(map[string]any)["values"].([]any)[0])
}
