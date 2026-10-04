// Package content copies validated tool-result blocks for independent owners.
// Copying keeps order, media, optional fields and nested metadata intact.
package content

import "slices"

// Clone copies a validated sequence, including nested resource metadata and
// icons. The caller receives values it can change without changing the original.
func (blocks Blocks) Clone() Blocks {
	cloned := slices.Clone(blocks)
	for i, block := range blocks {
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
			if value.Size != nil {
				size := *value.Size
				copy.Size = &size
			}
			copy.Icons = slices.Clone(value.Icons)
			for i, icon := range value.Icons {
				copy.Icons[i] = icon
				copy.Icons[i].MIMEType = cloneString(icon.MIMEType)
				copy.Icons[i].Theme = cloneString(icon.Theme)
				copy.Icons[i].Sizes = append([]string(nil), icon.Sizes...)
			}
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		case *EmbeddedResource:
			copy := *value
			copy.Resource = cloneResourceContents(value.Resource)
			copy.Annotations = cloneAnnotations(value.Annotations)
			copy.Meta = cloneRaw(value.Meta)
			cloned[i] = &copy
		default:
			panic("content: unknown content block")
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
		panic("content: unknown resource contents")
	}
}

// cloneAnnotations copies optional presentation metadata.
func cloneAnnotations(annotations *Annotations) *Annotations {
	if annotations == nil {
		return nil
	}
	copy := *annotations
	copy.Audience = slices.Clone(annotations.Audience)
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
