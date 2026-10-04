// Package mcp defines application-owned HTTP retry settings. Only a trusted tool
// that can be repeated without additional effects may retry a lost SSE response.
// Worker activity retries and host-input continuation remain separate operations.
package mcp

import "fmt"

type (
	// HTTPRetryPolicy controls retries of one MCP HTTP request round.
	HTTPRetryPolicy struct {
		// MaxAttempts counts all POST attempts, including the first. Zero means one
		// attempt. Negative values are invalid. Each host-input round has its own limit.
		MaxAttempts int
		// TrustToolAnnotations allows this endpoint's readOnlyHint or idempotentHint
		// to authorize retries. False leaves all tool calls at one attempt. The host
		// must establish trust; receiving a hint does not establish it.
		TrustToolAnnotations bool
	}

	// ToolBinding describes the static or discovered HTTP behavior of one tool.
	// Generated clients provide these facts directly; imported clients derive them
	// from the catalog returned under the call's current authorization context.
	ToolBinding struct {
		// Headers maps tool argument properties to protocol request headers.
		Headers []HeaderBinding
		// ReadOnly states that the tool does not change its environment.
		ReadOnly bool
		// Idempotent states that repeating arguments has no additional effects.
		Idempotent bool
	}

	// interruptedResponseError identifies a response that ended before a final
	// message, rather than a malformed message or a completed tool error.
	interruptedResponseError struct {
		cause error
	}
)

// Validate rejects a negative attempt count before the client sends requests.
func (p HTTPRetryPolicy) Validate() error {
	if p.MaxAttempts < 0 {
		return fmt.Errorf("mcp: HTTP MaxAttempts must be non-negative")
	}
	return nil
}

func (e *interruptedResponseError) Error() string {
	return e.cause.Error()
}

func (e *interruptedResponseError) Unwrap() error {
	return e.cause
}
