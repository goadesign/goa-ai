// These tests exercise content declarations through the public Goa DSL.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestToolContentDSL(t *testing.T) {
	design := func() {
		API("test", func() {})
		item := Type("ToolAttachment", func() {
			OneOf("content", "Selected attachment", func() {
				Attribute("text", func() {
					Attribute("text", String, "Text contents")
					Required("text")
				})
			})
			Required("content")
		})
		Service("documents", func() {
			MCP("documents", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() {
				Result(func() {
					Attribute("summary", String, "Domain result")
					Attribute("attachments", ArrayOfRequired(item), "Ordered attachments")
					Required("summary")
				})
				Tool("read", "Read documents", func() {
					ToolContent("attachments")
				})
			})
		})
	}
	runMCPDSL(t, design)
	assert.Equal(t, "attachments", mcpexpr.Root.MCPServers["documents"].Tools[0].ContentField)
}

func TestToolContentDSLRejectsInvalidContextAndDuplicate(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func()
	}{
		{name: "outside tool", configure: func() {
			ToolContent("attachments")
		}},
		{name: "empty", configure: func() {
			Tool("read", "Read", func() {
				ToolContent("")
			})
		}},
		{name: "duplicate", configure: func() {
			Tool("read", "Read", func() {
				ToolContent("attachments")
				ToolContent("attachments")
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				Service("documents", func() {
					MCP("documents", "1")
					JSONRPC(func() { POST("/mcp") })
					Method("read", test.configure)
				})
			})
			assert.Error(t, err)
		})
	}
}
