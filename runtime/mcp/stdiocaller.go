package mcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StdioOptions configures the stdio-based MCP caller.
type StdioOptions struct {
	Command       string
	Args          []string
	Env           []string
	Dir           string
	ClientName    string
	ClientVersion string
	InitTimeout   time.Duration
}

// NewStdioCaller launches the target command using CommandTransport,
// performs the MCP initialize handshake via Client.Connect,
// and returns a Caller that wraps the resulting ClientSession.
func NewStdioCaller(ctx context.Context, opts StdioOptions) (*SessionCaller, error) {
	if opts.Command == "" {
		return nil, errors.New("command is required")
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    mcpClientName(opts.ClientName),
		Version: mcpClientVersion(opts.ClientVersion),
	}, nil)

	return connectSession(ctx, opts.InitTimeout, func(sessionCtx context.Context) (*mcp.ClientSession, error) {
		// #nosec G204 -- stdio callers are explicitly configured to launch the target MCP server command.
		cmd := exec.CommandContext(sessionCtx, opts.Command, opts.Args...)
		if opts.Dir != "" {
			cmd.Dir = opts.Dir
		}
		if len(opts.Env) > 0 {
			cmd.Env = opts.Env
		}

		transport := &mcp.CommandTransport{
			Command: cmd,
		}

		session, err := client.Connect(sessionCtx, transport, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to MCP server: %w", err)
		}
		return session, nil
	})
}
