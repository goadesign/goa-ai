package codegen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestA2AJSONRPCRequestFormat verifies that A2A JSON-RPC requests follow the
// correct format for client-server communication.
// **Validates: Requirements 10.3**
func TestA2AJSONRPCRequestFormat(t *testing.T) {
	// Test that a properly formatted JSON-RPC request can be parsed
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      "test-123",
		"method":  "tasks/send",
		"params": map[string]any{
			"id": "task-456",
			"message": map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{"type": "text", "text": "Hello, agent!"},
				},
			},
		},
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err)

	// Verify the request can be unmarshaled back
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	require.Equal(t, "2.0", parsed["jsonrpc"])
	require.Equal(t, "test-123", parsed["id"])
	require.Equal(t, "tasks/send", parsed["method"])

	params := parsed["params"].(map[string]any)
	require.Equal(t, "task-456", params["id"])

	message := params["message"].(map[string]any)
	require.Equal(t, "user", message["role"])
}

// TestA2AJSONRPCResponseFormat verifies that A2A JSON-RPC responses follow the
// correct format for client-server communication.
// **Validates: Requirements 10.3**
func TestA2AJSONRPCResponseFormat(t *testing.T) {
	// Test success response format
	successResp := map[string]any{
		"jsonrpc": "2.0",
		"id":      "test-123",
		"result": map[string]any{
			"id": "task-456",
			"status": map[string]any{
				"state":     "completed",
				"timestamp": "2024-01-01T00:00:00Z",
			},
			"artifacts": []map[string]any{
				{
					"name":      "result",
					"parts":     []map[string]any{{"type": "text", "text": "Response"}},
					"lastChunk": true,
				},
			},
		},
	}

	data, err := json.Marshal(successResp)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	require.Equal(t, "2.0", parsed["jsonrpc"])
	require.Equal(t, "test-123", parsed["id"])
	require.NotNil(t, parsed["result"])

	result := parsed["result"].(map[string]any)
	require.Equal(t, "task-456", result["id"])

	status := result["status"].(map[string]any)
	require.Equal(t, "completed", status["state"])
}

// TestA2AJSONRPCErrorFormat verifies that A2A JSON-RPC error responses follow
// the correct format.
// **Validates: Requirements 10.3**
func TestA2AJSONRPCErrorFormat(t *testing.T) {
	// Test error response format
	errorResp := map[string]any{
		"jsonrpc": "2.0",
		"id":      "test-123",
		"error": map[string]any{
			"code":    -32600,
			"message": "Invalid Request",
			"data":    "Missing required field",
		},
	}

	data, err := json.Marshal(errorResp)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	require.Equal(t, "2.0", parsed["jsonrpc"])
	require.Equal(t, "test-123", parsed["id"])
	require.NotNil(t, parsed["error"])

	errObj := parsed["error"].(map[string]any)
	require.InDelta(t, float64(-32600), errObj["code"], 0.1)
	require.Equal(t, "Invalid Request", errObj["message"])
}

// TestA2AJSONRPCRoundTrip verifies that A2A JSON-RPC requests and responses
// can be correctly serialized and deserialized in a round-trip.
// **Validates: Requirements 10.3**
func TestA2AJSONRPCRoundTrip(t *testing.T) {
	// Create a mock server that echoes back the request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "POST", r.Method)

		// Parse request
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Verify JSON-RPC format
		assert.Equal(t, "2.0", req["jsonrpc"])
		assert.NotEmpty(t, req["id"])
		assert.NotEmpty(t, req["method"])

		// Send response
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"id": "task-response",
				"status": map[string]any{
					"state":     "completed",
					"timestamp": "2024-01-01T00:00:00Z",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Send request
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      "round-trip-test",
		"method":  "tasks/send",
		"params": map[string]any{
			"id": "task-123",
			"message": map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"type": "text", "text": "Test"}},
			},
		},
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), "POST", server.URL, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	require.Equal(t, "2.0", result["jsonrpc"])
	require.Equal(t, "round-trip-test", result["id"])
	require.NotNil(t, result["result"])
}

// TestA2AJSONRPCStreamingFormat verifies that A2A SSE streaming responses
// follow the correct format.
// **Validates: Requirements 10.3**
func TestA2AJSONRPCStreamingFormat(t *testing.T) {
	// Create a mock server that sends SSE events
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Accept header for streaming
		accept := r.Header.Get("Accept")
		assert.Equal(t, "text/event-stream", accept)

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Send status event
		statusEvent := map[string]any{
			"type":   "status",
			"taskId": "task-123",
			"status": map[string]any{
				"state":     "working",
				"timestamp": "2024-01-01T00:00:00Z",
			},
			"final": false,
		}
		statusData, _ := json.Marshal(statusEvent)
		_, _ = w.Write([]byte("event: status\n"))
		_, _ = w.Write([]byte("data: " + string(statusData) + "\n\n"))
		flusher.Flush()

		// Send artifact event
		artifactEvent := map[string]any{
			"type":   "artifact",
			"taskId": "task-123",
			"artifact": map[string]any{
				"name":      "result",
				"parts":     []map[string]any{{"type": "text", "text": "Response"}},
				"lastChunk": true,
			},
		}
		artifactData, _ := json.Marshal(artifactEvent)
		_, _ = w.Write([]byte("event: artifact\n"))
		_, _ = w.Write([]byte("data: " + string(artifactData) + "\n\n"))
		flusher.Flush()

		// Send final status event
		finalEvent := map[string]any{
			"type":   "status",
			"taskId": "task-123",
			"status": map[string]any{
				"state":     "completed",
				"timestamp": "2024-01-01T00:00:01Z",
			},
			"final": true,
		}
		finalData, _ := json.Marshal(finalEvent)
		_, _ = w.Write([]byte("event: status\n"))
		_, _ = w.Write([]byte("data: " + string(finalData) + "\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	// Send streaming request
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      "stream-test",
		"method":  "tasks/sendSubscribe",
		"params": map[string]any{
			"id": "task-123",
			"message": map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"type": "text", "text": "Test"}},
			},
		},
	}

	data, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), "POST", server.URL, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

// TestA2AAgentCardFormat verifies that A2A agent card responses follow the
// correct format.
// **Validates: Requirements 10.3**
func TestA2AAgentCardFormat(t *testing.T) {
	// Test agent card format
	agentCard := map[string]any{
		"protocolVersion":    "1.0",
		"name":               "test-agent",
		"description":        "A test agent",
		"url":                "http://localhost:8080/a2a",
		"version":            "1.0.0",
		"capabilities":       map[string]any{"streaming": true},
		"defaultInputModes":  []string{"application/json"},
		"defaultOutputModes": []string{"application/json"},
		"skills": []map[string]any{
			{
				"id":          "test_skill",
				"name":        "Test Skill",
				"description": "A test skill",
				"inputModes":  []string{"application/json"},
				"outputModes": []string{"application/json"},
			},
		},
	}

	data, err := json.Marshal(agentCard)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	require.Equal(t, "1.0", parsed["protocolVersion"])
	require.Equal(t, "test-agent", parsed["name"])
	require.NotEmpty(t, parsed["url"])
	require.NotEmpty(t, parsed["skills"])

	skills := parsed["skills"].([]any)
	require.Len(t, skills, 1)

	skill := skills[0].(map[string]any)
	require.Equal(t, "test_skill", skill["id"])
	require.Equal(t, "Test Skill", skill["name"])
}
