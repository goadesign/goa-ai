package design

import (
	. "goa.design/goa/v3/dsl"
	_ "goa.design/plugins/v3/mcp" // Import to register the plugin
	mcp "goa.design/plugins/v3/mcp/dsl"
)

var _ = API("assistant", func() {
	Title("AI Assistant API")
	Description("A comprehensive AI assistant service demonstrating all MCP features")
	Version("1.0")
})

var _ = Service("assistant", func() {
	Description("AI Assistant service with full MCP protocol support")

	// Enable MCP for this service
	// Transport is automatically JSON-RPC with SSE support
	// Capabilities are auto-detected from defined tools, resources, etc.
	mcp.MCPServer("assistant-mcp", "1.0.0")

	// Add static prompts to the service
	mcp.StaticPrompt(
		"code_review",
		"Template for code review",
		"system", "You are an expert code reviewer.",
		"user", "Please review this code: {{.code}}",
		"assistant", "I'll analyze the code for quality, bugs, and improvements.",
	)

	mcp.StaticPrompt(
		"explain_concept",
		"Template for explaining concepts",
		"system", "You are a helpful teacher.",
		"user", "Explain {{.concept}} in simple terms.",
	)

	// Enable JSON-RPC (automatically supports SSE via Accept header)
	JSONRPC(func() {
		POST("/rpc")
	})

	// Also expose as HTTP for testing
	HTTP(func() {
		Path("/api")
	})

	// ========== TOOLS ==========
	// Methods exposed as MCP tools for AI to call

	Method("analyze_text", func() {
		Description("Analyze text for sentiment, keywords, or summary")
		Payload(func() {
			Attribute("text", String, "Text to analyze")
			Attribute("mode", String, "Analysis mode", func() {
				Enum("sentiment", "keywords", "summary")
			})
			Required("text", "mode")
		})
		Result(AnalysisResult)

		// Expose as MCP tool
		mcp.Tool("analyze_text", "Analyze text with various modes")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/analyze")
		})
	})

	Method("search_knowledge", func() {
		Description("Search the knowledge base")
		Payload(func() {
			Attribute("query", String, "Search query")
			Attribute("limit", Int, "Maximum results", func() {
				Default(10)
			})
			Required("query")
		})
		Result(ArrayOf(SearchResult))

		// Expose as MCP tool
		mcp.Tool("search", "Search the knowledge base")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/search")
		})
	})

	Method("execute_code", func() {
		Description("Execute code in a sandboxed environment")
		Payload(func() {
			Attribute("language", String, "Programming language")
			Attribute("code", String, "Code to execute")
			Required("language", "code")
		})
		Result(ExecutionResult)

		// Expose as MCP tool with security considerations
		mcp.Tool("execute_code", "Execute code safely in sandbox")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/execute")
		})
	})

	// ========== RESOURCES ==========
	// Methods that provide data as MCP resources

	Method("list_documents", func() {
		Description("List available documents")
		Result(ArrayOf(Document))

		// Expose as MCP resource
		mcp.Resource("documents", "doc://list", "application/json")

		JSONRPC(func() {})
		HTTP(func() {
			GET("/documents")
		})
	})

	Method("get_system_info", func() {
		Description("Get system information and status")
		Result(SystemInfo)

		// Expose as MCP resource
		mcp.Resource("system_info", "system://info", "application/json")

		JSONRPC(func() {})
		HTTP(func() {
			GET("/system")
		})
	})

	Method("get_conversation_history", func() {
		Description("Get conversation history")
		Payload(func() {
			Attribute("limit", Int, "Number of messages", func() {
				Default(50)
			})
		})
		Result(ArrayOf(ChatMessage))

		// Expose as MCP resource
		mcp.Resource("conversation", "conversation://history", "application/json")

		JSONRPC(func() {})
		HTTP(func() {
			GET("/conversation")
		})
	})

	// ========== DYNAMIC PROMPTS ==========
	// Methods that generate prompts dynamically

	Method("generate_prompts", func() {
		Description("Generate context-aware prompts")
		Payload(func() {
			Attribute("context", String, "Current context")
			Attribute("task", String, "Task type")
			Required("context", "task")
		})
		Result(ArrayOf(PromptTemplate))

		// Mark as dynamic prompt generator
		mcp.DynamicPrompt("contextual_prompts", "Generate prompts based on context")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/prompts/generate")
		})
	})

	// ========== SAMPLING (Client Features) ==========
	// Server can request LLM sampling from client

	Method("request_completion", func() {
		Description("Request text completion from client LLM")
		Payload(func() {
			Attribute("messages", ArrayOf(SamplingMessage), "Messages for sampling")
			Attribute("model_preferences", func() {
				Attribute("hints", ArrayOf(ModelHint))
				Attribute("cost_priority", Float64)
				Attribute("speed_priority", Float64)
				Attribute("intelligence_priority", Float64)
			})
			Required("messages")
		})
		Result(func() {
			Attribute("model", String, "Model used")
			Attribute("content", String, "Generated content")
			Attribute("stop_reason", String, "Stop reason")
			Required("model", "content")
		})

		JSONRPC(func() {})
		HTTP(func() {
			POST("/complete")
		})
	})

	// ========== ROOTS (Client Features) ==========
	// Server can query filesystem/URI roots from client

	Method("get_workspace_info", func() {
		Description("Get workspace root directories from client")
		Result(func() {
			Attribute("roots", ArrayOf(RootInfo))
			Required("roots")
		})

		JSONRPC(func() {})
		HTTP(func() {
			GET("/workspace")
		})
	})

	// ========== NOTIFICATIONS ==========
	// Server sends notifications to client

	Method("send_notification", func() {
		Description("Send status notification to client")
		Payload(func() {
			Attribute("type", String, "Notification type", func() {
				Enum("info", "warning", "error", "success")
			})
			Attribute("message", String, "Notification message")
			Attribute("data", Any, "Additional data")
			Required("type", "message")
		})

		// Mark as notification sender
		mcp.Notification(
			"status_update",
			"Send status updates to client",
		)

		JSONRPC(func() {})
		HTTP(func() {
			POST("/notify")
		})
	})

	// ========== SUBSCRIPTIONS ==========
	// Handle resource update subscriptions

	Method("subscribe_to_updates", func() {
		Description("Subscribe to resource updates")
		Payload(func() {
			Attribute("resource", String, "Resource to monitor")
			Attribute("filter", String, "Optional filter")
			Required("resource")
		})
		Result(SubscriptionInfo)

		// Mark as subscription handler
		mcp.Subscription("documents")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/subscribe")
		})
	})

	// ========== PROGRESS TRACKING ==========
	// Long-running operation with progress

	Method("process_batch", func() {
		Description("Process a batch of items with progress tracking")
		Payload(func() {
			Attribute("items", ArrayOf(String), "Items to process")
			Required("items")
		})
		Result(BatchResult)

		// This will report progress via MCP progress notifications
		mcp.Tool("process_batch", "Process items with progress updates")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/batch")
		})
	})

	// ========== STREAMING (SSE Support) ==========
	// Resource subscription updates via SSE
	// When a client subscribes to a resource, it can request SSE updates
	// by setting Accept: text/event-stream header

	Method("monitor_resource_changes", func() {
		Description("Monitor resource changes and send updates via SSE when Accept: text/event-stream is set")
		Payload(func() {
			Attribute("subscription_id", String, "Subscription ID from subscribe_to_updates")
			Attribute("stream", Boolean, "Whether to stream updates (for SSE)", func() {
				Default(false)
			})
			Required("subscription_id")
		})
		Result(func() {
			Attribute("updates", ArrayOf(ResourceUpdate), "Resource updates")
			Required("updates")
		})

		// Mark as subscription monitor - uses SSE when Accept header is set
		mcp.SubscriptionMonitor("resource_changes")

		JSONRPC(func() {})
		HTTP(func() {
			GET("/monitor/{subscription_id}")
			// SSE is automatically enabled when Accept: text/event-stream
		})
	})

	Method("stream_logs", func() {
		Description("Stream server logs in real-time via SSE")
		Payload(func() {
			Attribute("level", String, "Minimum log level", func() {
				Enum("debug", "info", "warning", "error")
				Default("info")
			})
			Attribute("filter", String, "Optional log filter")
		})
		Result(func() {
			Attribute("logs", ArrayOf(LogEntry), "Log entries")
			Required("logs")
		})

		JSONRPC(func() {})
		HTTP(func() {
			GET("/logs")
		})
	})
})

// ========== TYPE DEFINITIONS ==========

var SamplingMessage = Type("SamplingMessage", func() {
	Attribute("role", String, "Message role")
	Attribute("content", String, "Message content")
	Required("role", "content")
})

var ModelHint = Type("ModelHint", func() {
	Attribute("name", String, "Model hint name")
})

var RootInfo = Type("RootInfo", func() {
	Attribute("uri", String, "Root URI")
	Attribute("name", String, "Root name")
	Required("uri")
})

var AnalysisResult = Type("AnalysisResult", func() {
	Attribute("mode", String, "Analysis mode used")
	Attribute("result", Any, "Analysis result (varies by mode)")
	Attribute("confidence", Float64, "Confidence score")
	Attribute("metadata", MapOf(String, Any), "Additional metadata")
	Required("mode", "result")
})

var SearchResult = Type("SearchResult", func() {
	Attribute("id", String, "Result ID")
	Attribute("title", String, "Result title")
	Attribute("content", String, "Result content")
	Attribute("score", Float64, "Relevance score")
	Required("id", "title", "content", "score")
})

var ExecutionResult = Type("ExecutionResult", func() {
	Attribute("output", String, "Execution output")
	Attribute("error", String, "Error message if any")
	Attribute("execution_time", Float64, "Execution time in seconds")
	Required("output", "execution_time")
})

var Document = Type("Document", func() {
	Attribute("id", String, "Document ID")
	Attribute("name", String, "Document name")
	Attribute("type", String, "Document type")
	Attribute("size", Int64, "Size in bytes")
	Attribute("modified", String, "Last modified", func() {
		Format(FormatDateTime)
	})
	Required("id", "name", "type", "size", "modified")
})

var SystemInfo = Type("SystemInfo", func() {
	Attribute("version", String, "System version")
	Attribute("uptime", Int64, "Uptime in seconds")
	Attribute("memory_usage", Float64, "Memory usage percentage")
	Attribute("cpu_usage", Float64, "CPU usage percentage")
	Attribute("active_connections", Int, "Number of active connections")
	Required("version", "uptime", "memory_usage", "cpu_usage", "active_connections")
})

var ChatMessage = Type("ChatMessage", func() {
	Attribute("id", String, "Message ID")
	Attribute("role", String, "Message role", func() {
		Enum("user", "assistant", "system")
	})
	Attribute("content", String, "Message content")
	Attribute("timestamp", String, "Message timestamp", func() {
		Format(FormatDateTime)
	})
	Required("id", "role", "content", "timestamp")
})

var PromptTemplate = Type("PromptTemplate", func() {
	Attribute("name", String, "Template name")
	Attribute("description", String, "Template description")
	Attribute("variables", ArrayOf(String), "Required variables")
	Attribute("template", String, "Template content")
	Required("name", "description", "template")
})

var SubscriptionInfo = Type("SubscriptionInfo", func() {
	Attribute("subscription_id", String, "Subscription ID")
	Attribute("resource", String, "Subscribed resource")
	Attribute("created_at", String, "Subscription created", func() {
		Format(FormatDateTime)
	})
	Required("subscription_id", "resource", "created_at")
})

var BatchResult = Type("BatchResult", func() {
	Attribute("processed", Int, "Number of items processed")
	Attribute("failed", Int, "Number of items failed")
	Attribute("results", ArrayOf(Any), "Processing results")
	Required("processed", "failed", "results")
})

var ResourceUpdate = Type("ResourceUpdate", func() {
	Attribute("update_id", String, "Update ID")
	Attribute("resource", String, "Resource that changed")
	Attribute("event_type", String, "Type of change", func() {
		Enum("created", "updated", "deleted")
	})
	Attribute("data", Any, "Update data")
	Attribute("timestamp", String, "Update timestamp", func() {
		Format(FormatDateTime)
	})
	Required("update_id", "resource", "event_type", "timestamp")
})

var LogEntry = Type("LogEntry", func() {
	Attribute("timestamp", String, "Log timestamp", func() {
		Format(FormatDateTime)
	})
	Attribute("level", String, "Log level", func() {
		Enum("debug", "info", "warning", "error")
	})
	Attribute("message", String, "Log message")
	Attribute("data", MapOf(String, Any), "Additional log data")
	Required("timestamp", "level", "message")
})
