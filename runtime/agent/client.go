// Package agent provides types for agent runtime integration.
package agent

import "context"

// Client is a simplified interface for running agent tasks from A2A adapters.
// It abstracts away session management and message typing, accepting raw
// messages as a slice of any and returning the output.
type Client interface {
	// Run executes the agent with the provided messages and returns the output.
	// The messages slice contains input data; the specific format depends on
	// the adapter implementation.
	Run(ctx context.Context, messages []any) (any, error)
}
