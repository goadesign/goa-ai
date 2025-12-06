package codegen

import "goa.design/goa/v3/expr"

// buildA2ATypes creates all A2A protocol type definitions
func (b *a2aExprBuilder) buildA2ATypes() {
	// Core types
	b.getOrCreateType("TaskMessage", b.buildTaskMessageType)
	b.getOrCreateType("MessagePart", b.buildMessagePartType)
	b.getOrCreateType("TaskStatus", b.buildTaskStatusType)
	b.getOrCreateType("Artifact", b.buildArtifactType)
	b.getOrCreateType("A2ASkill", b.buildA2ASkillType)
	b.getOrCreateType("A2ASecurityScheme", b.buildA2ASecuritySchemeType)
}

// Payload type builders

func (b *a2aExprBuilder) buildSendTaskPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "id", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Unique task identifier",
			}},
			{Name: "message", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("TaskMessage", b.buildTaskMessageType),
				Description: "Task message",
			}},
			{Name: "sessionId", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Optional session identifier for multi-turn conversations",
			}},
			{Name: "metadata", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Optional task metadata",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"id", "message"},
		},
	}
}

func (b *a2aExprBuilder) buildGetTaskPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "id", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Task identifier",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"id"},
		},
	}
}

func (b *a2aExprBuilder) buildCancelTaskPayloadType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "id", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Task identifier to cancel",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"id"},
		},
	}
}

// Response type builders

func (b *a2aExprBuilder) buildTaskResponseType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "id", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Task identifier",
			}},
			{Name: "status", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("TaskStatus", b.buildTaskStatusType),
				Description: "Task status",
			}},
			{Name: "artifacts", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("Artifact", b.buildArtifactType)}},
				Description: "Task output artifacts",
			}},
			{Name: "history", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("TaskMessage", b.buildTaskMessageType)}},
				Description: "Conversation history",
			}},
			{Name: "metadata", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Optional response metadata",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"id", "status"},
		},
	}
}

func (b *a2aExprBuilder) buildTaskEventType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Event type (status, artifact, message)",
				Validation:  &expr.ValidationExpr{Values: []any{"status", "artifact", "message", "error"}},
			}},
			{Name: "taskId", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Task identifier",
			}},
			{Name: "status", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("TaskStatus", b.buildTaskStatusType),
				Description: "Task status (for status events)",
			}},
			{Name: "artifact", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("Artifact", b.buildArtifactType),
				Description: "Artifact (for artifact events)",
			}},
			{Name: "message", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("TaskMessage", b.buildTaskMessageType),
				Description: "Message (for message events)",
			}},
			{Name: "final", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether this is the final event",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"type", "taskId"},
		},
	}
}

func (b *a2aExprBuilder) buildAgentCardResponseType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "protocolVersion", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "A2A protocol version",
			}},
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Agent name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Agent description",
			}},
			{Name: "url", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Agent endpoint URL",
			}},
			{Name: "version", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Agent version",
			}},
			{Name: "capabilities", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Agent capabilities",
			}},
			{Name: "defaultInputModes", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}},
				Description: "Default input content types",
			}},
			{Name: "defaultOutputModes", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}},
				Description: "Default output content types",
			}},
			{Name: "skills", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("A2ASkill", b.buildA2ASkillType)}},
				Description: "Agent skills",
			}},
			{Name: "securitySchemes", Attribute: &expr.AttributeExpr{
				Type:        &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("A2ASecurityScheme", b.buildA2ASecuritySchemeType)}},
				Description: "Security scheme definitions",
			}},
			{Name: "security", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Security requirements",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"protocolVersion", "name", "url"},
		},
	}
}

// Core type builders

func (b *a2aExprBuilder) buildTaskMessageType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "role", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Message role (user, assistant, system)",
				Validation:  &expr.ValidationExpr{Values: []any{"user", "assistant", "system"}},
			}},
			{Name: "parts", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("MessagePart", b.buildMessagePartType)}},
				Description: "Message content parts",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"role", "parts"},
		},
	}
}

func (b *a2aExprBuilder) buildMessagePartType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Part type (text, data, file)",
				Validation:  &expr.ValidationExpr{Values: []any{"text", "data", "file"}},
			}},
			{Name: "text", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Text content (when type is text)",
			}},
			{Name: "data", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Structured data content (when type is data)",
			}},
			{Name: "mimeType", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "MIME type for file content",
			}},
			{Name: "uri", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "URI for file content",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"type"},
		},
	}
}

func (b *a2aExprBuilder) buildTaskStatusType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "state", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Task state",
				Validation:  &expr.ValidationExpr{Values: []any{"submitted", "working", "input-required", "completed", "failed", "canceled"}},
			}},
			{Name: "message", Attribute: &expr.AttributeExpr{
				Type:        b.getOrCreateType("TaskMessage", b.buildTaskMessageType),
				Description: "Optional status message",
			}},
			{Name: "timestamp", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Status update timestamp",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"state"},
		},
	}
}

func (b *a2aExprBuilder) buildArtifactType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Artifact name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Artifact description",
			}},
			{Name: "parts", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("MessagePart", b.buildMessagePartType)}},
				Description: "Artifact content parts",
			}},
			{Name: "index", Attribute: &expr.AttributeExpr{
				Type:        expr.Int,
				Description: "Artifact index",
			}},
			{Name: "append", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether this artifact appends to a previous one",
			}},
			{Name: "lastChunk", Attribute: &expr.AttributeExpr{
				Type:        expr.Boolean,
				Description: "Whether this is the last chunk of a streaming artifact",
			}},
			{Name: "metadata", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "Optional artifact metadata",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"parts"},
		},
	}
}

func (b *a2aExprBuilder) buildA2ASkillType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "id", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Skill identifier",
			}},
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Skill name",
			}},
			{Name: "description", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Skill description",
			}},
			{Name: "tags", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}},
				Description: "Skill tags",
			}},
			{Name: "inputModes", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}},
				Description: "Input content types",
			}},
			{Name: "outputModes", Attribute: &expr.AttributeExpr{
				Type:        &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}},
				Description: "Output content types",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"id", "name"},
		},
	}
}

func (b *a2aExprBuilder) buildA2ASecuritySchemeType() *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Security scheme type (http, apiKey, oauth2)",
			}},
			{Name: "scheme", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "HTTP authentication scheme (bearer, basic)",
			}},
			{Name: "in", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "API key location (header, query)",
			}},
			{Name: "name", Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "API key parameter name",
			}},
			{Name: "flows", Attribute: &expr.AttributeExpr{
				Type:        expr.Any,
				Description: "OAuth2 flow configurations",
			}},
		},
		Validation: &expr.ValidationExpr{
			Required: []string{"type"},
		},
	}
}
