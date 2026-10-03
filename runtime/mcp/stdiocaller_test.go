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
	var blocked json.RawMessage
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
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": blocked, "result": map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": "late"}}); err != nil {
				return err
			}
			blocked = nil
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
		if name == "blocked" {
			blocked = request.ID
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
