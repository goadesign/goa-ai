package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Caller invokes MCP tools on behalf of the runtime-generated adapters. It is
// implemented by transport-specific clients (stdio, HTTP streaming, etc.).
type Caller interface {
	CallTool(ctx context.Context, req CallRequest) (CallResponse, error)
}

// CallRequest describes the toolset/tool invocation issued by the runtime.
type CallRequest struct {
	// Suite identifies the MCP toolset (server name) associated with the tool.
	Suite string
	// Tool is the MCP-local tool identifier (without the suite prefix).
	Tool string
	// Payload is the JSON-encoded tool arguments produced by the runtime.
	Payload json.RawMessage
}

// CallResponse captures the MCP tool result returned by the caller.
type CallResponse struct {
	// Result is the JSON payload returned by the MCP server.
	Result json.RawMessage
	// Structured carries optional structured content blobs emitted by MCP tools.
	Structured json.RawMessage
}

// SessionCaller implements Caller by wrapping an MCP SDK ClientSession.
// It is used by transport-specific callers (stdio, HTTP, SSE) to unify tool invocation.
type SessionCaller struct {
	session *mcp.ClientSession
}

// NewSessionCaller returns a new SessionCaller wrapping the provided SDK session.
func NewSessionCaller(session *mcp.ClientSession) *SessionCaller {
	return &SessionCaller{session: session}
}

// Close terminates the session and releases resources.
func (c *SessionCaller) Close() error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// CallTool invokes tools/call over the transport using the SDK session.
func (c *SessionCaller) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	var args map[string]any
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &args); err != nil {
			return CallResponse{}, fmt.Errorf("failed to unmarshal tool arguments: %w", err)
		}
	}

	params := &mcp.CallToolParams{
		Name:      req.Tool,
		Arguments: args,
	}

	addTraceMeta(ctx, &params.Meta)

	res, err := c.session.CallTool(ctx, params)
	if err != nil {
		return CallResponse{}, err
	}

	return normalizeSDKToolResult(res)
}

func normalizeSDKToolResult(res *mcp.CallToolResult) (CallResponse, error) {
	if res == nil {
		return CallResponse{}, errors.New("empty MCP response")
	}
	if len(res.Content) == 0 {
		return CallResponse{}, errors.New("tool returned no content")
	}

	var sb strings.Builder
	var combinedPayload []byte
	var structured []any

	for _, item := range res.Content {
		structured = append(structured, item)
		if textContent, ok := item.(*mcp.TextContent); ok {
			sb.WriteString(textContent.Text)
		}
	}

	textResult := sb.String()
	textBytes := []byte(textResult)

	switch {
	case textResult != "" && json.Valid(textBytes):
		// If the combined text is valid JSON, use it as the main result.
		combinedPayload = append(json.RawMessage(nil), textBytes...)
	case textResult != "":
		// Otherwise marshal it as a JSON string.
		marshaled, err := json.Marshal(textResult)
		if err != nil {
			return CallResponse{}, fmt.Errorf("failed to marshal text content: %w", err)
		}
		combinedPayload = marshaled
	default:
		// If there is no text at all, we fallback to just empty JSON object to satisfy the contract.
		// Alternatively, we could marshal the entire structured array as the result.
		// For framework safety, we'll serialize the first item if no text exists.
		marshaled, err := json.Marshal(res.Content[0])
		if err != nil {
			return CallResponse{}, fmt.Errorf("failed to marshal content: %w", err)
		}
		combinedPayload = marshaled
	}

	structuredPayload, err := json.Marshal(structured)
	if err != nil {
		return CallResponse{}, fmt.Errorf("failed to marshal structured content: %w", err)
	}

	return CallResponse{
		Result:     combinedPayload,
		Structured: structuredPayload,
	}, nil
}
