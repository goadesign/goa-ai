package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	stdioHelperEnv      = "GOA_MCP_STDIO_HELPER"
	rpcMethodInitialize = "initialize"
	rpcMethodToolsCall  = "tools/call"
)

func init() { otel.SetTextMapPropagator(propagation.TraceContext{}) }

func TestHTTPCallerCallTool(t *testing.T) {
	t.Parallel()
	var traceHeader string
	var metaTrace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case rpcMethodInitialize:
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			_ = json.NewEncoder(w).Encode(resp)
		case rpcMethodToolsCall:
			traceHeader = r.Header.Get("Traceparent")
			if params, ok := req.Params.(map[string]any); ok {
				if meta, ok := params["_meta"].(map[string]any); ok {
					if tp, ok := meta["traceparent"].(string); ok {
						metaTrace = tp
					}
				}
			}
			resultJSON := `{"content":[{"type":"text","text":"{\"ok\":true}","mimeType":"application/json"}],"isError":false}`
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(resultJSON)}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ctx, expectedTrace := contextWithTrace()
	caller, err := NewHTTPCaller(ctx, HTTPOptions{Endpoint: srv.URL})
	require.NoError(t, err)
	req := CallRequest{Suite: "svc.ts", Tool: "search", Payload: json.RawMessage(`{"query":"hi"}`)}
	resp, err := caller.CallTool(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "{\"ok\":true}", string(resp.Result))
	require.Equal(t, expectedTrace, traceHeader)
	require.Equal(t, expectedTrace, metaTrace)
}

func TestSSECallerCallTool(t *testing.T) {
	t.Parallel()
	var traceHeader string
	var metaTrace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case rpcMethodInitialize:
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			_ = json.NewEncoder(w).Encode(resp)
		case rpcMethodToolsCall:
			traceHeader = r.Header.Get("Traceparent")
			if params, ok := req.Params.(map[string]any); ok {
				if meta, ok := params["_meta"].(map[string]any); ok {
					if tp, ok := meta["traceparent"].(string); ok {
						metaTrace = tp
					}
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			resultJSON := `{"content":[{"type":"text","text":"{\"ok\":true}","mimeType":"application/json"}],"isError":false}`
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(resultJSON)}
			data, _ := json.Marshal(resp)
			_, _ = fmt.Fprintf(w, "event: response\n")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", bytes.TrimSpace(data))
			flusher.Flush()
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ctx, expectedTrace := contextWithTrace()
	caller, err := NewSSECaller(ctx, HTTPOptions{Endpoint: srv.URL})
	require.NoError(t, err)
	req := CallRequest{Suite: "svc.ts", Tool: "search", Payload: json.RawMessage(`{"query":"hi"}`)}
	resp, err := caller.CallTool(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "{\"ok\":true}", string(resp.Result))
	require.Equal(t, expectedTrace, traceHeader)
	require.Equal(t, expectedTrace, metaTrace)
}

func TestStdioCallerCallTool(t *testing.T) {
	t.Parallel()
	ctx, expectedTrace := contextWithTrace()
	caller, err := NewStdioCaller(ctx, StdioOptions{
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestStdioHelper", "--"},
		Env:         []string{stdioHelperEnv + "=1"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	defer func() { _ = caller.Close() }()
	resp, err := caller.CallTool(ctx, CallRequest{Suite: "svc.ts", Tool: "meta", Payload: json.RawMessage(`"noop"`)})
	require.NoError(t, err)
	var result string
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Equal(t, expectedTrace, result)
}

func contextWithTrace() (context.Context, string) {
	traceID := trace.TraceID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00}
	spanID := trace.SpanID{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)
	expected := fmt.Sprintf("00-%s-%s-01", traceID.String(), spanID.String())
	return ctx, expected
}

func TestStdioHelper(t *testing.T) {
	if os.Getenv(stdioHelperEnv) != "1" {
		t.Skip("helper process")
	}
	runStdioHelper()
}

func runStdioHelper() {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	for {
		frame, err := readFrame(reader)
		if err != nil {
			break
		}
		var req rpcRequest
		if err := json.Unmarshal(frame, &req); err != nil {
			continue
		}
		switch req.Method {
		case rpcMethodInitialize:
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			writeFrame(writer, resp)
		case rpcMethodToolsCall:
			traceVal := ""
			if params, ok := req.Params.(map[string]any); ok {
				if meta, ok := params["_meta"].(map[string]any); ok {
					if tp, ok := meta["traceparent"].(string); ok {
						traceVal = tp
					}
				}
			}
			if traceVal == "" {
				errResp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "missing traceparent"}}
				writeFrame(writer, errResp)
				continue
			}
			result := toolsCallResult{Content: []contentItem{{Type: "text", Text: &traceVal}}}
			data, _ := json.Marshal(result)
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: data}
			writeFrame(writer, resp)
		default:
			errResp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "unknown method"}}
			writeFrame(writer, errResp)
		}
	}
	_ = writer.Flush()
	os.Exit(0)
}

func writeFrame(writer *bufio.Writer, resp rpcResponse) {
	data, _ := json.Marshal(resp)
	_, _ = fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(data))
	_, _ = writer.Write(data)
	_ = writer.Flush()
}
