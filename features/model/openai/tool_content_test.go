// These tests inspect the SDK request's correlated function output rather than
// treating a base64 string in ordinary text as native media support.
package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

func TestToolContentUsesNativeCorrelatedFunctionOutput(t *testing.T) {
	t.Parallel()
	item, err := encodeToolResultMessage(model.ToolResultPart{
		ToolUseID: "call-1",
		Content:   "rejected",
		IsError:   true,
		Blocks: content.Blocks{
			&content.TextContent{Text: "detail", Meta: json.RawMessage(`{"secret":"host-value"}`)},
			&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
			&content.ResourceLink{Name: "guide", URI: "private://guide"},
			&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "private://pdf", MIMEType: new("application/pdf"), Blob: "AQI="}},
		},
	}, 1, 0)
	require.NoError(t, err)
	result := item.OfFunctionCallOutput
	require.NotNil(t, result)
	assert.Equal(t, "call-1", result.CallID.Value)
	blocks := result.Output.OfResponseFunctionCallOutputItemArray
	require.Len(t, blocks, 6)
	assert.JSONEq(t, `{"is_error":true,"error":"rejected"}`, blocks[0].OfInputText.Text)
	assert.Equal(t, "detail", blocks[1].OfInputText.Text)
	assert.Equal(t, "data:image/png;base64,AQI=", blocks[2].OfInputImage.ImageURL.Value)
	assert.Contains(t, blocks[3].OfInputText.Text, "private://guide")
	assert.Contains(t, blocks[4].OfInputText.Text, "private://pdf")
	assert.Equal(t, "data:application/pdf;base64,AQI=", blocks[5].OfInputFile.FileData.Value)
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "host-value")
	assert.Contains(t, string(encoded), `"type":"input_image"`)
	assert.Contains(t, string(encoded), `"type":"input_file"`)
}

func TestToolContentRejectsAudioBeforeProviderDispatch(t *testing.T) {
	t.Parallel()
	_, err := encodeToolResultMessage(model.ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{
		&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"},
	}}, 1, 0)
	assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
}

func TestToolContentRetainsSpreadsheetAsNativeFile(t *testing.T) {
	t.Parallel()
	item, err := encodeToolResultMessage(model.ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "doc://sheet", MIMEType: new("text/csv"), Blob: "eCx5Cg=="}},
	}}, 1, 0)
	require.NoError(t, err)
	file := item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray[2].OfInputFile
	assert.Equal(t, "data:text/csv;base64,eCx5Cg==", file.FileData.Value)
	assert.Equal(t, "resource-0.csv", file.Filename.Value)
}
