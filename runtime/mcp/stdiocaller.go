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
	Command         string
	Args            []string
	Env             []string
	Dir             string
	ProtocolVersion string
	ClientName      string
	ClientVersion   string
	InitTimeout     time.Duration
}

// NewStdioCaller launches the target command using CommandTransport,
// performs the MCP initialize handshake via Client.Connect,
// and returns a Caller that wraps the resulting ClientSession.
func NewStdioCaller(ctx context.Context, opts StdioOptions) (*SessionCaller, error) {
	if opts.Command == "" {
		return nil, errors.New("command is required")
	}

	clientName := opts.ClientName
	if clientName == "" {
		clientName = "goa-ai"
	}
	clientVersion := opts.ClientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    clientName,
		Version: clientVersion,
	}, nil)

	cmd := exec.Command(opts.Command, opts.Args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}

	transport := &mcp.CommandTransport{
		Command: cmd,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	return NewSessionCaller(session), nil
}
