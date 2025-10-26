package design

import (
	agentsdsl "goa.design/goa-ai/agents/dsl"
	mcpdsl "goa.design/goa-ai/features/mcp/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("assistant", func() {
	Title("AI Assistant API")
	Description("Simple MCP example exposing tools, resources, prompts, and an agent consumer")
	Version("1.0")
})

var _ = Service("assistant", func() {
	Description("AI Assistant service with full MCP protocol support")

	mcpdsl.MCPServer("assistant-mcp", "1.0.0", mcpdsl.ProtocolVersion("2025-06-18"))

	mcpdsl.StaticPrompt(
		"code_review",
		"Template for code review",
		"system", "You are an expert code reviewer.",
		"user", "Please review this code: {{.code}}",
		"assistant", "I'll analyze the code for quality, bugs, and improvements.",
	)

	JSONRPC(func() {
		POST("/rpc")
	})

	Method("analyze_sentiment", func() {
		Description("Analyze text sentiment")
		Payload(func() {
			Attribute("text", String, "Text to analyze", func() {
				MinLength(1)
				MaxLength(10000)
				Example("I love this new feature! It works perfectly.")
			})
			Required("text")
		})
		Result(SentimentResult)
		mcpdsl.Tool("analyze_sentiment", "Analyze text sentiment")
		JSONRPC(func() {})
	})

	Method("extract_keywords", func() {
		Description("Extract keywords from text")
		Payload(func() {
			Attribute("text", String, "Text to analyze", func() {
				MinLength(1)
				MaxLength(10000)
				Example("Machine learning algorithms process data to identify patterns.")
			})
			Required("text")
		})
		Result(KeywordsResult)
		mcpdsl.Tool("extract_keywords", "Extract keywords from text")
		JSONRPC(func() {})
	})

	Method("summarize_text", func() {
		Description("Summarize text")
		Payload(func() {
			Attribute("text", String, "Text to summarize", func() {
				MinLength(1)
				MaxLength(10000)
				Example("This is a long text that needs to be summarized.")
			})
			Required("text")
		})
		Result(SummaryResult)
		mcpdsl.Tool("summarize_text", "Summarize text")
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
		mcpdsl.Tool("search", "Search the knowledge base")
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
		mcpdsl.Tool("execute_code", "Execute code safely in sandbox")
		JSONRPC(func() {})
	})

	Method("list_documents", func() {
		Description("List available documents")
		Result(Documents)
		mcpdsl.Resource("documents", "doc://list", "application/json")
		JSONRPC(func() {})
	})

	Method("get_system_info", func() {
		Description("Get system information and status")
		Result(SystemInfo)
		mcpdsl.Resource("system_info", "system://info", "application/json")
		JSONRPC(func() {})
	})

	Method("get_conversation_history", func() {
		Description("Get conversation history with optional filtering")
		Payload(func() {
			Attribute("limit", Int, "Maximum items")
			Attribute("flag", Boolean, "Flag example")
			Attribute("nums", ArrayOf(Any), "Numbers")
		})
		Result(ConversationHistory)
		mcpdsl.Resource("conversation_history", "conversation://history", "application/json")
		JSONRPC(func() {})
	})

	Method("generate_prompts", func() {
		Description("Generate context-aware prompts")
		Payload(func() {
			Attribute("context", String, "Current context", func() { Example("testing") })
			Attribute("task", String, "Task type", func() { Example("unit-test") })
			Required("context", "task")
		})
		Result(PromptTemplates)
		mcpdsl.DynamicPrompt("contextual_prompts", "Generate prompts based on context")
		JSONRPC(func() {})
	})

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
		mcpdsl.Notification("status_update", "Send status updates to client")
		JSONRPC(func() {})
	})

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
		mcpdsl.Subscription("documents")
		JSONRPC(func() {})
	})

	Method("process_batch", func() {
		Description("Process a batch of items with progress tracking")
		Payload(func() {
			Attribute("items", ArrayOf(String), "Items to process", func() {
				MinLength(1)
				Example([]string{"item1", "item2"})
			})
			Attribute("format", String, "Output format", func() {
				Enum("text", "blob", "uri")
				Example("text")
			})
			Attribute("blob", Bytes, "Blob data when format=blob", func() { Example([]byte("hello")) })
			Attribute("uri", String, "URI to include when format=uri", func() {
				Format(FormatURI)
				Example("system://info")
			})
			Attribute("mimeType", String, "Mime type for blob/uri", func() { Example("text/plain") })
			Required("items")
		})
		Result(BatchResult)
		StreamingResult(BatchResult)
		mcpdsl.Tool("process_batch", "Process items with progress updates")
		JSONRPC(func() {
			ServerSentEvents()
		})
	})
})

var _ = Service("orchestrator", func() {
	Description("Agent service consuming the assistant MCP toolset")

	agentsdsl.Agent("chat", "Chat orchestrator", func() {
		agentsdsl.Uses(func() {
			agentsdsl.UseMCPToolset("assistant", "assistant-mcp")
		})
	})
})

var SentimentResult = Type("SentimentResult", func() {
	Attribute("label", String, "Sentiment label", func() { Enum("positive", "neutral", "negative"); Example("positive") })
	Attribute("confidence", Float64, "Confidence score (0–1)", func() { Minimum(0); Maximum(1); Example(0.98) })
	Required("label")
})

var KeywordsResult = Type("KeywordsResult", func() {
	Attribute("keywords", ArrayOf(String), "Extracted keywords", func() { Example([]string{"algorithms", "patterns"}) })
	Required("keywords")
})

var SummaryResult = Type("SummaryResult", func() {
	Attribute("summary", String, "Summary text", func() { MinLength(1); Example("Concise summary.") })
	Required("summary")
})

var SearchResult = Type("SearchResult", func() {
	Attribute("id", String, "Result ID", func() { Example("doc-1") })
	Attribute("title", String, "Result title", func() { Example("MCP Specification Overview") })
	Attribute("content", String, "Result content", func() { MaxLength(10000); Example("The Model Context Protocol (MCP) defines...") })
	Attribute("score", Float64, "Relevance score (0–1)", func() { Minimum(0); Maximum(1); Example(0.87) })
	Required("id", "title", "content", "score")
})

var ExecutionResult = Type("ExecutionResult", func() {
	Attribute("output", String, "Execution output", func() { Example("4") })
	Attribute("error", String, "Error message if any", func() { Example("") })
	Attribute("execution_time", Float64, "Execution time in seconds", func() { Minimum(0); Example(0.01) })
	Required("output", "execution_time")
})

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

var SystemInfo = Type("SystemInfo", func() {
	Attribute("version", String, "System version", func() { Example("1.0.0") })
	Attribute("uptime", Int64, "Uptime in seconds", func() { Minimum(0); Example(12345) })
	Attribute("memory_usage", Float64, "Memory usage percentage", func() { Minimum(0); Maximum(100); Example(42.5) })
	Attribute("cpu_usage", Float64, "CPU usage percentage", func() { Minimum(0); Maximum(100); Example(17.3) })
	Attribute("active_connections", Int, "Number of active connections", func() { Minimum(0); Example(3) })
	Required("version", "uptime", "memory_usage", "cpu_usage", "active_connections")
})

var PromptTemplate = Type("PromptTemplate", func() {
	Attribute("name", String, "Template name", func() { Example("code_review") })
	Attribute("description", String, "Template description", func() { Example("Template for code review") })
	Attribute("variables", ArrayOf(String), "Required variables", func() { Example([]string{"code"}) })
	Attribute("template", String, "Template content", func() { MinLength(1); Example("You are an expert code reviewer. {{.code}}") })
	Required("name", "description", "template")
})

var SubscriptionInfo = Type("SubscriptionInfo", func() {
	Attribute("subscription_id", String, "Subscription ID", func() { Format(FormatUUID); Example("550e8400-e29b-41d4-a716-446655440000") })
	Attribute("resource", String, "Subscribed resource", func() { Example("documents") })
	Attribute("created_at", String, "Subscription created", func() { Format(FormatDateTime); Example("2025-01-01T12:00:00Z") })
	Required("subscription_id", "resource", "created_at")
})

var BatchResult = Type("BatchResult", func() {
	Attribute("processed", Int, "Number of items processed", func() { Minimum(0); Example(2) })
	Attribute("failed", Int, "Number of items failed", func() { Minimum(0); Example(0) })
	Attribute("results", ArrayOf(Any), "Processing results")
	Required("processed", "failed", "results")
})

var SearchResults = Type("SearchResults", ArrayOf(SearchResult))

var Documents = Type("Documents", ArrayOf(Document))

var PromptTemplates = Type("PromptTemplates", ArrayOf(PromptTemplate))

var ConversationHistory = Type("ConversationHistory", func() {
	Attribute("messages", ArrayOf(Any), "Conversation messages")
})
