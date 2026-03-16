package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCaller_CallTool_MultiContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Create in-memory transports
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// 2. Setup the MCP Server
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)

	// Register a tool that returns multiple text contents
	srv.AddTool(&mcp.Tool{
		Name:        "multi_content_tool",
		Description: "Returns multiple text items",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "hello "},
				&mcp.TextContent{Text: "world"},
				&mcp.TextContent{Text: "!"},
			},
		}, nil
	})

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	// 3. Setup the MCP Client
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := cli.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	// 4. Create SessionCaller
	caller := NewSessionCaller(clientSession)

	// 5. Test CallTool
	resp, err := caller.CallTool(ctx, CallRequest{
		Tool:    "multi_content_tool",
		Payload: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	// Verify concatenated string payload
	var resultStr string
	err = json.Unmarshal(resp.Result, &resultStr)
	require.NoError(t, err, "Result should be capable of unmarshaling to a string")
	assert.Equal(t, "hello world!", resultStr, "Text parts should be concatenated")

	// Verify structured raw payload
	var structured []map[string]any
	err = json.Unmarshal(resp.Structured, &structured)
	require.NoError(t, err, "Structured should be valid JSON array of features")
	require.Len(t, structured, 3)

	assert.Equal(t, "text", structured[0]["type"])
	assert.Equal(t, "hello ", structured[0]["text"])
	assert.Equal(t, "text", structured[1]["type"])
	assert.Equal(t, "world", structured[1]["text"])
	assert.Equal(t, "text", structured[2]["type"])
	assert.Equal(t, "!", structured[2]["text"])
}
