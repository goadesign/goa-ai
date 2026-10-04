// These tests send real HTTP and stdio requests through shared callers. They
// prove one operation can forbid further host input without changing its peers.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPHostInputRestrictionIsPerOperation(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		response, err := hostInputPeerResponse(raw)
		if !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(response)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	transport := NewHTTPTransport(server.Client(), ClientInfo{Name: "host-tests", Version: "1"}, nil, InputSupport{Form: true, URL: true}, HTTPRetryPolicy{})
	imported, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "host-tests", Version: "1"}, InputSupport: InputSupport{Form: true, URL: true}})
	require.NoError(t, err)
	for name, caller := range map[string]Caller{
		"transport": CallerFunc(func(ctx context.Context, req CallRequest) (CallResponse, error) {
			return transport.CallTool(ctx, server.URL, req)
		}),
		"imported": imported,
	} {
		t.Run(name, func(t *testing.T) {
			verifyHostInputRestriction(t, caller)
			before := requests.Load()
			_, err := caller.CallTool(WithoutHostInput(t.Context()), CallRequest{Tool: "restricted_complete", Payload: json.RawMessage(`{}`), Continuation: &CallContinuation{}})
			var invalid *Error
			require.ErrorAs(t, err, &invalid)
			assert.Equal(t, JSONRPCInvalidParams, invalid.Code)
			assert.Equal(t, before, requests.Load(), "restricted continuation must not start discovery or execution")
		})
	}
}

func TestStdioHostInputRestrictionIsPerOperation(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestHostInputProtocolPeer$"}, Env: []string{"GOA_AI_MCP_HOST_INPUT_PEER=1"}, ClientInfo: ClientInfo{Name: "host-tests", Version: "1"}, InputSupport: InputSupport{Form: true, URL: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	verifyHostInputRestriction(t, caller)
	_, err = caller.CallTool(WithoutHostInput(t.Context()), CallRequest{Tool: "restricted_complete", Payload: json.RawMessage(`{}`), Continuation: &CallContinuation{}})
	var invalid *Error
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, JSONRPCInvalidParams, invalid.Code)
}

// verifyHostInputRestriction checks form, URL and state-only replies, then
// calls the same client normally to prove its host capabilities were not changed.
func verifyHostInputRestriction(t *testing.T, caller Caller) {
	t.Helper()
	for _, kind := range []string{"form", "url", "state", "complete"} {
		for _, restricted := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/restricted=%t", kind, restricted), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				name := "ordinary_" + kind
				if restricted {
					ctx = WithoutHostInput(ctx)
					name = "restricted_" + kind
				}
				response, err := caller.CallTool(ctx, CallRequest{Tool: name, Payload: json.RawMessage(`{}`)})
				if restricted && kind != "complete" {
					var malformed *MalformedResponseError
					require.ErrorAs(t, err, &malformed)
					require.ErrorContains(t, err, "host input is disabled")
					assert.Nil(t, response.InputRequired)
					return
				}
				require.NoError(t, err)
				if kind == "complete" {
					assert.JSONEq(t, `42`, string(response.StructuredContent))
				} else {
					assert.NotNil(t, response.InputRequired)
				}
			})
		}
	}
}

func TestHostInputProtocolPeer(_ *testing.T) {
	if os.Getenv("GOA_AI_MCP_HOST_INPUT_PEER") != "1" {
		return
	}
	if err := serveHostInputPeer(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(3)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

// serveHostInputPeer reads separate requests and checks each one's capabilities.
// It returns raw current-protocol results without using caller decoding types.
func serveHostInputPeer() error {
	reader := bufio.NewReader(os.Stdin)
	for {
		raw, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		response, err := hostInputPeerResponse(raw)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(os.Stdout, string(response)); err != nil {
			return err
		}
	}
}

// hostInputPeerResponse independently checks metadata and returns a catalog,
// a final value, or one of the unfinished input shapes allowed by MCP.
func hostInputPeerResponse(raw []byte) ([]byte, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string                     `json:"name"`
			Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Method == "tools/list" {
		tools := []map[string]any{}
		for _, prefix := range []string{"restricted_", "ordinary_"} {
			for _, kind := range []string{"form", "url", "state", "complete"} {
				tools = append(tools, map[string]any{"name": prefix + kind, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}})
			}
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": tools, "ttlMs": 0, "cacheScope": "private"}})
	}
	if request.Method != "tools/call" {
		return nil, fmt.Errorf("unexpected method %s", request.Method)
	}
	var capabilities struct {
		Elicitation map[string]json.RawMessage `json:"elicitation"`
	}
	if err := json.Unmarshal(request.Params.Meta["io.modelcontextprotocol/clientCapabilities"], &capabilities); err != nil {
		return nil, err
	}
	restricted := strings.HasPrefix(request.Params.Name, "restricted_")
	if restricted && len(capabilities.Elicitation) != 0 {
		return nil, errors.New("restricted operation advertised host input")
	}
	if !restricted && (len(capabilities.Elicitation["form"]) == 0 || len(capabilities.Elicitation["url"]) == 0) {
		return nil, errors.New("ordinary operation lost host input support")
	}
	kind := strings.TrimPrefix(strings.TrimPrefix(request.Params.Name, "restricted_"), "ordinary_")
	result := map[string]any{"resultType": "input_required", "requestState": "saved"}
	switch kind {
	case "form":
		result["inputRequests"] = map[string]any{"choice": map[string]any{"method": "elicitation/create", "params": map[string]any{"message": "Choose a value", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"type": "string"}}}}}}
	case "url":
		result["inputRequests"] = map[string]any{"consent": map[string]any{"method": "elicitation/create", "params": map[string]any{"mode": "url", "message": "Continue outside this client", "url": "https://example.test/consent"}}}
	case "state":
	case "complete":
		result = map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": 42}
	default:
		return nil, fmt.Errorf("unknown tool %s", request.Params.Name)
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}
