// Package dsl extends the Goa DSL with Model Context Protocol (MCP) constructs.
//
// The package provides helpers to enable MCP on a service and to declare
// MCP features such as tools, resources, prompts, notifications, and
// subscriptions directly in Goa designs.
//
// Import this package in your design and use the functions in service DSLs.
//
// Example usage:
//
//	package design
//
//	import (
//		mcp "goa.design/goa-ai/dsl" // import MCP DSL helpers
//		. "goa.design/goa/v3/dsl"
//	)
//
//	var _ = API("assistant", func() {
//		Title("AI Assistant API")
//		Version("1.0")
//	})
//
//	var _ = Service("assistant", func() {
//		Description("AI Assistant service with MCP support")
//
//		// Enable MCP for this service
//		mcp.MCPServer("assistant-mcp", "1.0.0")
//
//		// Expose a method as an MCP tool
//		Method("analyze_text", func() {
//			Description("Analyze text for sentiment or keywords")
//			// Expose as MCP tool
//			mcp.Tool("analyze_text", "Use this tool to analyze text with different modes")
//			Payload(func() {
//				Attribute("text", String, "Text to analyze")
//				Attribute("mode", String, "Analysis mode", func() { Enum("sentiment", "keywords") })
//				Required("text", "mode")
//			})
//			Result(func() {
//				Attribute("result", String, "Analysis result")
//				Required("result")
//			})
//			JSONRPC(func() {}) // MCP requires a JSON-RPC endpoint
//		})
//
//		// Add a static prompt template
//		mcp.StaticPrompt(
//			"code_review", "Template for code review",
//			"system", "You are an expert code reviewer.",
//			"user", "Please review this code: {{.code}}",
//		)
//	})
package dsl
