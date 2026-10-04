// Package toolcontent prepares validated tool blocks for native provider tool
// results. It retains assistant-visible order, decodes supplied media locally,
// and describes resources without opening their addresses. Extension metadata,
// display icons and user-only blocks remain in stored and host content.
package toolcontent

import (
	"encoding/base64"
	"fmt"
	"slices"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

// Parts returns the existing text, image and document values that provider
// adapters can nest in a correlated tool result. Unsupported media returns an
// inspectable error; it never becomes a text substitute for unseen media.
func Parts(blocks content.Blocks) ([]model.Part, error) {
	if err := blocks.Validate(); err != nil {
		return nil, fmt.Errorf("tool content: %w", err)
	}
	var parts []model.Part
	for index, block := range blocks {
		switch value := block.(type) {
		case *content.TextContent:
			if assistantAudience(value.Annotations) {
				parts = append(parts, model.TextPart{Text: value.Text})
			}
		case *content.ImageContent:
			if !assistantAudience(value.Annotations) {
				continue
			}
			image, err := imagePart(value.Data, value.MIMEType)
			if err != nil {
				return nil, fmt.Errorf("tool content[%d]: %w", index, err)
			}
			parts = append(parts, image)
		case *content.AudioContent:
			if assistantAudience(value.Annotations) {
				return nil, fmt.Errorf("tool content[%d] audio %q: %w", index, value.MIMEType, model.ErrToolContentUnsupported)
			}
		case *content.ResourceLink:
			if !assistantAudience(value.Annotations) {
				continue
			}
			link := *value
			link.Meta, link.Annotations, link.Icons = nil, nil, nil
			encoded, err := (content.Blocks{&link}).MarshalJSON()
			if err != nil {
				return nil, fmt.Errorf("tool content[%d] resource link: %w", index, err)
			}
			parts = append(parts, model.TextPart{Text: string(encoded)})
		case *content.EmbeddedResource:
			if !assistantAudience(value.Annotations) {
				continue
			}
			resource, err := resourceParts(value.Resource, index)
			if err != nil {
				return nil, fmt.Errorf("tool content[%d]: %w", index, err)
			}
			parts = append(parts, resource...)
		}
	}
	return parts, nil
}

// assistantAudience honors an explicit audience. Unspecified audiences make
// content available to both the model and the host; no priority changes order.
func assistantAudience(annotations *content.Annotations) bool {
	return annotations == nil || len(annotations.Audience) == 0 ||
		slices.Contains(annotations.Audience, content.RoleAssistant)
}

// imagePart decodes only image formats shared by the provider-neutral image
// contract. Each provider still owns its narrower native format constraints.
func imagePart(data, mime string) (model.ImagePart, error) {
	var format model.ImageFormat
	switch mime {
	case "image/png":
		format = model.ImageFormatPNG
	case "image/jpeg":
		format = model.ImageFormatJPEG
	case "image/gif":
		format = model.ImageFormatGIF
	case "image/webp":
		format = model.ImageFormatWEBP
	default:
		return model.ImagePart{}, fmt.Errorf("image MIME type %q: %w", mime, model.ErrToolContentUnsupported)
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return model.ImagePart{}, fmt.Errorf("decode image data: %w", err)
	}
	if len(bytes) == 0 {
		return model.ImagePart{}, fmt.Errorf("empty image: %w", model.ErrToolContentUnsupported)
	}
	return model.ImagePart{Format: format, Bytes: bytes}, nil
}

// resourceParts keeps the resource identity beside its supplied contents.
// Binary resources require an explicit supported MIME type; addresses never
// choose a file format or trigger a network read.
func resourceParts(resource content.ResourceContents, index int) ([]model.Part, error) {
	switch value := resource.(type) {
	case *content.TextResourceContents:
		text := *value
		text.Meta = nil
		encoded, err := (content.Blocks{&content.EmbeddedResource{Resource: &text}}).MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("encode text resource: %w", err)
		}
		return []model.Part{model.TextPart{Text: string(encoded)}}, nil
	case *content.BlobResourceContents:
		if value.MIMEType == nil {
			return nil, fmt.Errorf("binary resource without MIME type: %w", model.ErrToolContentUnsupported)
		}
		mime := *value.MIMEType
		description := model.TextPart{Text: fmt.Sprintf("Embedded resource %q (%s).", value.URI, mime)}
		if mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" || mime == "image/webp" {
			image, err := imagePart(value.Blob, mime)
			if err != nil {
				return nil, err
			}
			return []model.Part{description, image}, nil
		}
		var format model.DocumentFormat
		switch mime {
		case "application/pdf":
			format = model.DocumentFormatPDF
		case "text/plain":
			format = model.DocumentFormatTXT
		case "text/csv":
			format = model.DocumentFormatCSV
		case "application/msword":
			format = model.DocumentFormatDOC
		case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
			format = model.DocumentFormatDOCX
		case "application/vnd.ms-excel":
			format = model.DocumentFormatXLS
		case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
			format = model.DocumentFormatXLSX
		case "text/html":
			format = model.DocumentFormatHTML
		case "text/markdown":
			format = model.DocumentFormatMD
		default:
			return nil, fmt.Errorf("binary resource MIME type %q: %w", mime, model.ErrToolContentUnsupported)
		}
		bytes, err := base64.StdEncoding.DecodeString(value.Blob)
		if err != nil {
			return nil, fmt.Errorf("decode resource data: %w", err)
		}
		if len(bytes) == 0 || format == model.DocumentFormatTXT && !utf8.Valid(bytes) {
			return nil, fmt.Errorf("empty or invalid resource media: %w", model.ErrToolContentUnsupported)
		}
		return []model.Part{description, model.DocumentPart{
			Name:   fmt.Sprintf("resource-%d", index),
			Format: format,
			Bytes:  bytes,
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported resource contents %T", resource)
	}
}
