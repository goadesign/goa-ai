//nolint:lll // types builder constructs long composite literals for clarity
package codegen

import (
	"goa.design/goa/v3/expr"
)

// buildMCPTypes creates all MCP protocol type definitions
func (b *mcpExprBuilder) buildMCPTypes() {
	// Core types
	b.getOrCreateType("ClientInfo", b.buildClientInfoType)
	b.getOrCreateType("ServerInfo", b.buildServerInfoType)
	b.getOrCreateType("Capabilities", b.buildCapabilitiesType)

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

	// Events stream result type (avoid reusing ToolsCallResult to prevent duplicate method impls).
	// Always define to support events/stream even when no notifications are defined in the design.
	b.getOrCreateType("EventsStreamResult", b.buildEventsStreamResultType)

	// Ensure notification payload type exists; templates may reference it even
	// when no explicit notifications are declared in the design.
	b.getOrCreateType("SendNotificationPayload", b.buildSendNotificationPayloadType)
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
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"protocolVersion", "clientInfo"},
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
				Type:        b.getOrCreateType("ServerCapabilities", b.buildCapabilitiesType),
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

func (b *mcpExprBuilder) buildCapabilitiesType() *expr.AttributeExpr {
	// Create individual capability types as empty structs
	b.getOrCreateType("ToolsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type:        &expr.Object{},
			Description: "Tools capability marker",
		}
	})

	b.getOrCreateType("ResourcesCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type:        &expr.Object{},
			Description: "Resources capability marker",
		}
	})

	b.getOrCreateType("PromptsCapability", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type:        &expr.Object{},
			Description: "Prompts capability marker",
		}
	})

	// Client-side capabilities removed: SamplingCapability, RootsCapability

	// Create ServerCapabilities type with references to capability types
	types := b.Types()
	return b.getOrCreateType("ServerCapabilities", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "tools", Attribute: &expr.AttributeExpr{
					Type:        types["ToolsCapability"],
					Description: "Tool capabilities",
				}},
				{Name: "resources", Attribute: &expr.AttributeExpr{
					Type:        types["ResourcesCapability"],
					Description: "Resource capabilities",
				}},
				{Name: "prompts", Attribute: &expr.AttributeExpr{
					Type:        types["PromptsCapability"],
					Description: "Prompt capabilities",
				}},
				// sampling, roots removed
			},
		}
	}).AttributeExpr
}

func (b *mcpExprBuilder) buildPingResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "pong", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Response to ping",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"pong"},
		},
	}
}

// Tool type builders

func (b *mcpExprBuilder) buildToolsListPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "cursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Pagination cursor",
			}},
		},
	}
}

func (b *mcpExprBuilder) buildToolsListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "tools", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("ToolInfo", b.buildToolInfoType)}},
				Description: "List of available tools",
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
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"name"},
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
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("ContentItem", b.buildContentItemType)}},
				Description: "Tool execution results",
			}},
			{Name: "isError", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether the tool encountered an error",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"content"},
		},
	}
}

// buildEventsStreamResultType is identical to ToolsCallResult but named differently
func (b *mcpExprBuilder) buildEventsStreamResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "content", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("ContentItem", b.buildContentItemType)}},
				Description: "Tool execution results",
			}},
			{Name: "isError", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether the tool encountered an error",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"content"},
		},
	}
}

// (removed) buildTextContentType: unused

func (b *mcpExprBuilder) buildContentItemType() *expr.AttributeExpr {
	return b.buildContentLikeType()
}

// Resource type builders

func (b *mcpExprBuilder) buildResourcesListPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "cursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Pagination cursor",
			}},
		},
	}
}

func (b *mcpExprBuilder) buildResourcesListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "resources", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("ResourceInfo", b.buildResourceInfoType)}},
				Description: "List of available resources",
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
			Required: []string{"uri"},
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
					Pattern: "^[a-zA-Z][a-zA-Z0-9+.-]*:.*",
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
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("ResourceContent", b.buildResourceContentType)}},
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
			{Name: "blob", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Base64 encoded binary content",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri"},
		},
	}
}

// Prompt type builders

func (b *mcpExprBuilder) buildPromptsListPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "cursor", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Pagination cursor",
			}},
		},
	}
}

func (b *mcpExprBuilder) buildPromptsListResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "prompts", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("PromptInfo", b.buildPromptInfoType)}},
				Description: "List of available prompts",
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
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("PromptArgument", b.buildPromptArgumentType)}},
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
				Type:        expr.Any,
				Description: "Prompt arguments",
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

func (b *mcpExprBuilder) buildPromptsGetResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Prompt description",
			}},
			{Name: "messages", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("PromptMessage", b.buildPromptMessageType)}},
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
			Required: []string{"name", "required"},
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
					Values: []any{"user", "assistant", "system"},
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
	return b.buildContentLikeType()
}

// buildContentLikeType defines the shared structure used by ContentItem and MessageContent.
func (b *mcpExprBuilder) buildContentLikeType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Content type",
				Validation:  &expr.ValidationExpr{Values: []any{"text", "image", "resource"}},
			}},
			{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Text content"}},
			{Name: "data", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Base64 encoded data"}},
			{Name: "mimeType", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "MIME type"}},
			{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Resource URI"}},
		},
		Validation: &expr.ValidationExpr{Required: []string{"type"}},
	}
}

// Subscription type builders

func (b *mcpExprBuilder) buildSubscribePayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource URI to subscribe to",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri"},
		},
	}
}

func (b *mcpExprBuilder) buildSubscribeResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "success", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether subscription was successful",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"success"},
		},
	}
}

func (b *mcpExprBuilder) buildUnsubscribePayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Resource URI to unsubscribe from",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"uri"},
		},
	}
}

func (b *mcpExprBuilder) buildUnsubscribeResultType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "success", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether unsubscription was successful",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"success"},
		},
	}
}

// Notification payload builders

// buildSendNotificationPayloadType defines the MCP notification payload with required type
func (b *mcpExprBuilder) buildSendNotificationPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Notification type",
			}},
			{Name: "message", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Notification message",
			}},
			{Name: "data", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Additional data",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"type"},
		},
	}
}
