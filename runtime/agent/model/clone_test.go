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
		Meta: map[string]any{
			"nested": map[string]any{"values": []any{"meta"}},
		},
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

func TestCloneMessagesRejectsStructMetadata(t *testing.T) {
	_, err := CloneMessages([]*Message{{
		Meta: map[string]any{"value": struct{ Text string }{Text: "evidence"}},
	}})

	require.ErrorContains(t, err, "not JSON-compatible metadata")
}

func TestRejectedResponseEvidenceRetainsBoundedStructMetadata(t *testing.T) {
	type evidence struct {
		Text string
	}
	response := responseWithMetadata(evidence{Text: "rejected"})
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)

	owned, err := contract.ValidateResponse(response)

	require.ErrorContains(t, err, "not JSON-compatible metadata")
	require.Nil(t, owned)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	rejectedResponse, cloneErr := validationErr.RejectedResponse()
	require.NoError(t, cloneErr)
	rejected := rejectedResponse.Content[0].Meta["value"].(evidence)
	require.Equal(t, "rejected", rejected.Text)
}

func TestOwnResponsePreservesDistinctMessageOriginsAcrossClones(t *testing.T) {
	owned, err := ownResponse(&Response{
		Content: []Message{
			{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "same"}}},
			{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "same"}}},
		},
	})
	require.NoError(t, err)
	cloned, err := CloneResponse(owned)
	require.NoError(t, err)

	assert.False(t, SameMessageOrigin(&owned.Content[0], &owned.Content[1]))
	assert.True(t, SameMessageOrigin(&owned.Content[0], &cloned.Content[0]))
	assert.True(t, SameMessageOrigin(&owned.Content[1], &cloned.Content[1]))
	assert.False(t, SameMessageOrigin(&Message{}, &Message{}))
}

func TestCloneRejectsPointerPartAndChunkVariants(t *testing.T) {
	part, err := clonePart(&TextPart{Text: "pointer"})
	require.Nil(t, part)
	require.EqualError(t, err, "unsupported message part type *model.TextPart")

	chunk, err := cloneChunk(&TextChunk{})
	require.Nil(t, chunk)
	require.EqualError(t, err, "model: clone chunk: unsupported chunk type *model.TextChunk")
}

func TestOwnResponsePreservesFingerprintSignificantEmptySlices(t *testing.T) {
	response := &Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{
				CitationsPart{Citations: []Citation{{SourceContent: []string{}}}},
				ThinkingPart{Redacted: []byte{}},
				ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message{}},
			},
			Meta: map[string]any{"empty": []string{}},
		}},
		StopReason: "tool_use",
	}
	before, err := fingerprintResponse(response)
	require.NoError(t, err)

	owned, err := ownResponse(response)
	require.NoError(t, err)
	after, err := fingerprintResponse(owned)
	require.NoError(t, err)

	require.Equal(t, before, after)
	require.NotNil(t, owned.Content)
	require.NotNil(t, owned.Content[0].Parts)
	require.NotNil(t, owned.Content[0].Parts[0].(CitationsPart).Citations[0].SourceContent)
	require.NotNil(t, owned.Content[0].Parts[1].(ThinkingPart).Redacted)
	require.NotNil(t, owned.Content[0].Parts[2].(ToolUsePart).Input)
	require.NotNil(t, owned.Content[0].Meta["empty"].([]string))
}

func TestOwnResponseRejectsUnsupportedBinaryPartsBeforePayloadCopy(t *testing.T) {
	large := make([]byte, maxDynamicValueBytes+1)
	for _, part := range []Part{
		ImagePart{Format: ImageFormatPNG, Bytes: large},
		DocumentPart{Name: "large", Format: DocumentFormatPDF, Bytes: large},
	} {
		response := &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{part},
			}},
			StopReason: "end_turn",
		}

		owned, err := ownResponse(response)

		require.Nil(t, owned)
		require.ErrorContains(t, err, "unsupported assistant response part")
		assert.NotContains(t, err.Error(), "maximum byte size")
	}
}
