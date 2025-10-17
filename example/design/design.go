// Package design defines the Goa design for the example AI assistant service.
// It models a realistic, simple MCP surface with validated payloads and types.
package design

import (
	_ "goa.design/goa-ai" // Import to register the plugin
	mcp "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("assistant", func() {
	Title("AI Assistant API")
	Description("Simple MCP example exposing a few tools, resources, and prompts")
	Version("1.0")
})

var _ = Service("assistant", func() {
	Description("AI Assistant service with full MCP protocol support")

	// Enable MCP for this service
	// Transport is automatically JSON-RPC with SSE support
	// Capabilities are auto-detected from defined tools, resources, etc.
	mcp.MCPServer("assistant-mcp", "1.0.0", mcp.ProtocolVersion("2025-06-18"))

	// Add static prompts to the service
	mcp.StaticPrompt(
		"code_review",
		"Template for code review",
		"system", "You are an expert code reviewer.",
		"user", "Please review this code: {{.code}}",
		"assistant", "I'll analyze the code for quality, bugs, and improvements.",
	)

	// Enable JSON-RPC (automatically supports SSE via Accept header)
	JSONRPC(func() {
		POST("/rpc")
	})

	// ========== TOOLS ==========
	// Methods exposed as MCP tools for AI to call

	Method("analyze_text", func() {
		Description("Analyze text for sentiment, keywords, or summary")
		Payload(func() {
			Attribute("text", String, "Text to analyze", func() {
				MinLength(1)
				MaxLength(10000)
				Example("I love this new feature! It works perfectly.")
			})
			Attribute("mode", String, "Analysis mode", func() {
				Enum("sentiment", "keywords", "summary")
				Example("sentiment")
			})
			Required("text", "mode")
		})
		Result(AnalysisResult)

		// Expose as MCP tool
		mcp.Tool("analyze_text", "Analyze text with various modes")

		JSONRPC(func() {})
	})

	Method("search_knowledge", func() {
		Description("Search the knowledge base")
		Payload(func() {
			Attribute("query", String, "Search query", func() {
				MinLength(1)
				MaxLength(256)
				Example("MCP protocol")
			})
			Attribute("limit", Int, "Maximum results", func() {
				Default(10)
				Minimum(1)
				Maximum(100)
				Example(5)
			})
			Required("query")
		})
		Result(SearchResults)

		// Expose as MCP tool
		mcp.Tool("search", "Search the knowledge base")

		JSONRPC(func() {})
	})

	Method("execute_code", func() {
		Description("Execute code in a sandboxed environment")
		Payload(func() {
			Attribute("language", String, "Programming language", func() {
				Enum("python", "javascript", "go")
				Example("python")
			})
			Attribute("code", String, "Code to execute", func() {
				MinLength(1)
				MaxLength(20000)
				Example("print(2 + 2)")
			})
			Required("language", "code")
		})
		Result(ExecutionResult)

		// Expose as MCP tool with security considerations
		mcp.Tool("execute_code", "Execute code safely in sandbox")

		JSONRPC(func() {})
	})

	// ========== RESOURCES ==========
	// Methods that provide data as MCP resources

	Method("list_documents", func() {
		Description("List available documents")
		Result(Documents)

		// Expose as MCP resource
		mcp.Resource("documents", "doc://list", "application/json")

		JSONRPC(func() {})
	})

	Method("get_system_info", func() {
		Description("Get system information and status")
		Result(SystemInfo)

		// Expose as MCP resource
		mcp.Resource("system_info", "system://info", "application/json")

		JSONRPC(func() {})
	})

	// Additional conversation resource for tests
	Method("get_conversation_history", func() {
		Description("Get conversation history with optional filtering")
		Payload(func() {
			Attribute("limit", Int, "Maximum items")
			Attribute("flag", Boolean, "Flag example")
			Attribute("nums", ArrayOf(Any), "Numbers")
		})
		Result(ConversationHistory)

		// Expose as MCP resource
		mcp.Resource("conversation_history", "conversation://history", "application/json")

		JSONRPC(func() {})
	})

	// ========== DYNAMIC PROMPTS ==========
	// Methods that generate prompts dynamically

	Method("generate_prompts", func() {
		Description("Generate context-aware prompts")
		Payload(func() {
			Attribute("context", String, "Current context", func() { Example("testing") })
			Attribute("task", String, "Task type", func() { Example("unit-test") })
			Required("context", "task")
		})
		Result(PromptTemplates)

		// Mark as dynamic prompt generator
		mcp.DynamicPrompt("contextual_prompts", "Generate prompts based on context")

		JSONRPC(func() {})
	})

	// ========== NOTIFICATIONS ==========
	// Server sends notifications to client

	Method("send_notification", func() {
		Description("Send status notification to client")
		Payload(func() {
			Attribute("type", String, "Notification type", func() {
				Enum("info", "warning", "error", "success")
			})
			Attribute("message", String, "Notification message", func() {
				MinLength(1)
				MaxLength(2000)
				Example("Testing notification")
			})
			Attribute("data", Any, "Additional data")
			Required("type", "message")
		})

		// Mark as notification sender
		mcp.Notification(
			"status_update",
			"Send status updates to client",
		)

		JSONRPC(func() {})
	})

	// ========== SUBSCRIPTIONS ==========
	// Handle resource update subscriptions

	Method("subscribe_to_updates", func() {
		Description("Subscribe to resource updates")
		Payload(func() {
			Attribute("resource", String, "Resource to monitor", func() {
				Enum("documents", "conversation", "system_info")
				Example("documents")
			})
			Attribute("filter", String, "Optional filter")
			Required("resource")
		})
		Result(SubscriptionInfo)

		// Mark as subscription handler
		mcp.Subscription("documents")

		JSONRPC(func() {})
	})

	// ========== PROGRESS TRACKING ==========
	// Long-running operation with progress

	Method("process_batch", func() {
		Description("Process a batch of items with progress tracking")
		Payload(func() {
			Attribute("items", ArrayOf(String), "Items to process", func() {
				MinLength(1)
				Example([]string{"item1", "item2"})
			})
			// Optional output shaping for streaming scenarios
			Attribute("format", String, "Output format", func() { Enum("text", "blob", "uri"); Example("text") })
			Attribute("blob", Bytes, "Blob data when format=blob", func() { Example([]byte("hello")) })
			Attribute("uri", String, "URI to include when format=uri", func() { Format(FormatURI); Example("system://info") })
			Attribute("mimeType", String, "Mime type for blob/uri", func() { Example("text/plain") })
			Required("items")
		})
		Result(BatchResult)
		StreamingResult(BatchResult)

		// This will report progress via MCP progress notifications
		mcp.Tool("process_batch", "Process items with progress updates")

		JSONRPC(func() {
			ServerSentEvents()
		})
	})

})

// (Removed streaming, websocket, and grpc streaming example services to keep the MCP example minimal)

// ========== TYPE DEFINITIONS ==========

// AnalysisResult contains the outcome of text analysis.
var AnalysisResult = Type("AnalysisResult", func() {
	Attribute("mode", String, "Analysis mode used", func() {
		Enum("sentiment", "keywords", "summary")
		Example("sentiment")
	})
	Attribute("result", String, "Analysis result (summary, keywords or sentiment)", func() {
		MinLength(1)
		Example("positive")
	})
	Attribute("confidence", Float64, "Confidence score (0–1)", func() {
		Minimum(0)
		Maximum(1)
		Example(0.98)
	})
	Attribute("metadata", MapOf(String, Any), "Additional metadata")
	Required("mode", "result")
})

// SearchResult represents a single search match.
var SearchResult = Type("SearchResult", func() {
	Attribute("id", String, "Result ID", func() { Example("doc-1") })
	Attribute("title", String, "Result title", func() { Example("MCP Specification Overview") })
	Attribute("content", String, "Result content", func() { MaxLength(10000); Example("The Model Context Protocol (MCP) defines...") })
	Attribute("score", Float64, "Relevance score (0–1)", func() { Minimum(0); Maximum(1); Example(0.87) })
	Required("id", "title", "content", "score")
})

// ExecutionResult reports the outcome of code execution.
var ExecutionResult = Type("ExecutionResult", func() {
	Attribute("output", String, "Execution output", func() { Example("4") })
	Attribute("error", String, "Error message if any", func() { Example("") })
	Attribute("execution_time", Float64, "Execution time in seconds", func() { Minimum(0); Example(0.01) })
	Required("output", "execution_time")
})

// Document describes a document available via resources.
var Document = Type("Document", func() {
	Attribute("id", String, "Document ID", func() { Example("doc-1") })
	Attribute("name", String, "Document name", func() { Example("README.md") })
	Attribute("type", String, "Document type", func() { Enum("text", "pdf", "image", "markdown"); Example("markdown") })
	Attribute("size", Int64, "Size in bytes", func() { Minimum(0); Example(2048) })
	Attribute("modified", String, "Last modified", func() {
		Format(FormatDateTime)
		Example("2025-01-01T12:00:00Z")
	})
	Required("id", "name", "type", "size", "modified")
})

// SystemInfo summarizes server status.
var SystemInfo = Type("SystemInfo", func() {
	Attribute("version", String, "System version", func() { Example("1.0.0") })
	Attribute("uptime", Int64, "Uptime in seconds", func() { Minimum(0); Example(12345) })
	Attribute("memory_usage", Float64, "Memory usage percentage", func() { Minimum(0); Maximum(100); Example(42.5) })
	Attribute("cpu_usage", Float64, "CPU usage percentage", func() { Minimum(0); Maximum(100); Example(17.3) })
	Attribute("active_connections", Int, "Number of active connections", func() { Minimum(0); Example(3) })
	Required("version", "uptime", "memory_usage", "cpu_usage", "active_connections")
})

// PromptTemplate defines a reusable prompt template.
var PromptTemplate = Type("PromptTemplate", func() {
	Attribute("name", String, "Template name", func() { Example("code_review") })
	Attribute("description", String, "Template description", func() { Example("Template for code review") })
	Attribute("variables", ArrayOf(String), "Required variables", func() { Example([]string{"code"}) })
	Attribute("template", String, "Template content", func() { MinLength(1); Example("You are an expert code reviewer. {{.code}}") })
	Required("name", "description", "template")
})

// SubscriptionInfo contains details about a resource subscription.
var SubscriptionInfo = Type("SubscriptionInfo", func() {
	Attribute("subscription_id", String, "Subscription ID", func() { Format(FormatUUID); Example("550e8400-e29b-41d4-a716-446655440000") })
	Attribute("resource", String, "Subscribed resource", func() { Example("documents") })
	Attribute("created_at", String, "Subscription created", func() { Format(FormatDateTime); Example("2025-01-01T12:00:00Z") })
	Required("subscription_id", "resource", "created_at")
})

// BatchResult summarizes batch processing progress or results.
var BatchResult = Type("BatchResult", func() {
	Attribute("processed", Int, "Number of items processed", func() { Minimum(0); Example(2) })
	Attribute("failed", Int, "Number of items failed", func() { Minimum(0); Example(0) })
	Attribute("results", ArrayOf(Any), "Processing results")
	Required("processed", "failed", "results")
})

// SearchResults is a list of search results.
var SearchResults = Type("SearchResults", ArrayOf(SearchResult))

// Documents is a list of documents.
var Documents = Type("Documents", ArrayOf(Document))

// PromptTemplates is a list of prompt templates.
var PromptTemplates = Type("PromptTemplates", ArrayOf(PromptTemplate))

// ConversationHistory represents conversation data for the history resource.
var ConversationHistory = Type("ConversationHistory", func() {
	Attribute("messages", ArrayOf(Any), "Conversation messages")
})
