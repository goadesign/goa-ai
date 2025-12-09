package registry

import (
	"encoding/json"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestToolCallMessageStructure verifies Property 13: Tool call message structure.
// **Feature: internal-tool-registry, Property 13: Tool call message structure**
// *For any* tool call, the message delivered to providers should contain the correct
// tool name, payload, and a valid tool use ID.
// **Validates: Requirements 3.3**
func TestToolCallMessageStructure(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("NewToolCallMessage creates message with correct structure", prop.ForAll(
		func(toolUseID, tool string, payloadData map[string]any) bool {
			// Marshal the payload to JSON
			payload, err := json.Marshal(payloadData)
			if err != nil {
				return false
			}

			// Create the message
			msg := NewToolCallMessage(toolUseID, tool, payload)

			// Verify message type is "call"
			if msg.Type != MessageTypeCall {
				return false
			}

			// Verify tool_use_id is set correctly
			if msg.ToolUseID != toolUseID {
				return false
			}

			// Verify tool name is set correctly
			if msg.Tool != tool {
				return false
			}

			// Verify payload is set correctly
			if string(msg.Payload) != string(payload) {
				return false
			}

			// Verify ping_id is empty for call messages
			if msg.PingID != "" {
				return false
			}

			return true
		},
		genToolUseID(),
		genToolName(),
		genPayloadData(),
	))

	properties.Property("NewPingMessage creates message with correct structure", prop.ForAll(
		func(pingID string) bool {
			// Create the ping message
			msg := NewPingMessage(pingID)

			// Verify message type is "ping"
			if msg.Type != MessageTypePing {
				return false
			}

			// Verify ping_id is set correctly
			if msg.PingID != pingID {
				return false
			}

			// Verify tool_use_id is empty for ping messages
			if msg.ToolUseID != "" {
				return false
			}

			// Verify tool is empty for ping messages
			if msg.Tool != "" {
				return false
			}

			// Verify payload is nil for ping messages
			if msg.Payload != nil {
				return false
			}

			return true
		},
		genPingID(),
	))

	properties.Property("ToolCallMessage serializes to valid JSON with required fields", prop.ForAll(
		func(toolUseID, tool string, payloadData map[string]any) bool {
			// Marshal the payload to JSON
			payload, err := json.Marshal(payloadData)
			if err != nil {
				return false
			}

			// Create and serialize the message
			msg := NewToolCallMessage(toolUseID, tool, payload)
			serialized, err := json.Marshal(msg)
			if err != nil {
				return false
			}

			// Deserialize and verify structure
			var decoded map[string]any
			if err := json.Unmarshal(serialized, &decoded); err != nil {
				return false
			}

			// Verify required fields are present
			if decoded["type"] != MessageTypeCall {
				return false
			}
			if decoded["tool_use_id"] != toolUseID {
				return false
			}
			if decoded["tool"] != tool {
				return false
			}

			// Verify payload is present and can be decoded
			if _, ok := decoded["payload"]; !ok {
				return false
			}

			return true
		},
		genToolUseID(),
		genToolName(),
		genPayloadData(),
	))

	properties.TestingRun(t)
}

// --- Generators ---

// genToolUseID generates valid tool use IDs.
func genToolUseID() gopter.Gen {
	return gen.OneConstOf(
		"call-abc123",
		"call-def456",
		"call-ghi789",
		"toolu_01ABC",
		"toolu_02DEF",
	)
}

// genPingID generates valid ping IDs.
func genPingID() gopter.Gen {
	return gen.OneConstOf(
		"ping-abc123",
		"ping-def456",
		"ping-ghi789",
		"ping-xyz000",
	)
}

// genPayloadData generates payload data for tool calls.
func genPayloadData() gopter.Gen {
	return gen.OneConstOf(
		map[string]any{"query": "test-value"},
		map[string]any{"input": 42},
		map[string]any{"data": true},
		map[string]any{"config": "setting", "options": []string{"a", "b"}},
		map[string]any{"nested": map[string]any{"key": "value"}},
	)
}

// genToolName generates valid tool names.
func genToolName() gopter.Gen {
	return gen.OneConstOf(
		"query",
		"analyze",
		"search",
		"get_data",
		"process_request",
	)
}
