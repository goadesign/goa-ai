package design

import (
    . "goa.design/goa-ai/dsl"
    . "goa.design/goa/v3/dsl"
)

var _ = API("assistant", func() {
    Title("AI Assistant API")
    Description("Simple MCP example exposing tools, resources, prompts, and an agent consumer")
    Version("1.0")
    Server("orchestrator", func() {
        Host("dev", func() {
            URI("http://localhost:8080")
        })
        Services("assistant")
    })
})

var _ = Service("assistant", func() {
    Description("AI Assistant service with full MCP protocol support")

    MCPServer("assistant-mcp", "1.0.0", ProtocolVersion("2025-06-18"))

    // Keep the design minimal; integration tests exercise MCP protocol handlers.
    JSONRPC(func() {
        POST("/rpc")
    })

    Method("list_documents", func() {
        Description("List available documents")
        Result(Documents)
        Resource("documents", "doc://list", "application/json")
        JSONRPC(func() {})
    })

    // Additional resources used by scenarios
    Method("system_info", func() {
        Description("Return system info")
        Result(func() {
            // Simple object; adapter marshals to JSON
            Attribute("name", String, "System name")
            Attribute("version", String, "System version")
        })
        Resource("system_info", "system://info", "application/json")
        JSONRPC(func() {})
    })

    Method("conversation_history", func() {
        Description("Return conversation history with optional query params")
        Payload(func() {
            Attribute("limit", Int, "Max items")
            Attribute("flag", Boolean, "Sample boolean flag")
            Attribute("nums", ArrayOf(Any), "Numbers array")
        })
        Result(func() {
            Attribute("items", ArrayOf(String), "History items")
        })
        Resource("conversation_history", "conversation://history", "application/json")
        JSONRPC(func() {})
    })

    // Static prompt for tests
    StaticPrompt("code_review", "Simple code review prompt", "system", "Review the provided code and suggest improvements.")

    Method("generate_prompts", func() {
        Description("Generate context-aware prompts")
        Payload(func() {
            Attribute("context", String, "Current context")
            Attribute("task", String, "Task type")
            Required("context", "task")
        })
        Result(PromptTemplates)
        DynamicPrompt("contextual_prompts", "Generate prompts based on context")
        JSONRPC(func() {})
    })

    Method("send_notification", func() {
        Description("Send status notification to client")
        Payload(func() {
            Attribute("type", String, "Notification type")
            Attribute("message", String, "Notification message")
            Attribute("data", Any, "Additional data")
            Required("type", "message")
        })
        Notification("status_update", "Send status updates to client")
        JSONRPC(func() {})
    })

    // ---- Tools (for MCP tools/list and tools/call) ----

    Method("analyze_sentiment", func() {
        Description("Analyze sentiment of text")
        Payload(func() {
            Attribute("text", String, "Input text to analyze")
            Required("text")
        })
        Result(func() {
            Attribute("sentiment", String, "Detected sentiment")
        })
        MCPTool("analyze_sentiment", "Analyze sentiment of text")
        JSONRPC(func() {})
    })

    Method("extract_keywords", func() {
        Description("Extract keywords from text")
        Payload(func() {
            Attribute("text", String, "Input text")
            Required("text")
        })
        Result(func() { Attribute("keywords", ArrayOf(String), "Extracted keywords") })
        MCPTool("extract_keywords", "Extract keywords from text")
        JSONRPC(func() {})
    })

    Method("summarize_text", func() {
        Description("Summarize text")
        Payload(func() {
            Attribute("text", String, "Input text to summarize")
            Required("text")
        })
        Result(func() { Attribute("summary", String, "Summary") })
        MCPTool("summarize_text", "Summarize text")
        JSONRPC(func() {})
    })

    Method("search", func() {
        Description("Search knowledge base")
        Payload(func() {
            Attribute("query", String, "Search query")
            Attribute("limit", Int, "Maximum number of results")
            Required("query")
        })
        Result(func() { Attribute("results", ArrayOf(String), "Search results") })
        MCPTool("search", "Search knowledge base")
        JSONRPC(func() {})
    })

    Method("execute_code", func() {
        Description("Execute code")
        Payload(func() {
            Attribute("language", String, "Language to execute", func() { Enum("python", "javascript") })
            Attribute("code", String, "Code to execute")
            Required("language", "code")
        })
        Result(func() { Attribute("output", String, "Execution output") })
        MCPTool("execute_code", "Execute code")
        JSONRPC(func() {})
    })

    Method("process_batch", func() {
        Description("Process batch of items")
        Payload(func() {
            Attribute("items", ArrayOf(String), "Items to process")
            Attribute("format", String, "Output format", func() { Enum("json", "text", "blob", "uri") })
            Attribute("blob", String, "Base64 blob")
            Attribute("uri", String, "Resource URI")
            Attribute("mimeType", String, "MIME type")
            Required("items")
        })
        Result(func() { Attribute("ok", Boolean, "Operation status") })
        MCPTool("process_batch", "Process a batch of items")
        JSONRPC(func() {})
    })
})

// ---- Shared Types (subset sufficient for integration tests) ----

var Documents = Type("Documents", func() {
    Attribute("items", ArrayOf(String), "Document entries")
    Required("items")
})

var PromptTemplates = Type("PromptTemplates", func() {
    Attribute("templates", ArrayOf(String), "Templates")
    Required("templates")
})
