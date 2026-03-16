package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPOptions configures the HTTP Caller.
type HTTPOptions struct {
	Endpoint        string
	Client          *http.Client
	ProtocolVersion string
	ClientName      string
	ClientVersion   string
	InitTimeout     time.Duration
}

// NewHTTPCaller creates an HTTP-based Caller and performs MCP initialize handshake.
func NewHTTPCaller(ctx context.Context, opts HTTPOptions) (*SessionCaller, error) {
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

	// StreamableClientTransport handles the standard MCP HTTP transport
	transport := &mcp.StreamableClientTransport{
		Endpoint:   opts.Endpoint,
		HTTPClient: httpClient,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP HTTP server: %w", err)
	}

	return NewSessionCaller(session), nil
}
