package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPClientName    = "goa-ai"
	defaultMCPClientVersion = "dev"
	defaultMCPHTTPTimeout   = 30 * time.Second
)

// HTTPOptions configures the HTTP Caller.
type HTTPOptions struct {
	Endpoint      string
	Client        *http.Client
	ClientName    string
	ClientVersion string
	InitTimeout   time.Duration
}

// NewHTTPCaller creates an HTTP-based Caller and performs MCP initialize handshake.
func NewHTTPCaller(ctx context.Context, opts HTTPOptions) (*SessionCaller, error) {
	return newHTTPTransportCaller(ctx, opts, func(httpClient *http.Client) mcp.Transport {
		// StreamableClientTransport handles the standard MCP HTTP transport.
		return &mcp.StreamableClientTransport{
			Endpoint:   opts.Endpoint,
			HTTPClient: httpClient,
		}
	}, "failed to connect to MCP HTTP server")
}

func newHTTPTransportCaller(
	ctx context.Context,
	opts HTTPOptions,
	newTransport func(*http.Client) mcp.Transport,
	connectErr string,
) (*SessionCaller, error) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    mcpClientName(opts.ClientName),
		Version: mcpClientVersion(opts.ClientVersion),
	}, nil)

	transport := newTransport(mcpHTTPClient(opts.Client))

	return connectSession(ctx, opts.InitTimeout, func(sessionCtx context.Context) (*mcp.ClientSession, error) {
		session, err := client.Connect(sessionCtx, transport, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", connectErr, err)
		}
		return session, nil
	})
}

func mcpClientName(name string) string {
	if name == "" {
		return defaultMCPClientName
	}
	return name
}

func mcpClientVersion(version string) string {
	if version == "" {
		return defaultMCPClientVersion
	}
	return version
}

func mcpHTTPClient(httpClient *http.Client) *http.Client {
	if httpClient != nil {
		return httpClient
	}
	return &http.Client{
		Timeout: defaultMCPHTTPTimeout,
	}
}
