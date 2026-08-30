// Package mcp represents every content block allowed in an MCP tool result.
// Transport clients decode the wire format into these values so callers keep
// images, audio, resource links, and embedded resources without losing data.
package mcp

import "encoding/json"

type (
	// ContentBlock is one typed item returned by an MCP tool.
	ContentBlock interface {
		isContentBlock()
	}

	// Role identifies an MCP message audience.
	Role string

	// Annotations describe who should see a content block and its importance.
	Annotations struct {
		// Audience lists the roles this content is intended for.
		Audience []Role `json:"audience,omitempty"`
		// Priority is the content importance from zero to one.
		Priority *float64 `json:"priority,omitempty"`
		// LastModified records when the content last changed.
		LastModified *string `json:"lastModified,omitempty"` //nolint:tagliatelle // MCP requires this field name.
	}

	// TextContent is a text block returned by an MCP tool.
	TextContent struct {
		// Text is the returned text.
		Text string
		// Annotations describe how the block should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// ImageContent is a base64-encoded image returned by an MCP tool.
	ImageContent struct {
		// Data is the base64-encoded image data.
		Data string
		// MIMEType identifies the image format.
		MIMEType string
		// Annotations describe how the block should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// AudioContent is base64-encoded audio returned by an MCP tool.
	AudioContent struct {
		// Data is the base64-encoded audio data.
		Data string
		// MIMEType identifies the audio format.
		MIMEType string
		// Annotations describe how the block should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// ResourceLink is a link to a resource returned by an MCP tool.
	ResourceLink struct {
		// Name is the resource name.
		Name string
		// URI identifies the resource.
		URI string
		// Title is the optional display title.
		Title *string
		// Description explains the resource.
		Description *string
		// MIMEType identifies the resource format.
		MIMEType *string
		// Size is the resource size in bytes.
		Size *int64
		// Annotations describe how the link should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// EmbeddedResource contains resource data returned directly by an MCP tool.
	EmbeddedResource struct {
		// Resource is text or base64-encoded binary resource data.
		Resource ResourceContents
		// Annotations describe how the resource should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// ResourceContents is the data stored in an embedded MCP resource.
	ResourceContents interface {
		isResourceContents()
	}

	// TextResourceContents contains an embedded text resource.
	TextResourceContents struct {
		// URI identifies the resource.
		URI string
		// MIMEType identifies the text format.
		MIMEType *string
		// Text is the resource contents.
		Text string
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// BlobResourceContents contains an embedded binary resource.
	BlobResourceContents struct {
		// URI identifies the resource.
		URI string
		// MIMEType identifies the binary format.
		MIMEType *string
		// Blob is the base64-encoded resource contents.
		Blob string
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}
)

const (
	// RoleUser identifies content intended for a user.
	RoleUser Role = "user"
	// RoleAssistant identifies content intended for an assistant.
	RoleAssistant Role = "assistant"
)

func (*TextContent) isContentBlock()      {}
func (*ImageContent) isContentBlock()     {}
func (*AudioContent) isContentBlock()     {}
func (*ResourceLink) isContentBlock()     {}
func (*EmbeddedResource) isContentBlock() {}

func (*TextResourceContents) isResourceContents() {}
func (*BlobResourceContents) isResourceContents() {}
