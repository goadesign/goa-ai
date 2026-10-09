// Package mcp defines application-owned HTTP retry settings. Only a trusted tool
// that can be repeated without additional effects may retry a lost SSE response.
// Worker activity retries and host-input continuation remain separate operations.
package mcp

import "fmt"

type (
	// HTTPRetryPolicy controls retries of one MCP HTTP request round.
	HTTPRetryPolicy struct {
		// MaxAttempts counts attempts governed by stream-loss retry, including the
		// first. Zero means one; negative values are invalid. A rejected credential
		// can add one separately bounded resend after a fresh grant. Each host-input
		// round receives its own allowances.
		MaxAttempts int
		// TrustToolAnnotations allows this endpoint's readOnlyHint or idempotentHint
		// to authorize retries. False leaves all tool calls at one attempt. The host
		// must establish trust; receiving a hint does not establish it.
		TrustToolAnnotations bool
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
