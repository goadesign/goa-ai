// Package mcp defines errors that callers can inspect without parsing text.
package mcp

import (
	"fmt"
	"strings"
)

type (
	// MalformedResponseError reports an MCP response with missing or invalid fields.
	MalformedResponseError struct {
		cause error
	}

	// InternalError reports a bug in the MCP client.
	InternalError struct {
		cause error
	}

	// ToolExecutionError reports an MCP tools/call response whose isError flag
	// says the remote tool rejected or failed the call.
	ToolExecutionError struct {
		// Response contains the text and structured data returned by the tool.
		Response CallResponse
	}
)

// NewMalformedResponseError wraps a response decoding or shape failure.
func NewMalformedResponseError(cause error) *MalformedResponseError {
	if cause == nil {
		panic("mcp: malformed response error requires a cause")
	}
	return &MalformedResponseError{cause: cause}
}

// Error implements error.
func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("malformed MCP response: %v", e.cause)
}

// Unwrap returns the response failure.
func (e *MalformedResponseError) Unwrap() error {
	return e.cause
}

// NewInternalError wraps a bug in the MCP client.
func NewInternalError(cause error) *InternalError {
	if cause == nil {
		panic("mcp: internal error requires a cause")
	}
	return &InternalError{cause: cause}
}

// Error implements error.
func (e *InternalError) Error() string {
	return fmt.Sprintf("internal MCP client failure: %v", e.cause)
}

// Unwrap returns the implementation failure.
func (e *InternalError) Unwrap() error {
	return e.cause
}

// NewToolExecutionError preserves the validated result returned by a tool that
// set MCP's isError flag.
func NewToolExecutionError(response CallResponse) *ToolExecutionError {
	response.Content = cloneContentBlocks(response.Content)
	response.StructuredContent = append([]byte(nil), response.StructuredContent...)
	return &ToolExecutionError{Response: response}
}

// Error implements error.
func (e *ToolExecutionError) Error() string {
	if len(e.Response.Content) == 0 {
		return "MCP tool execution error"
	}
	var messages []string
	for _, block := range e.Response.Content {
		if text, ok := block.(*TextContent); ok {
			messages = append(messages, text.Text)
		}
	}
	if len(messages) == 0 {
		return "MCP tool execution error"
	}
	return "MCP tool execution error: " + strings.Join(messages, "\n")
}

// cloneContentBlocks copies tool error content so callers cannot change the
// error after it is created.
func cloneContentBlocks(content []ContentBlock) []ContentBlock {
	cloned := make([]ContentBlock, len(content))
	for i, block := range content {
		switch value := block.(type) {
		case *TextContent:
			copy := *value
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		case *ImageContent:
			copy := *value
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		case *AudioContent:
			copy := *value
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		case *ResourceLink:
			copy := *value
			copy.Title = cloneString(value.Title)
			copy.Description = cloneString(value.Description)
			copy.MIMEType = cloneString(value.MIMEType)
			copy.Size = cloneInt64(value.Size)
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		case *EmbeddedResource:
			copy := *value
			copy.Resource = cloneResourceContents(value.Resource)
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		}
	}
	return cloned
}

// cloneResourceContents copies one embedded resource value.
func cloneResourceContents(resource ResourceContents) ResourceContents {
	switch value := resource.(type) {
	case *TextResourceContents:
		copy := *value
		copy.MIMEType = cloneString(value.MIMEType)
		copy.Meta = cloneRaw(value.Meta)
		return &copy
	case *BlobResourceContents:
		copy := *value
		copy.MIMEType = cloneString(value.MIMEType)
		copy.Meta = cloneRaw(value.Meta)
		return &copy
	default:
		panic("mcp: unknown resource contents")
	}
}

// cloneAnnotations copies optional presentation metadata.
func cloneAnnotations(annotations *Annotations) *Annotations {
	if annotations == nil {
		return nil
	}
	copy := *annotations
	copy.Audience = append([]Role(nil), annotations.Audience...)
	if annotations.Priority != nil {
		priority := *annotations.Priority
		copy.Priority = &priority
	}
	copy.LastModified = cloneString(annotations.LastModified)
	return &copy
}

// cloneString copies an optional string.
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// cloneInt64 copies an optional integer.
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
