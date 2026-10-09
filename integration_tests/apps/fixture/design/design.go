// Package design declares the record panel and actions used by the browser
// host. Goa generates every MCP request, response, and validation boundary.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("apps-peer", func() {
	Description("Synthetic record panel used to verify official MCP Apps clients")
})

var privateRecord = Type("PrivateRecord", func() {
	Field(1, "recordId", String, "Record selected by the panel")
	Required("recordId")
})

var _ = Service("records", func() {
	Description("Owns a record panel, its refresh action, and a model-only query")
	MCP("records", "1")
	JSONRPC(func() { POST("/mcp") })
	Method("panel", func() {
		Description("Read the HTML panel displayed after a record is shown")
		Result(String)
		Resource("panel", "ui://records/panel", "text/html;profile=mcp-app")
	})
	Method("show", func() {
		Description("Show a synthetic record and its interactive panel")
		Result(func() {
			Field(1, "summary", String, "Record summary available to the model")
			Field(2, "privateRecord", privateRecord, "Record identity available only to the host and panel")
			Required("summary", "privateRecord")
		})
		Tool("show", "Show a record", func() {
			ToolUI("ui://records/panel")
			ToolMetadata("privateRecord")
		})
	})
	Method("refresh", func() {
		Description("Refresh the record shown in the interactive panel")
		Result(func() {
			Field(1, "summary", String, "Updated record summary")
			Required("summary")
		})
		Tool("refresh", "Refresh the panel", func() { ToolVisibility("app") })
	})
	Method("query", func() {
		Description("Query a record through the model rather than the panel")
		Result(String)
		Tool("query", "Query records", func() { ToolVisibility("model") })
	})
	Method("wait", func() {
		Description("Wait until the host cancels this synthetic request")
		Result(String)
		Tool("wait", "Wait for host cancellation", func() { ToolVisibility("app") })
	})
})
