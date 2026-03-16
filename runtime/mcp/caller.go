package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// Structured carries the full structured MCP content payload, including text
	// items that may also contribute to Result.
	Structured json.RawMessage
}

// NormalizeToolCallResponse converts raw text parts and structured content into
// the canonical CallResponse representation used by MCP callers.
//
// Text parts are concatenated in order. If the combined text is valid JSON, it
// becomes Result directly; otherwise it is marshaled as a JSON string. If no
// text is present, fallbackResult is marshaled into Result. Structured is
// marshaled into Structured when non-nil.
func NormalizeToolCallResponse(textParts []string, structured any, fallbackResult any) (CallResponse, error) {
	if len(textParts) == 0 && fallbackResult == nil {
		return CallResponse{}, errors.New("tool returned no content")
	}

	var result json.RawMessage
	textResult := strings.Join(textParts, "")
	textBytes := []byte(textResult)

	switch {
	case textResult != "" && json.Valid(textBytes):
		result = append(json.RawMessage(nil), textBytes...)
	case textResult != "":
		marshaled, err := json.Marshal(textResult)
		if err != nil {
			return CallResponse{}, fmt.Errorf("failed to marshal text content: %w", err)
		}
		result = marshaled
	default:
		marshaled, err := json.Marshal(fallbackResult)
		if err != nil {
			return CallResponse{}, fmt.Errorf("failed to marshal fallback content: %w", err)
		}
		result = marshaled
	}

	var structuredPayload json.RawMessage
	if structured != nil {
		marshaled, err := json.Marshal(structured)
		if err != nil {
			return CallResponse{}, fmt.Errorf("failed to marshal structured content: %w", err)
		}
		structuredPayload = append(json.RawMessage(nil), marshaled...)
	}

	return CallResponse{
		Result:     result,
		Structured: structuredPayload,
	}, nil
}

// SessionCaller implements Caller by wrapping an MCP SDK ClientSession.
// It is used by transport-specific callers (stdio, HTTP, SSE) to unify tool invocation.
type SessionCaller struct {
	session *mcp.ClientSession
	cancel  context.CancelFunc
}

// NewSessionCaller returns a new SessionCaller wrapping the provided SDK session.
func NewSessionCaller(session *mcp.ClientSession, cancel context.CancelFunc) *SessionCaller {
	return &SessionCaller{
		session: session,
		cancel:  cancel,
	}
}

// Close terminates the session and releases resources.
func (c *SessionCaller) Close() error {
	var err error
	if c.cancel != nil {
		defer c.cancel()
	}
	if c.session != nil {
		err = c.session.Close()
	}
	return err
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

	textParts := make([]string, 0, len(res.Content))
	var structured []any

	for _, item := range res.Content {
		structured = append(structured, item)
		if textContent, ok := item.(*mcp.TextContent); ok {
			textParts = append(textParts, textContent.Text)
		}
	}

	return NormalizeToolCallResponse(textParts, structured, res.Content[0])
}

// connectSession establishes an SDK session without tying the live session
// lifecycle to a short-lived initialization timeout context.
func connectSession(
	ctx context.Context,
	initTimeout time.Duration,
	connect func(context.Context) (*mcp.ClientSession, error),
) (*SessionCaller, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	if initTimeout <= 0 {
		session, err := connect(sessionCtx)
		if err != nil {
			cancel()
			return nil, err
		}
		return NewSessionCaller(session, cancel), nil
	}

	type connectResult struct {
		session *mcp.ClientSession
		err     error
	}

	resultCh := make(chan connectResult, 1)
	go func() {
		session, err := connect(sessionCtx)
		resultCh <- connectResult{session: session, err: err}
	}()

	timer := time.NewTimer(initTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, fmt.Errorf("mcp initialize timed out after %s", initTimeout)
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		return NewSessionCaller(result.session, cancel), nil
	}
}
