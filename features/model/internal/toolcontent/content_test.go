// These tests distinguish content kept for hosts from content sent to a model.
// Native media stays media and resource addresses remain unvisited descriptions.
package toolcontent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

func TestPartsPreserveVisibleOrderWithoutExtensionMetadata(t *testing.T) {
	t.Parallel()
	blocks := content.Blocks{
		&content.TextContent{Text: "", Meta: json.RawMessage(`{"secret":"host-value"}`)},
		&content.AudioContent{Data: "", MIMEType: "audio/wav", Annotations: &content.Annotations{Audience: []content.Role{content.RoleUser}}},
		&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
		&content.ResourceLink{Name: "guide", URI: "private://guide", Title: new("Guide"), Meta: json.RawMessage(`{"secret":"host-value"}`)},
		&content.EmbeddedResource{Resource: &content.TextResourceContents{URI: "private://text", Text: "instructions", Meta: json.RawMessage(`{"secret":"host-value"}`)}},
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "private://pdf", MIMEType: new("application/pdf"), Blob: "AQI="}},
	}
	parts, err := Parts(blocks)
	require.NoError(t, err)
	require.Len(t, parts, 6)
	assert.Equal(t, model.TextPart{Text: ""}, parts[0])
	assert.Equal(t, model.ImagePart{Format: model.ImageFormatPNG, Bytes: []byte{1, 2}}, parts[1])
	assert.Contains(t, parts[2].(model.TextPart).Text, "private://guide")
	assert.Contains(t, parts[3].(model.TextPart).Text, "instructions")
	assert.Contains(t, parts[4].(model.TextPart).Text, "private://pdf")
	assert.Equal(t, model.DocumentPart{Name: "resource-5", Format: model.DocumentFormatPDF, Bytes: []byte{1, 2}}, parts[5])
	for _, part := range parts {
		if text, ok := part.(model.TextPart); ok {
			assert.NotContains(t, text.Text, "host-value")
			assert.NotContains(t, text.Text, "_meta")
		}
	}
	assert.JSONEq(t, `{"secret":"host-value"}`, string(blocks[3].(*content.ResourceLink).Meta))
}

func TestPartsRejectUnsupportedVisibleMedia(t *testing.T) {
	t.Parallel()
	for _, block := range []content.ContentBlock{
		&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"},
		&content.ImageContent{Data: "AQI=", MIMEType: "image/svg+xml"},
		&content.ImageContent{Data: "", MIMEType: "image/png"},
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "doc://blob", Blob: "AQI="}},
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "doc://blob", Blob: "AQI=", MIMEType: new("application/octet-stream")}},
	} {
		_, err := Parts(content.Blocks{block})
		assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
	}
}

func TestPartsHonorExplicitAssistantAudience(t *testing.T) {
	t.Parallel()
	for _, audience := range [][]content.Role{nil, {}, {content.RoleAssistant}, {content.RoleUser, content.RoleAssistant}} {
		parts, err := Parts(content.Blocks{&content.TextContent{Text: "visible", Annotations: &content.Annotations{Audience: audience}}})
		require.NoError(t, err)
		assert.Equal(t, []model.Part{model.TextPart{Text: "visible"}}, parts)
	}
	parts, err := Parts(content.Blocks{&content.TextContent{Text: "host only", Annotations: &content.Annotations{Audience: []content.Role{content.RoleUser}}}})
	require.NoError(t, err)
	assert.Empty(t, parts)
}

func TestPartsRetainEveryKnownDocumentFormat(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		mime   string
		format model.DocumentFormat
	}{
		{"application/pdf", model.DocumentFormatPDF},
		{"text/plain", model.DocumentFormatTXT},
		{"text/csv", model.DocumentFormatCSV},
		{"application/msword", model.DocumentFormatDOC},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", model.DocumentFormatDOCX},
		{"application/vnd.ms-excel", model.DocumentFormatXLS},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", model.DocumentFormatXLSX},
		{"text/html", model.DocumentFormatHTML},
		{"text/markdown", model.DocumentFormatMD},
	} {
		t.Run(test.mime, func(t *testing.T) {
			parts, err := Parts(content.Blocks{&content.EmbeddedResource{Resource: &content.BlobResourceContents{
				URI: "doc://resource", MIMEType: &test.mime, Blob: "eA==",
			}}})
			require.NoError(t, err)
			require.Len(t, parts, 2)
			assert.Equal(t, test.format, parts[1].(model.DocumentPart).Format)
			assert.Equal(t, []byte("x"), parts[1].(model.DocumentPart).Bytes)
		})
	}
}
