// Package codegen defines the MCP values that Goa turns into generated service
// and transport types. Clients accept every current content kind; adapters emit
// the results selected from each authored service contract at generation time.
//
//nolint:lll // Type definitions use complete literals so their wire shape is visible in one place.
package codegen

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

// buildMCPTypes creates all MCP protocol type definitions
func (b *mcpExprBuilder) buildMCPTypes() {
	// Core types
	b.getOrCreateType("ServerCapabilities", b.buildServerCapabilitiesType)

	// Tool types
	if len(b.mcp.Tools) > 0 {
		b.getOrCreateType("ToolInfo", b.buildToolInfoType)
		b.getOrCreateType("ContentItem", b.buildContentItemType)
	}

	// Resource types
	if len(b.mcp.Resources)+len(b.mcp.ResourceTemplates) > 0 {
		b.getOrCreateType("ResourceInfo", b.buildResourceInfoType)
		b.getOrCreateType("ResourceContent", b.buildResourceContentType)
	}

	// Prompt types
	if b.hasPrompts() {
		b.getOrCreateType("PromptInfo", b.buildPromptInfoType)
		b.getOrCreateType("PromptArgument", b.buildPromptArgumentType)
		b.getOrCreateType("PromptMessage", b.buildPromptMessageType)
		b.getOrCreateType("ContentItem", b.buildContentItemType)
	}
}

// Core type builders

// buildDiscoverResultType gives callers the server's static capability catalog.
func (b *mcpExprBuilder) buildDiscoverResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "supportedVersions", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}}, Description: "Protocol revisions implemented by this release"}},
			{Name: "capabilities", Attribute: &expr.AttributeExpr{Type: b.getOrCreateType("ServerCapabilities", b.buildServerCapabilitiesType), Description: "Operations declared by this service"}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"supportedVersions", "capabilities"}},
	}
}

func (b *mcpExprBuilder) buildServerCapabilitiesType() *expr.AttributeExpr {
	tools := b.getOrCreateType("ToolsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "listChanged", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Send authorized tool catalog changes through subscriptions/listen"}},
		}, Description: "Tool capabilities"}
	})
	resources := b.getOrCreateType("ResourcesCapability", func() *expr.AttributeExpr {
		fields := expr.Object{
			{Name: "subscribe", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Accept resource update subscriptions through subscriptions/listen"}},
		}
		return &expr.AttributeExpr{Type: &fields, Description: "Resource capabilities"}
	})
	prompts := b.getOrCreateType("PromptsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "listChanged", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Send authorized prompt catalog changes through subscriptions/listen"}},
		}, Description: "Prompt capabilities"}
	})
	completions := b.getOrCreateType("CompletionsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{}, Description: "Argument suggestions"}
	})
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "extensions", Attribute: protocolJSONAttribute("Declared extension identifiers and their open settings objects")},
			{Name: "completions", Attribute: &expr.AttributeExpr{Type: completions, Description: "Declared argument suggestion providers"}},
			{
				Name: "tools",
				Attribute: &expr.AttributeExpr{
					Type:        tools,
					Description: "Tool capabilities",
				},
			},
			{
				Name: "resources",
				Attribute: &expr.AttributeExpr{
					Type:        resources,
					Description: "Resource capabilities",
				},
			},
			{
				Name: "prompts",
				Attribute: &expr.AttributeExpr{
					Type:        prompts,
					Description: "Prompt capabilities",
				},
			},
		},
	}
}

// Tool type builders

func (b *mcpExprBuilder) buildToolsListPayloadType() *expr.AttributeExpr {
	return b.buildListPayloadType()
}

// buildListPayloadType keeps pagination and per-request metadata in the actual
// JSON-RPC params object. Static catalogs reject any supplied cursor.
func (b *mcpExprBuilder) buildListPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "cursor", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Opaque cursor from a prior catalog page"}},
	}}
}

func (b *mcpExprBuilder) buildToolsListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "tools", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("ToolInfo", b.buildToolInfoType)},
					NonNullableElems: true,
				},
				Description: "List of available tools",
			}},
			{Name: "nextCursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Cursor for the next page",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"tools"},
		},
	}
}

func (b *mcpExprBuilder) buildToolInfoType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Tool name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Tool description",
			}},
			{Name: "annotations", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ToolAnnotations", b.buildToolAnnotationsType),
				Description: "Optional behavior hints; clients must trust the server before acting on them",
			}},
			{Name: "inputSchema", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "JSON Schema for tool input",
				Meta: expr.MetaExpr{
					"struct:field:type": []string{"json.RawMessage", "encoding/json"},
				},
			}},
			{Name: "outputSchema", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "JSON Schema for structured tool output",
				Meta: expr.MetaExpr{
					"struct:field:type": []string{"json.RawMessage", "encoding/json"},
				},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name", "inputSchema"},
		},
	}
}

// buildToolAnnotationsType preserves omitted hints and explicit false values
// so each client can apply the defaults defined by the MCP protocol.
func (b *mcpExprBuilder) buildToolAnnotationsType() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "title", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Human-readable tool display name"}},
		{Name: "readOnlyHint", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Whether the tool leaves its environment unchanged; absent means false"}},
		{Name: "destructiveHint", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Whether the tool may remove or replace data; absent means true"}},
		{Name: "idempotentHint", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Whether repeating arguments has no additional effects; absent means false"}},
		{Name: "openWorldHint", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Whether the tool interacts with external entities; absent means true"}},
	}}
}

func (b *mcpExprBuilder) buildToolsCallPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Tool name",
			}},
			{Name: "arguments", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Tool arguments",
				Meta: expr.MetaExpr{
					"struct:field:type": []string{"json.RawMessage", "encoding/json"},
				},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name"},
		},
	}
}

func (b *mcpExprBuilder) buildToolsCallResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "content", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("ContentItem", b.buildContentItemType)},
					NonNullableElems: true,
				},
				Description: "Tool execution results",
				Meta:        expr.MetaExpr{"struct:tag:json": {"content"}},
			}},
			{Name: "isError", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether the tool encountered an error",
			}},
			{Name: "structuredContent", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Structured tool result",
				Meta: expr.MetaExpr{
					"struct:field:type": []string{"json.RawMessage", "encoding/json"},
				},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"content"},
		},
	}
}

// Resource type builders

func (b *mcpExprBuilder) buildResourcesListPayloadType() *expr.AttributeExpr {
	return b.buildListPayloadType()
}

func (b *mcpExprBuilder) buildResourcesListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "resources", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("ResourceInfo", b.buildResourceInfoType)},
					NonNullableElems: true,
				},
				Description: "List of available resources",
			}},
			{Name: "nextCursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Cursor for the next page",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"resources"},
		},
	}
}

func (b *mcpExprBuilder) buildResourceInfoType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource URI",
			}},
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource description",
			}},
			{Name: "mimeType", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource MIME type",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri", "name"},
		},
	}
}

func (b *mcpExprBuilder) buildResourcesReadPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource URI",
				Validation: &expr.ValidationExpr{
					Format:  expr.FormatURI,
					Pattern: mcpexpr.ResourceURIPattern,
				},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri"},
		},
	}
}

func (b *mcpExprBuilder) buildResourcesReadResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "contents", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("ResourceContent", b.buildResourceContentType)},
					NonNullableElems: true,
				},
				Description: "Resource contents",
				Meta:        expr.MetaExpr{"struct:tag:json": {"contents"}},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"contents"},
		},
	}
}

func (b *mcpExprBuilder) buildResourceContentType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource URI",
				Validation:  &expr.ValidationExpr{Format: expr.FormatURI},
			}},
			{Name: "mimeType", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Content MIME type",
			}},
			{Name: "text", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Text content; present only when blob is absent",
			}},
			{Name: "blob", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Base64 binary content; present only when text is absent",
			}},
			{Name: "_meta", Attribute: contentMetaAttribute()},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri"},
		},
	}
}

// Prompt type builders

func (b *mcpExprBuilder) buildPromptsListPayloadType() *expr.AttributeExpr {
	return b.buildListPayloadType()
}

func (b *mcpExprBuilder) buildPromptsListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "prompts", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("PromptInfo", b.buildPromptInfoType)},
					NonNullableElems: true,
				},
				Description: "List of available prompts",
			}},
			{Name: "nextCursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Cursor for the next page",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"prompts"},
		},
	}
}

func (b *mcpExprBuilder) buildPromptInfoType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Prompt name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Prompt description",
			}},
			{Name: "arguments", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("PromptArgument", b.buildPromptArgumentType)},
					NonNullableElems: true,
				},
				Description: "Prompt arguments",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name"},
		},
	}
}

func (b *mcpExprBuilder) buildPromptsGetPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Prompt name",
			}},
			{Name: "arguments", Attribute: &expr.AttributeExpr{
				Type: &expr.Map{
					KeyType:  &expr.AttributeExpr{Type: expr.String},
					ElemType: &expr.AttributeExpr{Type: expr.String},
				},
				Description: "Prompt arguments",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name"},
		},
	}
}

func (b *mcpExprBuilder) buildPromptsGetResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Prompt description",
			}},
			{Name: "messages", Attribute: &expr.AttributeExpr{
				Type: &expr.Array{
					ElemType:         &expr.AttributeExpr{Type: b.getOrCreateType("PromptMessage", b.buildPromptMessageType)},
					NonNullableElems: true,
				},
				Description: "Prompt messages",
				Meta:        expr.MetaExpr{"struct:tag:json": {"messages"}},
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"messages"},
		},
	}
}

func (b *mcpExprBuilder) buildPromptArgumentType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Argument name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Argument description",
			}},
			{Name: "required", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether the argument is required",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name"},
		},
	}
}

func (b *mcpExprBuilder) buildPromptMessageType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "role", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Message role",
				Validation: &expr.ValidationExpr{
					Values: []any{"user", "assistant"},
				},
			}},
			{Name: "content", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ContentItem", b.buildContentItemType),
				Description: "Message content",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"role", "content"},
		},
	}
}
