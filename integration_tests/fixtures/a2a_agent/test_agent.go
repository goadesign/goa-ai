package a2aagent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// TestAgentRuntime implements the agent runtime interface for testing.
type TestAgentRuntime struct{}

// Run executes the agent with the given messages.
func (r *TestAgentRuntime) Run(ctx context.Context, messages []any) (any, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}
	// Return a simple response for testing
	return map[string]any{
		"status":  "completed",
		"message": "Test agent processed request",
	}, nil
}

// EchoHandler handles the echo tool.
func EchoHandler(ctx context.Context, message string) (string, error) {
	return fmt.Sprintf("Echo: %s", message), nil
}

// AddNumbersHandler handles the add_numbers tool.
func AddNumbersHandler(ctx context.Context, a, b int) (int, error) {
	return a + b, nil
}

// ProcessDataHandler handles the process_data tool.
func ProcessDataHandler(ctx context.Context, items []string, format string) ([]string, int, error) {
	processed := make([]string, len(items))
	for i, item := range items {
		switch format {
		case "text":
			processed[i] = strings.ToUpper(item)
		case "csv":
			processed[i] = fmt.Sprintf("\"%s\"", item)
		default: // json
			processed[i] = fmt.Sprintf("{\"item\":\"%s\"}", item)
		}
	}
	return processed, len(items), nil
}

// ValidateInputHandler handles the validate_input tool.
func ValidateInputHandler(ctx context.Context, value, pattern string) (bool, string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Sprintf("Invalid pattern: %v", err), nil
	}
	if re.MatchString(value) {
		return true, "Input matches pattern", nil
	}
	return false, "Input does not match pattern", nil
}
