// Package codegen defines the MCP values that Goa turns into generated service
// and transport types. The generator emits only the protocol branches that a Goa
// service can produce, so generated runtime code does not inspect content kinds.
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
	b.getOrCreateType("ClientInfo", b.buildClientInfoType)
	b.getOrCreateType("ServerInfo", b.buildServerInfoType)
	b.getOrCreateType("ClientCapabilities", b.buildClientCapabilitiesType)
	b.getOrCreateType("ServerCapabilities", b.buildServerCapabilitiesType)

	// Tool types
	if len(b.mcp.Tools) > 0 {
		b.getOrCreateType("ToolInfo", b.buildToolInfoType)
		b.getOrCreateType("ContentItem", b.buildContentItemType)
	}

	// Resource types
	if len(b.mcp.Resources) > 0 {
		b.getOrCreateType("ResourceInfo", b.buildResourceInfoType)
		b.getOrCreateType("ResourceContent", b.buildResourceContentType)
	}

	// Prompt types
	if b.hasPrompts() {
		b.getOrCreateType("PromptInfo", b.buildPromptInfoType)
		b.getOrCreateType("PromptArgument", b.buildPromptArgumentType)
		b.getOrCreateType("PromptMessage", b.buildPromptMessageType)
		b.getOrCreateType("MessageContent", b.buildMessageContentType)
	}
}

// Core type builders

func (b *mcpExprBuilder) buildInitializePayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "protocolVersion", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "MCP protocol version",
			}},
			{Name: "clientInfo", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ClientInfo", b.buildClientInfoType),
				Description: "Client information",
			}},
			{Name: "capabilities", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ClientCapabilities", b.buildClientCapabilitiesType),
				Description: "Client capabilities",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"protocolVersion", "clientInfo", "capabilities"},
		},
	}
}

func (b *mcpExprBuilder) buildInitializeResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "protocolVersion", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "MCP protocol version",
			}},
			{Name: "capabilities", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ServerCapabilities", b.buildServerCapabilitiesType),
				Description: "Server capabilities",
			}},
			{Name: "serverInfo", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("ServerInfo", b.buildServerInfoType),
				Description: "Server information",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"protocolVersion", "capabilities", "serverInfo"},
		},
	}
}

func (b *mcpExprBuilder) buildClientInfoType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Client name",
			}},
			{Name: "version", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Client version",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name", "version"},
		},
	}
}

func (b *mcpExprBuilder) buildServerInfoType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Server name",
			}},
			{Name: "version", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Server version",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name", "version"},
		},
	}
}

func (b *mcpExprBuilder) buildClientCapabilitiesType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type:        &expr.Object{},
		Description: "Capabilities implemented by this client",
	}
}

func (b *mcpExprBuilder) buildServerCapabilitiesType() *expr.AttributeExpr {
	tools := b.getOrCreateType("ToolsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{}, Description: "Tool capabilities"}
	})
	resources := b.getOrCreateType("ResourcesCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{}, Description: "Resource capabilities"}
	})
	prompts := b.getOrCreateType("PromptsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{}, Description: "Prompt capabilities"}
	})
	return &expr.AttributeExpr{
		Type: &expr.Object{
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

func (b *mcpExprBuilder) buildPingResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{}}
}

// Tool type builders

func (b *mcpExprBuilder) buildToolsListPayloadType() *expr.AttributeExpr {
	return b.buildListPayloadType()
}

// buildListPayloadType keeps the service payload distinct from the optional
// JSON-RPC params object. Goa can then omit params or decode the object when it
// is present.
func (b *mcpExprBuilder) buildListPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "params", Attribute: &expr.AttributeExpr{
				Type: b.getOrCreateType("PaginatedRequestParams", func() *expr.AttributeExpr {
					return &expr.AttributeExpr{
						Type: &expr.Object{
							{Name: "cursor", Attribute: &expr.AttributeExpr{
								Type:        expr.String,
								Description: "Pagination cursor",
							}},
						},
						Description: "Optional pagination parameters",
					}
				}),
				Description: "Request parameters when pagination is used",
			}},
		},
	}
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

func (b *mcpExprBuilder) buildContentItemType() *expr.AttributeExpr {
	return b.buildTextContentType()
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
			}},
			{Name: "mimeType", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Content MIME type",
			}},
			{Name: "text", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Text content",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri", "text"},
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
				Type:        b.getOrCreateType("MessageContent", b.buildMessageContentType),
				Description: "Message content",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"role", "content"},
		},
	}
}

func (b *mcpExprBuilder) buildMessageContentType() *expr.AttributeExpr {
	return b.buildTextContentType()
}

// buildTextContentType defines the text content emitted by generated Goa tools
// and prompts. Other MCP content branches require application data that Goa-AI
// does not currently expose in authored service results.
func (b *mcpExprBuilder) buildTextContentType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Content type",
				Validation:  &expr.ValidationExpr{Values: []any{"text"}},
			}},
			{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Text content"}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"type", "text"}},
	}
}
