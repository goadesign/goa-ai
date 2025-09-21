package design

import (
	_ "goa.design/goa-ai" // Import to register the plugin
	mcp "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
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
		Result(SearchResults)

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
		Result(Documents)

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
		Result(ChatMessages)

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
		Result(PromptTemplates)

		// Mark as dynamic prompt generator
		mcp.DynamicPrompt("contextual_prompts", "Generate prompts based on context")

		JSONRPC(func() {})
		HTTP(func() {
			POST("/prompts/generate")
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
			// Optional output shaping for streaming scenarios
			Attribute("format", String, "Output format", func() { Enum("text", "blob", "uri") })
			Attribute("blob", Bytes, "Blob data when format=blob")
			Attribute("uri", String, "URI to include when format=uri")
			Attribute("mimeType", String, "Mime type for blob/uri")
			Required("items")
		})
		Result(BatchResult)
		StreamingResult(BatchResult)

		// This will report progress via MCP progress notifications
		mcp.Tool("process_batch", "Process items with progress updates")

		JSONRPC(func() {
			ServerSentEvents()
		})
		HTTP(func() {
			POST("/batch")
			ServerSentEvents()
		})
	})

})

// ========== STREAMING SERVICE (HTTP/SSE) ==========
// Service for testing HTTP streaming with SSE
var _ = Service("streaming", func() {
	Description("Service for testing HTTP streaming features")

	// Server streaming with SSE
	Method("stream_events", func() {
		Description("Stream events from server to client using SSE")
		Payload(func() {
			Attribute("category", String, "Event category", func() {
				Enum("system", "user", "application")
			})
			Attribute("filter", String, "Optional filter")
			Required("category")
		})
		StreamingResult(EventUpdate)

		HTTP(func() {
			GET("/stream/events/{category}")
			Params(func() {
				Param("category")
				Param("filter")
			})
		})
	})

	// Another server streaming example
	Method("stream_logs", func() {
		Description("Stream logs using SSE")
		Payload(func() {
			Attribute("level", String, "Log level", func() {
				Enum("debug", "info", "warning", "error")
				Default("info")
			})
		})
		StreamingResult(LogEntry)

		HTTP(func() {
			GET("/stream/logs")
			Params(func() {
				Param("level")
			})
		})
	})

	// Monitor resource changes with SSE
	Method("monitor_resource_changes", func() {
		Description("Monitor resource changes with server streaming")
		Payload(func() {
			Attribute("resource_type", String, "Type of resource to monitor")
			Attribute("filter", String, "Optional filter")
			Required("resource_type")
		})
		StreamingResult(ResourceUpdate)

		HTTP(func() {
			GET("/stream/monitor/{resource_type}")
			Params(func() {
				Param("resource_type")
				Param("filter")
			})
		})
	})

	// Flexible data streaming with SSE
	Method("flexible_data", func() {
		Description("Flexible data streaming")
		Payload(func() {
			Attribute("data_type", String, "Type of data")
			Attribute("streaming", Boolean, "Enable streaming", func() {
				Default(true)
			})
		})
		StreamingResult(DataUpdate)

		HTTP(func() {
			GET("/stream/data/{data_type}")
			Params(func() {
				Param("data_type")
				Param("streaming")
			})
		})
	})
})

// ========== WEBSOCKET SERVICE ==========
// Service for WebSocket streaming
var _ = Service("websocket", func() {
	Description("Service for testing WebSocket streaming")

	// Client streaming
	Method("upload_chunks", func() {
		Description("Upload data chunks via client stream")
		StreamingPayload(DocumentChunk)
		Result(func() {
			Attribute("total_size", Int, "Total size")
			Attribute("chunk_count", Int, "Number of chunks")
			Required("total_size", "chunk_count")
		})

		HTTP(func() {
			POST("/ws/upload")
		})
	})

	// Client streaming for documents
	Method("upload_documents", func() {
		Description("Upload multiple documents via client stream")
		StreamingPayload(DocumentChunk)
		Result(func() {
			Attribute("total_size", Int, "Total size")
			Attribute("document_count", Int, "Number of documents")
			Required("total_size", "document_count")
		})

		HTTP(func() {
			POST("/ws/upload_documents")
		})
	})

	// Bidirectional streaming
	Method("chat", func() {
		Description("Interactive chat with bidirectional streaming")
		StreamingPayload(ChatInput)
		StreamingResult(ChatResponse)

		HTTP(func() {
			POST("/ws/chat")
		})
	})

	// Another bidirectional streaming
	Method("interactive_chat", func() {
		Description("Extended interactive chat with bidirectional streaming")
		StreamingPayload(ChatInput)
		StreamingResult(ChatResponse)

		HTTP(func() {
			POST("/ws/interactive_chat")
		})
	})
})

// ========== GRPC STREAMING SERVICE ==========
// Service for testing gRPC streaming
var _ = Service("grpcstream", func() {
	Description("Service for testing gRPC streaming")

	// Server streaming
	Method("list_items", func() {
		Description("List items with server streaming")
		Payload(func() {
			Field(1, "filter", String, "Filter criteria")
		})
		StreamingResult(func() {
			Field(1, "id", String, "Item ID")
			Field(2, "name", String, "Item name")
			Required("id", "name")
		})

		GRPC(func() {})
	})

	// Client streaming
	Method("collect_metrics", func() {
		Description("Collect metrics via client stream")
		StreamingPayload(func() {
			Field(1, "metric", String, "Metric name")
			Field(2, "value", Float64, "Metric value")
			Required("metric", "value")
		})
		Result(func() {
			Field(1, "count", Int, "Total metrics received")
			Field(2, "average", Float64, "Average value")
			Required("count", "average")
		})

		GRPC(func() {})
	})

	// Bidirectional streaming
	Method("echo", func() {
		Description("Echo service with bidirectional streaming")
		StreamingPayload(func() {
			Field(1, "message", String, "Message to echo")
			Required("message")
		})
		StreamingResult(func() {
			Field(1, "echo", String, "Echoed message")
			Field(2, "timestamp", String, "Echo timestamp")
			Required("echo", "timestamp")
		})

		GRPC(func() {})
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

// Streaming type definitions
var EventUpdate = Type("EventUpdate", func() {
	Attribute("event_id", String, "Event ID")
	Attribute("category", String, "Event category")
	Attribute("type", String, "Event type")
	Attribute("data", Any, "Event data")
	Attribute("timestamp", String, "Event timestamp", func() {
		Format(FormatDateTime)
	})
	Required("event_id", "category", "type", "timestamp")
})

var DocumentChunk = Type("DocumentChunk", func() {
	Attribute("document_id", String, "Document ID")
	Attribute("chunk_index", Int, "Chunk index")
	Attribute("total_chunks", Int, "Total number of chunks")
	Attribute("data", Bytes, "Chunk data")
	Attribute("metadata", MapOf(String, Any), "Document metadata")
	Required("document_id", "chunk_index", "data")
})

var ChatInput = Type("ChatInput", func() {
	Attribute("message", String, "User message")
	Attribute("context", MapOf(String, Any), "Additional context")
	Attribute("stream_control", String, "Stream control command", func() {
		Enum("continue", "pause", "stop")
	})
	Required("message")
})

var ChatResponse = Type("ChatResponse", func() {
	Attribute("response", String, "Assistant response")
	Attribute("thinking", Boolean, "Whether assistant is still thinking")
	Attribute("metadata", MapOf(String, Any), "Response metadata")
	Required("response", "thinking")
})

var DataResponse = Type("DataResponse", func() {
	Attribute("data", Any, "Response data")
	Attribute("total_count", Int, "Total count of items")
	Attribute("metadata", MapOf(String, Any), "Response metadata")
	Required("data")
})

var DataUpdate = Type("DataUpdate", func() {
	Attribute("update_id", String, "Update ID")
	Attribute("data", Any, "Update data")
	Attribute("sequence", Int, "Update sequence number")
	Attribute("final", Boolean, "Whether this is the final update")
	Required("update_id", "data", "sequence", "final")
})

var SearchResults = Type("SearchResults", ArrayOf(SearchResult))
var Documents = Type("Documents", ArrayOf(Document))
var ChatMessages = Type("ChatMessages", ArrayOf(ChatMessage))
var PromptTemplates = Type("PromptTemplates", ArrayOf(PromptTemplate))
