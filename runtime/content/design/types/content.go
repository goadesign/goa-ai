// Package types declares tool content independently of a service transport.
// Public run replies and MCP generation share these leaf fields. Content keeps
// its selected kind and original order; no converter fetches a resource URL.
package types

import (
	"goa.design/goa-ai/internal/mcpvalue"
	"goa.design/goa-ai/runtime/content"
	. "goa.design/goa/v3/dsl"
)

var (
	// Annotations retains audience, importance and modification hints.
	Annotations = mcpvalue.Define("ToolContentAnnotations", "toolcontent", mcpvalue.Annotations(), func() {
		CreateFrom(content.Annotations{})
		ConvertTo(content.Annotations{})
	})

	// Icon retains a resource image description without loading its address.
	Icon = mcpvalue.Define("ToolContentIcon", "toolcontent", mcpvalue.Icon(), func() {
		CreateFrom(content.Icon{})
		ConvertTo(content.Icon{})
	})

	// Text retains one text block, including an explicitly empty string.
	Text = Type("ToolTextContent", func() {
		Meta("struct:pkg:path", "toolcontent")
		CreateFrom(content.TextContent{})
		ConvertTo(content.TextContent{})
		Field(1, "text", String, "Text returned by the tool.")
		Field(2, "annotations", Annotations, "Audience and importance hints.")
		Field(3, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("text")
	})

	// Image retains base64 image data and its MIME type.
	Image = Type("ToolImageContent", func() {
		Meta("struct:pkg:path", "toolcontent")
		CreateFrom(content.ImageContent{})
		ConvertTo(content.ImageContent{})
		Field(1, "data", String, "Base64-encoded image bytes.")
		Field(2, "mimeType", String, "Image MIME type.", func() {
			Meta("struct:field:external", "MIMEType")
			Meta("struct:field:name", "MIMEType")
		})
		Field(3, "annotations", Annotations, "Audience and importance hints.")
		Field(4, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("data", "mimeType")
	})

	// Audio retains base64 audio data and its MIME type.
	Audio = Type("ToolAudioContent", func() {
		Meta("struct:pkg:path", "toolcontent")
		CreateFrom(content.AudioContent{})
		ConvertTo(content.AudioContent{})
		Field(1, "data", String, "Base64-encoded audio bytes.")
		Field(2, "mimeType", String, "Audio MIME type.", func() {
			Meta("struct:field:external", "MIMEType")
			Meta("struct:field:name", "MIMEType")
		})
		Field(3, "annotations", Annotations, "Audience and importance hints.")
		Field(4, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("data", "mimeType")
	})

	// ResourceLink retains a named resource address and optional descriptions.
	ResourceLink = Type("ToolResourceLink", func() {
		Meta("struct:pkg:path", "toolcontent")
		Field(1, "name", String, "Resource identifier.")
		Field(2, "uri", String, "Resource address.", func() { Format(FormatURI) })
		Field(3, "title", String, "Optional resource display title.")
		Field(4, "description", String, "Optional resource description.")
		Field(5, "mimeType", String, "Optional resource MIME type.", func() {
			Meta("struct:field:external", "MIMEType")
			Meta("struct:field:name", "MIMEType")
		})
		Field(6, "size", Float64, "Raw resource bytes before base64 encoding.", func() { Minimum(0) })
		Field(7, "icons", ArrayOfRequired(Icon), "Optional resource icons.")
		Field(8, "annotations", Annotations, "Audience and importance hints.")
		Field(9, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("name", "uri")
	})

	// TextResource retains a resource's text without a later fetch.
	TextResource = Type("ToolTextResource", func() {
		Meta("struct:pkg:path", "toolcontent")
		CreateFrom(content.TextResourceContents{})
		ConvertTo(content.TextResourceContents{})
		Field(1, "uri", String, "Resource address.", func() { Format(FormatURI) })
		Field(2, "mimeType", String, "Optional resource MIME type.", func() {
			Meta("struct:field:external", "MIMEType")
			Meta("struct:field:name", "MIMEType")
		})
		Field(3, "text", String, "Resource text, including an empty string.")
		Field(4, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("uri", "text")
	})

	// BlobResource retains a resource's bytes without a later fetch.
	BlobResource = Type("ToolBlobResource", func() {
		Meta("struct:pkg:path", "toolcontent")
		CreateFrom(content.BlobResourceContents{})
		ConvertTo(content.BlobResourceContents{})
		Field(1, "uri", String, "Resource address.", func() { Format(FormatURI) })
		Field(2, "mimeType", String, "Optional resource MIME type.", func() {
			Meta("struct:field:external", "MIMEType")
			Meta("struct:field:name", "MIMEType")
		})
		Field(3, "blob", String, "Base64-encoded resource bytes.")
		Field(4, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("uri", "blob")
	})

	// EmbeddedResource selects one included text or binary resource.
	EmbeddedResource = Type("ToolEmbeddedResource", func() {
		Meta("struct:pkg:path", "toolcontent")
		OneOf("resource", func() {
			TypeName("ToolResourceContents")
			Field(1, "text", TextResource, "Included text resource.")
			Field(2, "blob", BlobResource, "Included binary resource.")
		})
		Field(3, "annotations", Annotations, "Audience and importance hints.")
		Field(4, "_meta", Bytes, "Exact JSON-object bytes containing open namespaced protocol extensions.", contentMeta)
		Required("resource")
	})

	// Block selects one of the five content kinds supported by the runtime.
	Block = Type("ToolContentBlock", func() {
		Meta("struct:pkg:path", "toolcontent")
		OneOf("content", func() {
			TypeName("ToolContentValue")
			Field(1, "text", Text, "Text returned by the tool.")
			Field(2, "image", Image, "Image returned by the tool.")
			Field(3, "audio", Audio, "Audio returned by the tool.")
			Field(4, "resource_link", ResourceLink, "Reference to a resource.")
			Field(5, "resource", EmbeddedResource, "Included resource contents.")
		})
		Required("content")
	})
)

// contentMeta maps exact extension bytes to the runtime metadata field. The
// content owner validates the JSON object before converting it to MCP content.
func contentMeta() {
	Meta("struct:field:type", "json.RawMessage", "encoding/json")
	Meta("struct:field:external", "Meta")
	Meta("struct:tag:json", "_meta,omitempty")
}
