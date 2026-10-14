// Package runtime converts public content while preserving its five content
// kinds and their order. Generated converters copy individual fields; these
// functions select branches and apply the content owner's cross-field rules.
package runtime

import (
	"fmt"
	"slices"

	gentoolcontent "goa.design/goa-ai/registry/gen/toolcontent"
	"goa.design/goa-ai/runtime/content"
)

// publicContentBlocks validates runtime content and retains each selected kind
// without serializing the complete value or fetching resource addresses.
func publicContentBlocks(blocks content.Blocks) ([]*gentoolcontent.ToolContentBlock, error) {
	if err := blocks.Validate(); err != nil {
		return nil, err
	}
	result := make([]*gentoolcontent.ToolContentBlock, 0, len(blocks))
	for _, block := range blocks.Clone() {
		var selected gentoolcontent.ToolContentValue
		switch value := block.(type) {
		case *content.TextContent:
			converted := &gentoolcontent.ToolTextContent{}
			converted.CreateFromTextContent(value)
			selected = gentoolcontent.NewToolContentValueText(converted)
		case *content.ImageContent:
			converted := &gentoolcontent.ToolImageContent{}
			converted.CreateFromImageContent(value)
			selected = gentoolcontent.NewToolContentValueImage(converted)
		case *content.AudioContent:
			converted := &gentoolcontent.ToolAudioContent{}
			converted.CreateFromAudioContent(value)
			selected = gentoolcontent.NewToolContentValueAudio(converted)
		case *content.ResourceLink:
			converted := &gentoolcontent.ToolResourceLink{
				Name: value.Name, URI: value.URI, Title: value.Title,
				Description: value.Description, MIMEType: value.MIMEType,
				Size: value.Size, Meta: value.Meta,
			}
			for i := range value.Icons {
				icon := &gentoolcontent.ToolContentIcon{}
				icon.CreateFromIcon(&value.Icons[i])
				converted.Icons = append(converted.Icons, icon)
			}
			if value.Annotations != nil {
				converted.Annotations = &gentoolcontent.ToolContentAnnotations{}
				converted.Annotations.CreateFromAnnotations(value.Annotations)
			}
			selected = gentoolcontent.NewToolContentValueResourceLink(converted)
		case *content.EmbeddedResource:
			converted := &gentoolcontent.ToolEmbeddedResource{Meta: slices.Clone(value.Meta)}
			if value.Annotations != nil {
				converted.Annotations = &gentoolcontent.ToolContentAnnotations{}
				converted.Annotations.CreateFromAnnotations(value.Annotations)
			}
			switch resource := value.Resource.(type) {
			case *content.TextResourceContents:
				included := &gentoolcontent.ToolTextResource{}
				included.CreateFromTextResourceContents(resource)
				converted.Resource = gentoolcontent.NewToolResourceContentsText(included)
			case *content.BlobResourceContents:
				included := &gentoolcontent.ToolBlobResource{}
				included.CreateFromBlobResourceContents(resource)
				converted.Resource = gentoolcontent.NewToolResourceContentsBlob(included)
			default:
				return nil, fmt.Errorf("unsupported included resource %T", value.Resource)
			}
			selected = gentoolcontent.NewToolContentValueResource(converted)
		default:
			return nil, fmt.Errorf("unsupported content block %T", block)
		}
		result = append(result, &gentoolcontent.ToolContentBlock{Content: selected})
	}
	return result, nil
}

// inputContentBlocks copies generated content into runtime values and validates
// media bytes, resource addresses, metadata and the selected resource kind.
func inputContentBlocks(blocks []*gentoolcontent.ToolContentBlock) (content.Blocks, error) {
	result := make(content.Blocks, 0, len(blocks))
	for _, block := range blocks {
		if value, ok := block.Content.AsText(); ok {
			result = append(result, value.ConvertToTextContent())
		} else if value, ok := block.Content.AsImage(); ok {
			result = append(result, value.ConvertToImageContent())
		} else if value, ok := block.Content.AsAudio(); ok {
			result = append(result, value.ConvertToAudioContent())
		} else if value, ok := block.Content.AsResourceLink(); ok {
			converted := &content.ResourceLink{
				Name: value.Name, URI: value.URI, Title: value.Title,
				Description: value.Description, MIMEType: value.MIMEType,
				Size: value.Size, Meta: value.Meta,
			}
			for _, icon := range value.Icons {
				converted.Icons = append(converted.Icons, *icon.ConvertToIcon())
			}
			if value.Annotations != nil {
				converted.Annotations = value.Annotations.ConvertToAnnotations()
			}
			result = append(result, converted)
		} else if value, ok := block.Content.AsResource(); ok {
			converted := &content.EmbeddedResource{Meta: slices.Clone(value.Meta)}
			if value.Annotations != nil {
				converted.Annotations = value.Annotations.ConvertToAnnotations()
			}
			if resource, ok := value.Resource.AsText(); ok {
				converted.Resource = resource.ConvertToTextResourceContents()
			} else if resource, ok := value.Resource.AsBlob(); ok {
				converted.Resource = resource.ConvertToBlobResourceContents()
			} else {
				return nil, fmt.Errorf("unsupported resource content kind %q", value.Resource.Kind())
			}
			result = append(result, converted)
		} else {
			return nil, fmt.Errorf("unsupported content kind %q", block.Content.Kind())
		}
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result.Clone(), nil
}
