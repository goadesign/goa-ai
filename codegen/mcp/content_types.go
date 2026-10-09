// Package codegen defines the five MCP content variants shared by generated tool and
// prompt clients. MCP keeps variant fields beside the type discriminator, so Goa
// declares their types here and the MCP decoder checks which fields are required.
//
//nolint:lll // Complete field literals keep the MCP wire contract visible in one place.
package codegen

import "goa.design/goa/v3/expr"

// buildContentItemType preserves each field defined by MCP's content union.
// The generated decoder checks the selected variant before a caller sees it.
func (b *mcpExprBuilder) buildContentItemType() *expr.AttributeExpr {
	zero := float64(0)
	annotations := b.getOrCreateType("ContentAnnotations", buildContentAnnotationsType)
	icon := b.getOrCreateType("ContentIcon", buildContentIconType)
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Selects the required content fields", Validation: &expr.ValidationExpr{Values: []any{"text", "image", "audio", "resource_link", "resource"}}}},
		{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Text for the text variant, including an empty string"}},
		{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Base64 bytes for image and audio variants"}},
		{Name: "mimeType", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Required format for image or audio; optional format for resource links"}},
		{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Required identifier for resource links"}},
		{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Required address for resource links", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
		{Name: "title", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional resource link display name"}},
		{Name: "description", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional resource link description"}},
		{Name: "size", Attribute: &expr.AttributeExpr{Type: expr.Float64, Description: "Raw bytes in the linked resource before base64 encoding", Validation: &expr.ValidationExpr{Minimum: &zero}}},
		{Name: "icons", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: icon}, NonNullableElems: true}, Description: "Optional resource link icons"}},
		{Name: "resource", Attribute: &expr.AttributeExpr{Type: b.getOrCreateType("ResourceContent", b.buildResourceContentType), Description: "Required text or binary contents for embedded resources"}},
		{Name: "annotations", Attribute: &expr.AttributeExpr{Type: annotations, Description: "Optional audience and importance for this content"}},
		{Name: "_meta", Attribute: contentMetaAttribute()},
	}, Validation: &expr.ValidationExpr{Required: []string{"type"}}}
}

// contentMetaAttribute retains open extension data as encoded JSON. Extension
// keys are not known to Goa; the MCP decoder requires an object when it is present.
func contentMetaAttribute() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type:        expr.Any,
		Description: "Namespaced extension metadata retained without interpreting its fields",
		Meta:        expr.MetaExpr{"struct:field:type": []string{"json.RawMessage", "encoding/json"}, "struct:tag:json": []string{"_meta,omitempty"}},
	}
}

// buildContentAnnotationsType declares the shared audience, importance and time
// hints used by content and resource discovery. Validation applies to one item.
func buildContentAnnotationsType() *expr.AttributeExpr {
	zero, one := float64(0), float64(1)
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "audience", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Values: []any{"user", "assistant"}}}}, Description: "Roles that should see this content"}},
		{Name: "priority", Attribute: &expr.AttributeExpr{Type: expr.Float64, Description: "Importance from zero through one, inclusive, for this content item", Validation: &expr.ValidationExpr{Minimum: &zero, Maximum: &one}}},
		{Name: "lastModified", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Time this content last changed"}},
	}}
}

// buildContentIconType declares the shared image reference and display hints.
// Clients validate and retain the URI; decoding never fetches its image.
func buildContentIconType() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "src", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "URI of the icon; decoding does not fetch it", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
		{Name: "mimeType", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional image MIME type"}},
		{Name: "sizes", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}}, Description: "Suggested dimensions or any for scalable icons"}},
		{Name: "theme", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional light or dark theme", Validation: &expr.ValidationExpr{Values: []any{"light", "dark"}}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"src"}}}
}
