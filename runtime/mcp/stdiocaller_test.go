// These tests start a synthetic protocol peer in a separate process. It
// checks each request's metadata and cancellation ID before returning results.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStdioCallerStatelessRequestsAndCancellation(t *testing.T) {
	caller, err := NewStdioCaller(context.Background(), StdioOptions{
		Command: os.Args[0], Args: []string{"-test.run=^TestStdioProtocolPeer$"},
		Env:          []string{"GOA_AI_MCP_PROTOCOL_PEER=1"},
		ClientInfo:   ClientInfo{Name: "stdio-tests", Version: "1"},
		InputSupport: InputSupport{Form: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := caller.CallTool(ctx, CallRequest{Tool: "first", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"first"`, string(response.StructuredContent))
	interrupted, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = caller.CallTool(interrupted, CallRequest{Tool: "blocked", Payload: json.RawMessage(`{}`)})
	stop()
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	response, err = caller.CallTool(ctx, CallRequest{Tool: "after-cancel", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"after-cancel"`, string(response.StructuredContent))
	_, err = caller.CallTool(ctx, CallRequest{Tool: "error", Payload: json.RawMessage(`{}`)})
	var protocolError *Error
	require.ErrorAs(t, err, &protocolError)
	assert.Equal(t, UnsupportedProtocolVersion, protocolError.Code)
	assert.JSONEq(t, `{"requested":"unsupported","supported":["2026-07-28"]}`, string(protocolError.Data))
}

func TestStdioProtocolPeer(_ *testing.T) {
	if os.Getenv("GOA_AI_MCP_PROTOCOL_PEER") != "1" {
		return
	}
	if err := serveStdioProtocolPeer(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(3)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

// serveStdioProtocolPeer rejects a handshake or an incorrect cancellation ID.
// It emits a late canceled response to prove that the next call stays correlated.
func serveStdioProtocolPeer() error {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var blocked, blockedToken json.RawMessage
	type peerCall struct {
		id, token json.RawMessage
		name      string
	}
	var pair []peerCall
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return nil
			}
			if len(line) == 0 {
				return nil
			}
			return err
		}
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			return err
		}
		if request.Method == "notifications/cancelled" {
			if string(blocked) != string(request.Params["requestId"]) {
				return errors.New("cancellation ID changed")
			}
			if len(blockedToken) > 0 {
				if err := writePeerProgress(encoder, blockedToken, 200); err != nil {
					return err
				}
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": blocked, "result": map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": "late"}}); err != nil {
				return err
			}
			blocked, blockedToken = nil, nil
			continue
		}
		if request.Method == "tools/list" {
			if err := writePeerToolCatalog(encoder, request.ID, []string{"first", "blocked", "after-cancel", "error", "progress", "progress-fail", "pair-one", "pair-two", "app-only"}); err != nil {
				return err
			}
			continue
		}
		if request.Method != "tools/call" {
			return fmt.Errorf("unexpected method %s", request.Method)
		}
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(request.Params["_meta"], &meta); err != nil {
			return err
		}
		if string(meta[protocolVersionKey]) != `"2026-07-28"` {
			return errors.New("wrong protocol metadata")
		}
		var capabilities struct {
			Elicitation map[string]json.RawMessage `json:"elicitation"`
		}
		if err := json.Unmarshal(meta[clientCapabilitiesKey], &capabilities); err != nil {
			return err
		}
		if _, ok := capabilities.Elicitation["form"]; !ok {
			return errors.New("form capability missing")
		}
		if _, ok := capabilities.Elicitation["url"]; ok {
			return errors.New("URL capability falsely advertised")
		}
		var name string
		if err := json.Unmarshal(request.Params["name"], &name); err != nil {
			return err
		}
		if name == "app-only" {
			return errors.New("model caller executed an app-only tool")
		}
		if name == "blocked" {
			blocked = request.ID
			continue
		}
		if name == "progress" || name == "progress-fail" {
			token := meta["progressToken"]
			if len(token) == 0 {
				return errors.New("progress token missing")
			}
			if name == "progress-fail" {
				blocked, blockedToken = request.ID, token
			}
			for _, value := range []float64{0, 50, 100} {
				if err := writePeerProgress(encoder, token, value); err != nil {
					return err
				}
			}
			if name == "progress-fail" {
				continue
			}
		}
		if name == "pair-one" || name == "pair-two" {
			pair = append(pair, peerCall{request.ID, meta["progressToken"], name})
			if len(pair) < 2 {
				continue
			}
			for _, value := range []float64{0, 50, 100} {
				for _, call := range pair {
					if err := writePeerProgress(encoder, call.token, value); err != nil {
						return err
					}
				}
			}
			for _, call := range pair {
				if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": call.id, "result": map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": call.name}}); err != nil {
					return err
				}
			}
			pair = nil
			continue
		}
		if name == "error" {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": UnsupportedProtocolVersion, "message": "unsupported", "data": map[string]any{"requested": "unsupported", "supported": []string{ProtocolVersion}}}}); err != nil {
				return err
			}
			continue
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": name}}); err != nil {
			return err
		}
	}
}

// writePeerToolCatalog describes the synthetic process's tools before execution.
// The app-only entry lets callers prove that visibility prevents a tool request.
func writePeerToolCatalog(encoder *json.Encoder, id json.RawMessage, names []string) error {
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tool := map[string]any{"name": name, "inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false}`), "outputSchema": json.RawMessage(`{"type":"string"}`)}
		if name == "app-only" {
			tool["_meta"] = json.RawMessage(`{"ui":{"visibility":["app"]}}`)
		}
		tools = append(tools, tool)
	}
	return encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"resultType": "complete", "tools": tools, "ttlMs": 0, "cacheScope": "private"}})
}

// writePeerProgress emits an independent protocol notification using the exact
// client token so the tests exercise stdio correlation without server helpers.
func writePeerProgress(encoder *json.Encoder, token json.RawMessage, value float64) error {
	return encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"progressToken": token, "progress": value, "total": 100}})
}

func TestStdioProgressParallelCallsAndCallbackFailure(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioProtocolPeer$"}, Env: []string{"GOA_AI_MCP_PROTOCOL_PEER=1"}, ClientInfo: ClientInfo{Name: "progress-tests", Version: "1"}, InputSupport: InputSupport{Form: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type observation struct {
		name     string
		updates  []Progress
		response CallResponse
		err      error
	}
	finished := make(chan observation, 2)
	for _, name := range []string{"pair-one", "pair-two"} {
		go func() {
			var updates []Progress
			callCtx := WithProgress(ctx, func(_ context.Context, p Progress) error { updates = append(updates, p); return nil })
			response, err := caller.CallTool(callCtx, CallRequest{Tool: name, Payload: json.RawMessage(`{}`)})
			finished <- observation{name, updates, response, err}
		}()
	}
	requestIDs := make([]json.RawMessage, 0, 2)
	for range 2 {
		result := <-finished
		require.NoError(t, result.err)
		assert.JSONEq(t, fmt.Sprintf("%q", result.name), string(result.response.StructuredContent))
		require.Len(t, result.updates, 3)
		requestIDs = append(requestIDs, result.updates[0].RequestID)
		for index, update := range result.updates {
			assert.InDelta(t, float64(index*50), update.Value, 0)
			assert.Equal(t, result.updates[0].RequestID, update.RequestID)
		}
	}
	assert.NotEqual(t, requestIDs[0], requestIDs[1])
	failure := errors.New("host stopped progress")
	failedCtx := WithProgress(ctx, func(context.Context, Progress) error { return failure })
	_, err = caller.CallTool(failedCtx, CallRequest{Tool: "progress-fail", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, failure)
	var observed []Progress
	goodCtx := WithProgress(ctx, func(_ context.Context, p Progress) error { observed = append(observed, p); return nil })
	response, err := caller.CallTool(goodCtx, CallRequest{Tool: "progress", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"progress"`, string(response.StructuredContent))
	require.Len(t, observed, 3)
	assert.Zero(t, observed[0].Value)
}
