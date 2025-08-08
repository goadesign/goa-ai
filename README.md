# goa-mcp-plugin

Goa plugin that adds MCP (Model Context Protocol) as a transport and DSL annotations to expose Goa service methods as MCP tools, resources and prompts.

Status: experimental scaffold. Generates an MCP server wrapper over Goa endpoints using JSON-RPC 2.0 over stdio.

## Install

```
go install goa.design/goa/v3/cmd/goa@latest
```

## Usage (preview)

- In your Goa design, import `github.com/workspace/goa-mcp-plugin/dsl/mcp` and annotate methods:

```go
import mcpdsl "github.com/workspace/goa-mcp-plugin/dsl/mcp"

var _ = Service("calc", func() {
    Method("add", func() {
        Payload(func() {
            Attribute("a", Int)
            Attribute("b", Int)
            Required("a", "b")
        })
        Result(Int)
        // Declare as an MCP tool with description
        mcpdsl.Tool(func() {
            mcpdsl.Description("Add two integers")
        })
    })
})
```

- Run `goa gen` as usual. The plugin generates `gen/mcp/server` with a stdio MCP server exposing your annotated methods as tools.

- Start the MCP server:

```
go run ./gen/mcp/server
```

Point an MCP client (e.g., Claude Desktop) to the binary with stdio transport.