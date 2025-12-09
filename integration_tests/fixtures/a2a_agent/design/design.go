package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("a2a_test", func() {
	Title("A2A Test Agent API")
	Description("Test agent exposing tools via A2A protocol")
	Version("1.0")
	Server("a2a_server", func() {
		Host("dev", func() {
			URI("http://localhost:8080")
		})
		Services("test_agent")
	})
})

// TestTools is a top-level toolset that will be exported via A2A
var TestTools = Toolset("test_tools", func() {
	ToolsetDescription("Test tools for A2A integration")

	Tool("echo", "Echo back the input message", func() {
		Args(func() {
			Attribute("message", String, "Message to echo")
			Required("message")
		})
		Return(func() {
			Attribute("response", String, "Echoed message")
		})
	})

	Tool("add_numbers", "Add two numbers together", func() {
		Args(func() {
			Attribute("a", Int, "First number")
			Attribute("b", Int, "Second number")
			Required("a", "b")
		})
		Return(func() {
			Attribute("sum", Int, "Sum of the two numbers")
		})
	})

	Tool("process_data", "Process structured data", func() {
		Args(func() {
			Attribute("items", ArrayOf(String), "Items to process")
			Attribute("format", String, "Output format", func() {
				Enum("json", "text", "csv")
				Default("json")
			})
			Required("items")
		})
		Return(func() {
			Attribute("processed", ArrayOf(String), "Processed items")
			Attribute("count", Int, "Number of items processed")
		})
	})

	Tool("validate_input", "Validate input against schema", func() {
		Args(func() {
			Attribute("value", String, "Value to validate")
			Attribute("pattern", String, "Regex pattern to match")
			Required("value", "pattern")
		})
		Return(func() {
			Attribute("valid", Boolean, "Whether the input is valid")
			Attribute("message", String, "Validation message")
		})
	})
})

var _ = Service("test_agent", func() {
	Description("Test agent with A2A protocol support")

	// Define the agent with A2A export
	Agent("test_agent", "A test agent for A2A integration tests", func() {
		// Export the toolset via A2A
		Export(TestTools)
	})

	// JSON-RPC transport for A2A
	JSONRPC(func() {
		POST("/a2a")
	})

	// Placeholder method to satisfy Goa's requirement for at least one method
	Method("health", func() {
		Description("Health check endpoint")
		Result(func() {
			Attribute("status", String, "Health status")
		})
		JSONRPC(func() {})
	})
})
