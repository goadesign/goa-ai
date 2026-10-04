// Package content owns ordered tool-result content shared by transport clients,
// saved runtime events and model messages. Its closed variants preserve text,
// media, resource references and metadata without opening resource addresses.
package content

import "encoding/json"

type (
	// Blocks retains typed content in the order returned by one tool invocation.
	// Its JSON codec uses the MCP content shape for transport and saved values.
	Blocks []ContentBlock

	// ContentBlock is one typed item returned by a tool.
	ContentBlock interface {
		isContentBlock()
	}

	// Role identifies a tool-content audience.
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

	// TextContent is a text block returned by a tool.
	TextContent struct {
		// Text is the returned text.
		Text string
		// Annotations describe how the block should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// ImageContent is a base64-encoded image returned by a tool.
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

	// AudioContent is base64-encoded audio returned by a tool.
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

	// ResourceLink is a link to a resource returned by a tool.
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
		Size *float64
		// Icons are optional images supplied for the resource link.
		Icons []Icon
		// Annotations describe how the link should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// Icon describes an image URI supplied by a tool. Decoding retains the
	// description without fetching or rendering the image.
	Icon struct {
		// Src is the image URI.
		Src string `json:"src"`
		// MIMEType optionally identifies the image format.
		MIMEType *string `json:"mimeType,omitempty"` //nolint:tagliatelle // MCP defines this wire field name.
		// Sizes lists suggested dimensions or "any" for scalable images.
		Sizes []string `json:"sizes,omitempty"`
		// Theme optionally identifies the light or dark display theme.
		Theme *string `json:"theme,omitempty"`
	}

	// EmbeddedResource contains resource data returned directly by a tool.
	EmbeddedResource struct {
		// Resource is text or base64-encoded binary resource data.
		Resource ResourceContents
		// Annotations describe how the resource should be presented.
		Annotations *Annotations
		// Meta preserves protocol extension data.
		Meta json.RawMessage
	}

	// ResourceContents is the data stored in an embedded resource.
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
