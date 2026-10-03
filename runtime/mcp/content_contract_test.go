// This file verifies the MCP content decoder at the peer-response boundary.
// It checks malformed media, optional resource-link fields, and independent
// ownership of content copied into a tool error.
package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentDecoderRejectsMalformedPeerValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, content string }{
		{"invalid image", `{"type":"image","data":"%%%","mimeType":"image/png"}`},
		{"invalid audio", `{"type":"audio","data":"%%%","mimeType":"audio/wav"}`},
		{"invalid blob", `{"type":"resource","resource":{"uri":"doc://inline","blob":"%%%"}}`},
		{"relative link", `{"type":"resource_link","name":"guide","uri":"relative"}`},
		{"relative resource", `{"type":"resource","resource":{"uri":"relative","text":"hello"}}`},
		{"relative icon", `{"type":"resource_link","name":"guide","uri":"doc://guide","icons":[{"src":"relative"}]}`},
		{"missing icon uri", `{"type":"resource_link","name":"guide","uri":"doc://guide","icons":[{"theme":"dark"}]}`},
		{"invalid icon theme", `{"type":"resource_link","name":"guide","uri":"doc://guide","icons":[{"src":"https://example.org/icon","theme":"other"}]}`},
		{"invalid audience", `{"type":"text","text":"hello","annotations":{"audience":["system"]}}`},
		{"priority below zero", `{"type":"text","text":"hello","annotations":{"priority":-0.1}}`},
		{"priority above one", `{"type":"text","text":"hello","annotations":{"priority":1.1}}`},
		{"invalid metadata", `{"type":"text","text":"hello","_meta":[]}`},
		{"null metadata", `{"type":"text","text":"hello","_meta":null}`},
		{"ambiguous resource", `{"type":"resource","resource":{"uri":"doc://inline","text":"","blob":""}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result toolsCallResult
			require.NoError(t, json.Unmarshal([]byte(`{"resultType":"complete","content":[`+test.content+`]}`), &result))
			_, err := normalizeToolResult(result)
			var malformed *MalformedResponseError
			assert.ErrorAs(t, err, &malformed)
		})
	}
}

func TestContentDecoderPreservesLinkIconsAndNumericSize(t *testing.T) {
	t.Parallel()
	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{
  "resultType":"complete","content":[
   {"type":"resource_link","name":"guide","uri":"doc://guide","size":42.5,
    "icons":[{"src":"data:image/png;base64,AQI=","mimeType":"image/png","sizes":["any"],"theme":"dark"}],
    "_meta":{"example.org/source":{"id":1}},"annotations":{"priority":1}},
   {"type":"text","text":"","annotations":{"priority":1}},
   {"type":"image","data":"","mimeType":"image/png"},
   {"type":"audio","data":"","mimeType":"audio/wav"},
   {"type":"resource","resource":{"uri":"blob://inline","blob":""}}
  ]}`), &result))
	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.Len(t, response.Content, 5)
	link := response.Content[0].(*ResourceLink)
	assert.Equal(t, new(42.5), link.Size)
	require.Len(t, link.Icons, 1)
	assert.Equal(t, "data:image/png;base64,AQI=", link.Icons[0].Src)
	assert.Equal(t, "image/png", *link.Icons[0].MIMEType)
	assert.Equal(t, []string{"any"}, link.Icons[0].Sizes)
	assert.Equal(t, "dark", *link.Icons[0].Theme)
	assert.JSONEq(t, `{"example.org/source":{"id":1}}`, string(link.Meta))
	assert.Empty(t, response.Content[1].(*TextContent).Text)
	assert.Empty(t, response.Content[2].(*ImageContent).Data)
	assert.Empty(t, response.Content[3].(*AudioContent).Data)
	assert.Empty(t, response.Content[4].(*EmbeddedResource).Resource.(*BlobResourceContents).Blob)

	// Each item's priority is valid even when their sum exceeds one. The protocol
	// bounds an individual annotation, not the complete response's importance.
	assert.Equal(t, new(1.0), link.Annotations.Priority)
	assert.Equal(t, new(1.0), response.Content[1].(*TextContent).Annotations.Priority)

	failure := NewToolExecutionError(response)
	copied := failure.Response.Content[0].(*ResourceLink)
	*link.Size = 99
	*link.Icons[0].MIMEType = "changed"
	*link.Icons[0].Theme = "light"
	link.Icons[0].Sizes[0] = "changed"
	link.Meta[0] = '['
	assert.Equal(t, new(42.5), copied.Size)
	assert.Equal(t, "image/png", *copied.Icons[0].MIMEType)
	assert.Equal(t, "dark", *copied.Icons[0].Theme)
	assert.Equal(t, []string{"any"}, copied.Icons[0].Sizes)
	assert.JSONEq(t, `{"example.org/source":{"id":1}}`, string(copied.Meta))
}
