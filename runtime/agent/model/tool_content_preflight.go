// Package model checks tool content before copying a request or saved message.
// All strings, encoded media and metadata share the existing complete-request
// byte budget. No content block receives a separate allowance.
package model

import (
	"encoding/json"
	"fmt"

	toolcontent "goa.design/goa-ai/runtime/content"
)

// preflightToolContent bounds every mutable field before the content codec may
// allocate decoded media or copied metadata. Only the closed content variants
// can enter this walk; their codec checks required values after the byte check.
func preflightToolContent(blocks toolcontent.Blocks, walk *dynamicValueWalk) error {
	if err := walk.checkChildren(len(blocks)); err != nil {
		return err
	}
	for index, block := range blocks {
		if err := preflightToolContentBlock(block, walk); err != nil {
			return fmt.Errorf("block %d: %w", index, err)
		}
	}
	return blocks.Validate()
}

// preflightToolContentBlock charges the selected block's text, media and nested
// descriptions before any clone or base64 decoder allocates their copies.
func preflightToolContentBlock(block toolcontent.ContentBlock, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	var annotations *toolcontent.Annotations
	var meta json.RawMessage
	var values []string
	switch value := block.(type) {
	case *toolcontent.TextContent:
		if value == nil {
			return fmt.Errorf("text content is nil")
		}
		annotations, meta = value.Annotations, value.Meta
		values = []string{value.Text}
	case *toolcontent.ImageContent:
		if value == nil {
			return fmt.Errorf("image content is nil")
		}
		annotations, meta = value.Annotations, value.Meta
		values = []string{value.Data, value.MIMEType}
	case *toolcontent.AudioContent:
		if value == nil {
			return fmt.Errorf("audio content is nil")
		}
		annotations, meta = value.Annotations, value.Meta
		values = []string{value.Data, value.MIMEType}
	case *toolcontent.ResourceLink:
		if value == nil {
			return fmt.Errorf("resource link is nil")
		}
		annotations, meta = value.Annotations, value.Meta
		values = []string{value.Name, value.URI}
		for _, text := range []*string{value.Title, value.Description, value.MIMEType} {
			if text != nil {
				if err := chargeString(walk, *text); err != nil {
					return err
				}
			}
		}
		if err := walk.checkChildren(len(value.Icons)); err != nil {
			return err
		}
		for _, icon := range value.Icons {
			if err := preflightToolContentIcon(icon, walk); err != nil {
				return err
			}
		}
	case *toolcontent.EmbeddedResource:
		if value == nil {
			return fmt.Errorf("embedded resource is nil")
		}
		annotations, meta = value.Annotations, value.Meta
		if err := preflightToolContentResource(value.Resource, walk); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported content block %T", block)
	}
	for _, value := range values {
		if err := chargeString(walk, value); err != nil {
			return err
		}
	}
	if err := chargeJSON(walk, meta); err != nil {
		return err
	}
	if annotations == nil {
		return nil
	}
	if err := walk.visit(); err != nil {
		return err
	}
	if err := walk.checkChildren(len(annotations.Audience)); err != nil {
		return err
	}
	for _, role := range annotations.Audience {
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, string(role)); err != nil {
			return err
		}
	}
	if annotations.LastModified != nil {
		return chargeString(walk, *annotations.LastModified)
	}
	return nil
}

// preflightToolContentIcon bounds one retained icon description without opening
// its address. Each suggested size is charged to the same request budget.
func preflightToolContentIcon(icon toolcontent.Icon, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, icon.Src); err != nil {
		return err
	}
	for _, value := range []*string{icon.MIMEType, icon.Theme} {
		if value != nil {
			if err := chargeString(walk, *value); err != nil {
				return err
			}
		}
	}
	if err := walk.checkChildren(len(icon.Sizes)); err != nil {
		return err
	}
	for _, size := range icon.Sizes {
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, size); err != nil {
			return err
		}
	}
	return nil
}

// preflightToolContentResource charges one embedded text or blob and its raw
// metadata. The resource remains data supplied by the tool, never a fetch.
func preflightToolContentResource(resource toolcontent.ResourceContents, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	var uri, data string
	var mime *string
	var meta json.RawMessage
	switch value := resource.(type) {
	case *toolcontent.TextResourceContents:
		if value == nil {
			return fmt.Errorf("text resource is nil")
		}
		uri, data, mime, meta = value.URI, value.Text, value.MIMEType, value.Meta
	case *toolcontent.BlobResourceContents:
		if value == nil {
			return fmt.Errorf("blob resource is nil")
		}
		uri, data, mime, meta = value.URI, value.Blob, value.MIMEType, value.Meta
	default:
		return fmt.Errorf("unsupported resource contents %T", resource)
	}
	if err := chargeString(walk, uri); err != nil {
		return err
	}
	if err := chargeString(walk, data); err != nil {
		return err
	}
	if mime != nil {
		if err := chargeString(walk, *mime); err != nil {
			return err
		}
	}
	return chargeJSON(walk, meta)
}
