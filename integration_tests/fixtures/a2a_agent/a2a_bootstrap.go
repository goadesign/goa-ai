package a2aagent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AgentRuntime implements the agent runtime interface for A2A integration tests.
type AgentRuntime struct{}

// Run executes the agent with the given messages and returns a response.
func (r *AgentRuntime) Run(ctx context.Context, messages []any) (any, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}

	// Check if this is a skill invocation
	for _, msg := range messages {
		if m, ok := msg.(map[string]any); ok {
			if content, ok := m["content"].(string); ok {
				// Try to parse as JSON for skill invocation
				var data map[string]any
				if err := parseJSON(content, &data); err == nil {
					if skill, ok := data["skill"].(string); ok {
						return r.executeSkill(ctx, skill, data["arguments"])
					}
				}
			}
		}
	}

	// Default response for non-skill messages
	return map[string]any{
		"status":  "completed",
		"message": "Test agent processed request",
	}, nil
}

// executeSkill routes skill invocations to the appropriate handler.
func (r *AgentRuntime) executeSkill(ctx context.Context, skill string, args any) (any, error) {
	arguments, _ := args.(map[string]any)
	if arguments == nil {
		arguments = map[string]any{}
	}

	switch skill {
	case "test_tools.echo":
		message, _ := arguments["message"].(string)
		return map[string]any{
			"response": fmt.Sprintf("Echo: %s", message),
		}, nil

	case "test_tools.add_numbers":
		a, _ := toInt(arguments["a"])
		b, _ := toInt(arguments["b"])
		return map[string]any{
			"sum": a + b,
		}, nil

	case "test_tools.process_data":
		items, _ := toStringSlice(arguments["items"])
		format, _ := arguments["format"].(string)
		if format == "" {
			format = "json"
		}

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
		return map[string]any{
			"processed": processed,
			"count":     len(items),
		}, nil

	case "test_tools.validate_input":
		value, _ := arguments["value"].(string)
		pattern, _ := arguments["pattern"].(string)

		re, err := regexp.Compile(pattern)
		if err != nil {
			return map[string]any{
				"valid":   false,
				"message": fmt.Sprintf("Invalid pattern: %v", err),
			}, nil
		}

		if re.MatchString(value) {
			return map[string]any{
				"valid":   true,
				"message": "Input matches pattern",
			}, nil
		}
		return map[string]any{
			"valid":   false,
			"message": "Input does not match pattern",
		}, nil

	default:
		return nil, fmt.Errorf("unknown skill: %s", skill)
	}
}

// Helper functions

func parseJSON(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func toStringSlice(v any) ([]string, bool) {
	if arr, ok := v.([]any); ok {
		result := make([]string, len(arr))
		for i, item := range arr {
			if s, ok := item.(string); ok {
				result[i] = s
			}
		}
		return result, true
	}
	if arr, ok := v.([]string); ok {
		return arr, true
	}
	return nil, false
}
