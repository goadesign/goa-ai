package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewSSECaller creates an SSE-based Caller and performs the MCP initialize handshake.
func NewSSECaller(ctx context.Context, opts HTTPOptions) (*SessionCaller, error) {
	clientName := opts.ClientName
	if clientName == "" {
		clientName = "goa-ai"
	}
	clientVersion := opts.ClientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}

	httpClient := opts.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    clientName,
		Version: clientVersion,
	}, nil)

	transport := &mcp.SSEClientTransport{
		Endpoint:   opts.Endpoint,
		HTTPClient: httpClient,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP SSE server: %w", err)
	}

	return NewSessionCaller(session), nil
}
