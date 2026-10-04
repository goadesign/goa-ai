// Package content encodes one ordered sequence of tool content for MCP and
// durable history. Decoding validates media, resource addresses and metadata;
// encoding checks caller-built values before they enter a saved or sent result.
package content

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"unicode/utf8"
)

type (
	contentItem struct {
		Type        string          `json:"type"`
		Text        *string         `json:"text,omitempty"`
		Data        *string         `json:"data,omitempty"`
		MIMEType    *string         `json:"mimeType,omitempty"` //nolint:tagliatelle // MCP protocol field.
		Name        *string         `json:"name,omitempty"`
		Title       *string         `json:"title,omitempty"`
		URI         *string         `json:"uri,omitempty"`
		Description *string         `json:"description,omitempty"`
		Size        *float64        `json:"size,omitempty"`
		Icons       []Icon          `json:"icons,omitempty"`
		Resource    json.RawMessage `json:"resource,omitempty"`
		Annotations *Annotations    `json:"annotations,omitempty"`
		Meta        json.RawMessage `json:"_meta,omitempty"` //nolint:tagliatelle // MCP protocol field.
	}

	resourceContents struct {
		URI      *string         `json:"uri,omitempty"`
		MIMEType *string         `json:"mimeType,omitempty"` //nolint:tagliatelle // MCP protocol field.
		Text     *string         `json:"text,omitempty"`
		Blob     *string         `json:"blob,omitempty"`
		Meta     json.RawMessage `json:"_meta,omitempty"` //nolint:tagliatelle // MCP protocol field.
	}
)

// MarshalJSON validates every block and returns the flat content array used by
// MCP peers and stored events. An empty sequence is always encoded as [].
func (blocks Blocks) MarshalJSON() ([]byte, error) {
	items := make([]contentItem, len(blocks))
	for index, block := range blocks {
		item, err := encodeBlock(block)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		if err := validateWireText(item); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		if _, err := normalizeContentBlock(item); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		items[index] = item
	}
	return json.Marshal(items)
}

// UnmarshalJSON validates the incoming content array before replacing the
// receiver. A malformed block leaves the previous sequence unchanged.
func (blocks *Blocks) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("content JSON contains invalid UTF-8")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("content must be an array")
	}
	var items []contentItem
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("decode content array: %w", err)
	}
	decoded := make(Blocks, len(items))
	for index, item := range items {
		block, err := normalizeContentBlock(item)
		if err != nil {
			return fmt.Errorf("content[%d]: %w", index, err)
		}
		decoded[index] = block
	}
	*blocks = decoded
	return nil
}

// encodeBlock copies one typed value into the matching flat wire variant.
// Nil blocks and nil embedded resources are rejected instead of losing content.
func encodeBlock(block ContentBlock) (contentItem, error) {
	switch value := block.(type) {
	case *TextContent:
		if value == nil {
			return contentItem{}, errors.New("text content is nil")
		}
		return contentItem{
			Type:        "text",
			Text:        &value.Text,
			Annotations: value.Annotations,
			Meta:        value.Meta,
		}, nil
	case *ImageContent:
		if value == nil {
			return contentItem{}, errors.New("image content is nil")
		}
		return contentItem{
			Type:        "image",
			Data:        &value.Data,
			MIMEType:    &value.MIMEType,
			Annotations: value.Annotations,
			Meta:        value.Meta,
		}, nil
	case *AudioContent:
		if value == nil {
			return contentItem{}, errors.New("audio content is nil")
		}
		return contentItem{
			Type:        "audio",
			Data:        &value.Data,
			MIMEType:    &value.MIMEType,
			Annotations: value.Annotations,
			Meta:        value.Meta,
		}, nil
	case *ResourceLink:
		if value == nil {
			return contentItem{}, errors.New("resource link is nil")
		}
		return contentItem{
			Type:        "resource_link",
			Name:        &value.Name,
			URI:         &value.URI,
			Title:       value.Title,
			Description: value.Description,
			MIMEType:    value.MIMEType,
			Size:        value.Size,
			Icons:       value.Icons,
			Annotations: value.Annotations,
			Meta:        value.Meta,
		}, nil
	case *EmbeddedResource:
		if value == nil {
			return contentItem{}, errors.New("embedded resource is nil")
		}
		resource, err := encodeResource(value.Resource)
		if err != nil {
			return contentItem{}, err
		}
		if err := validateResourceText(resource); err != nil {
			return contentItem{}, err
		}
		encoded, err := json.Marshal(resource)
		if err != nil {
			return contentItem{}, fmt.Errorf("encode embedded resource: %w", err)
		}
		return contentItem{
			Type:        "resource",
			Resource:    encoded,
			Annotations: value.Annotations,
			Meta:        value.Meta,
		}, nil
	default:
		return contentItem{}, fmt.Errorf("unsupported content block %T", block)
	}
}

// encodeResource copies the selected text or blob representation without
// inventing resource data or fetching the address.
func encodeResource(resource ResourceContents) (resourceContents, error) {
	switch value := resource.(type) {
	case *TextResourceContents:
		if value == nil {
			return resourceContents{}, errors.New("text resource is nil")
		}
		return resourceContents{
			URI:      &value.URI,
			MIMEType: value.MIMEType,
			Text:     &value.Text,
			Meta:     value.Meta,
		}, nil
	case *BlobResourceContents:
		if value == nil {
			return resourceContents{}, errors.New("blob resource is nil")
		}
		return resourceContents{
			URI:      &value.URI,
			MIMEType: value.MIMEType,
			Blob:     &value.Blob,
			Meta:     value.Meta,
		}, nil
	default:
		return resourceContents{}, fmt.Errorf("unsupported resource contents %T", resource)
	}
}

// normalizeContentBlock validates one flat block and returns its typed variant.
// Transport and saved-value decoding therefore apply the same content contract.
func normalizeContentBlock(item contentItem) (ContentBlock, error) {
	if err := validateContentMetadata(item.Annotations, item.Meta); err != nil {
		return nil, err
	}
	switch item.Type {
	case "text":
		if item.Text == nil {
			return nil, errors.New("text content is missing text")
		}
		return &TextContent{Text: *item.Text, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "image":
		if item.Data == nil || item.MIMEType == nil {
			return nil, errors.New("image content requires data and mimeType")
		}
		if err := validateBase64(*item.Data); err != nil {
			return nil, err
		}
		return &ImageContent{Data: *item.Data, MIMEType: *item.MIMEType, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "audio":
		if item.Data == nil || item.MIMEType == nil {
			return nil, errors.New("audio content requires data and mimeType")
		}
		if err := validateBase64(*item.Data); err != nil {
			return nil, err
		}
		return &AudioContent{Data: *item.Data, MIMEType: *item.MIMEType, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	case "resource_link":
		if item.Name == nil || item.URI == nil {
			return nil, errors.New("resource link requires name and uri")
		}
		if err := validateContentURI(*item.URI); err != nil {
			return nil, err
		}
		for _, icon := range item.Icons {
			if err := validateContentURI(icon.Src); err != nil {
				return nil, err
			}
			if icon.Theme != nil && *icon.Theme != "light" && *icon.Theme != "dark" {
				return nil, errors.New("icon theme must be light or dark")
			}
		}
		if item.Size != nil && (math.IsNaN(*item.Size) || math.IsInf(*item.Size, 0) || *item.Size < 0) {
			return nil, errors.New("resource link size must be finite and non-negative")
		}
		return &ResourceLink{
			Name: *item.Name, URI: *item.URI, Title: item.Title,
			Description: item.Description, MIMEType: item.MIMEType, Size: item.Size,
			Icons: item.Icons, Annotations: item.Annotations, Meta: cloneRaw(item.Meta),
		}, nil
	case "resource":
		resource, err := normalizeResourceContents(item.Resource)
		if err != nil {
			return nil, err
		}
		return &EmbeddedResource{Resource: resource, Annotations: item.Annotations, Meta: cloneRaw(item.Meta)}, nil
	default:
		return nil, fmt.Errorf("unsupported content type %q", item.Type)
	}
}

// normalizeResourceContents decodes the text-or-blob union carried by an
// embedded resource.
func normalizeResourceContents(raw json.RawMessage) (ResourceContents, error) {
	if len(raw) == 0 {
		return nil, errors.New("embedded resource is missing resource")
	}
	var resource resourceContents
	if err := json.Unmarshal(raw, &resource); err != nil || resource.URI == nil {
		return nil, errors.New("embedded resource requires a resource object with uri")
	}
	if err := validateContentURI(*resource.URI); err != nil {
		return nil, err
	}
	if err := validateMeta(resource.Meta); err != nil {
		return nil, err
	}
	if (resource.Text == nil) == (resource.Blob == nil) {
		return nil, errors.New("embedded resource must contain exactly one of text or blob")
	}
	if resource.Text != nil {
		return &TextResourceContents{URI: *resource.URI, MIMEType: resource.MIMEType, Text: *resource.Text, Meta: cloneRaw(resource.Meta)}, nil
	}
	if err := validateBase64(*resource.Blob); err != nil {
		return nil, err
	}
	return &BlobResourceContents{URI: *resource.URI, MIMEType: resource.MIMEType, Blob: *resource.Blob, Meta: cloneRaw(resource.Meta)}, nil
}

// validateContentMetadata checks the closed MCP annotation values and the
// object shape required for extension metadata.
func validateContentMetadata(annotations *Annotations, meta json.RawMessage) error {
	if annotations != nil {
		for _, role := range annotations.Audience {
			if role != RoleUser && role != RoleAssistant {
				return fmt.Errorf("unsupported annotation audience %q", role)
			}
		}
		if annotations.Priority != nil && (math.IsNaN(*annotations.Priority) || math.IsInf(*annotations.Priority, 0) || *annotations.Priority < 0 || *annotations.Priority > 1) {
			return errors.New("annotation priority must be between zero and one")
		}
	}
	return validateMeta(meta)
}

// validateMeta requires MCP extension metadata to be a JSON object.
func validateMeta(meta json.RawMessage) error {
	if len(meta) == 0 {
		return nil
	}
	if !utf8.Valid(meta) {
		return errors.New("_meta contains invalid UTF-8")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(meta, &object); err != nil || object == nil {
		return errors.New("_meta must be a JSON object")
	}
	return nil
}

// validateBase64 checks the encoded bytes in one media or resource item. Empty
// data remains valid; decoding does not impose an operation-wide size limit.
func validateBase64(data string) error {
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return fmt.Errorf("content must contain base64 data: %w", err)
	}
	return nil
}

// validateContentURI checks one resource or icon address without opening it.
func validateContentURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("content URI %q must be an absolute URI", uri)
	}
	return nil
}

// cloneRaw gives each decoded block ownership of its extension metadata.
func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

// validateWireText rejects caller text that JSON encoding would silently replace.
// Required values and optional presentation text therefore keep their exact bytes.
func validateWireText(item contentItem) error {
	values := []*string{item.Text, item.Data, item.MIMEType, item.Name, item.Title, item.URI, item.Description}
	for _, value := range values {
		if value != nil && !utf8.ValidString(*value) {
			return errors.New("content text contains invalid UTF-8")
		}
	}
	if item.Annotations != nil && item.Annotations.LastModified != nil && !utf8.ValidString(*item.Annotations.LastModified) {
		return errors.New("annotation text contains invalid UTF-8")
	}
	for _, icon := range item.Icons {
		if !utf8.ValidString(icon.Src) {
			return errors.New("icon address contains invalid UTF-8")
		}
		for _, value := range []*string{icon.MIMEType, icon.Theme} {
			if value != nil && !utf8.ValidString(*value) {
				return errors.New("icon text contains invalid UTF-8")
			}
		}
		for _, size := range icon.Sizes {
			if !utf8.ValidString(size) {
				return errors.New("icon size contains invalid UTF-8")
			}
		}
	}
	return nil
}

// validateResourceText rejects caller-built resource text before JSON encoding
// could change it. Decoding already receives a validated UTF-8 JSON document.
func validateResourceText(resource resourceContents) error {
	for _, value := range []*string{resource.URI, resource.MIMEType, resource.Text, resource.Blob} {
		if value != nil && !utf8.ValidString(*value) {
			return errors.New("resource text contains invalid UTF-8")
		}
	}
	return nil
}
