// Public input and output share content values without losing media, resource
// descriptions, extension numbers, or the order chosen by the tool.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/content"
)

func TestPublicContentPreservesEveryRuntimeVariant(t *testing.T) {
	title, mime, theme, modified, description := "Guide", "text/plain", "dark", "2026-01-01T00:00:00Z", "Description"
	size, priority := 0.0, 0.5
	original := content.Blocks{
		&content.TextContent{Text: "", Annotations: &content.Annotations{Audience: []content.Role{content.RoleUser}, Priority: &priority, LastModified: &modified}, Meta: []byte(`{"integer":9007199254740993}`)},
		&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
		&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"},
		&content.ResourceLink{Name: "guide", URI: "https://example.test/guide", Title: &title, Description: &description, Meta: []byte(`{"id":9007199254740993123456789}`), Annotations: &content.Annotations{Audience: []content.Role{content.RoleAssistant}, Priority: &priority, LastModified: &modified}, MIMEType: &mime, Size: &size, Icons: []content.Icon{{Src: "https://example.test/icon", MIMEType: &mime, Sizes: []string{"any"}, Theme: &theme}}},
		&content.EmbeddedResource{Resource: &content.TextResourceContents{URI: "doc://guide", MIMEType: &mime, Text: "", Meta: []byte(`{"nested":true}`)}, Meta: []byte(`{"outer":true}`)},
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "blob://guide", Blob: ""}},
	}
	public, err := publicContentBlocks(original)
	require.NoError(t, err)
	restored, err := inputContentBlocks(public)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
	restored[0].(*content.TextContent).Meta[0] = 'x'
	assert.Equal(t, `{"integer":9007199254740993}`, string(original[0].(*content.TextContent).Meta))
	again, err := inputContentBlocks(public)
	require.NoError(t, err)
	assert.Equal(t, original, again)
}
