package mcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewSSECaller creates an SSE-based Caller and performs the MCP initialize handshake.
func NewSSECaller(ctx context.Context, opts HTTPOptions) (*SessionCaller, error) {
	return newHTTPTransportCaller(ctx, opts, func(httpClient *http.Client) mcp.Transport {
		return &mcp.SSEClientTransport{
			Endpoint:   opts.Endpoint,
			HTTPClient: httpClient,
		}
	}, "failed to connect to MCP SSE server")
}
