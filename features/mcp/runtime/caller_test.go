package runtime

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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const stdioHelperEnv = "GOA_MCP_STDIO_HELPER"

func init() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

func TestHTTPCallerCallTool(t *testing.T) {
	t.Parallel()
	var traceHeader string
	var metaTrace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/call":
			traceHeader = r.Header.Get("Traceparent")
			if params, ok := req.Params.(map[string]any); ok {
				if meta, ok := params["_meta"].(map[string]any); ok {
					if tp, ok := meta["traceparent"].(string); ok {
						metaTrace = tp
					}
				}
			}
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID,
				Result: json.RawMessage(`{"content":[{"type":"text","text":"{\"ok\":true}","mimeType":"application/json"}],"isError":false}`)}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ctx, expectedTrace := contextWithTrace()
	caller, err := NewHTTPCaller(ctx, HTTPOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("new caller: %v", err)
	}
	resp, err := caller.CallTool(ctx, CallRequest{Suite: "svc.ts", Tool: "search", Payload: json.RawMessage(`{"query":"hi"}`)})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if got := string(resp.Result); got != "{\"ok\":true}" {
		t.Fatalf("unexpected result: %s", got)
	}
	if traceHeader != expectedTrace {
		t.Fatalf("expected header %s got %s", expectedTrace, traceHeader)
	}
	if metaTrace != expectedTrace {
		t.Fatalf("expected meta trace %s got %s", expectedTrace, metaTrace)
	}
}

func TestSSECallerCallTool(t *testing.T) {
	t.Parallel()
	var traceHeader string
	var metaTrace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/call":
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
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID,
				Result: json.RawMessage(`{"content":[{"type":"text","text":"{\"ok\":true}","mimeType":"application/json"}],"isError":false}`)}
			data, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: response\n")
			fmt.Fprintf(w, "data: %s\n\n", bytes.TrimSpace(data))
			flusher.Flush()
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	ctx, expectedTrace := contextWithTrace()
	caller, err := NewSSECaller(ctx, HTTPOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("new caller: %v", err)
	}
	resp, err := caller.CallTool(ctx, CallRequest{Suite: "svc.ts", Tool: "search", Payload: json.RawMessage(`{"query":"hi"}`)})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if string(resp.Result) != "{\"ok\":true}" {
		t.Fatalf("unexpected result: %s", resp.Result)
	}
	if traceHeader != expectedTrace {
		t.Fatalf("expected header %s got %s", expectedTrace, traceHeader)
	}
	if metaTrace != expectedTrace {
		t.Fatalf("expected meta trace %s got %s", expectedTrace, metaTrace)
	}
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
	if err != nil {
		t.Fatalf("new stdio caller: %v", err)
	}
	defer caller.Close()
	resp, err := caller.CallTool(ctx, CallRequest{Suite: "svc.ts", Tool: "meta", Payload: json.RawMessage(`"noop"`)})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result != expectedTrace {
		t.Fatalf("expected trace %s got %s", expectedTrace, result)
	}
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
		case "initialize":
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)}
			writeFrame(writer, resp)
		case "tools/call":
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
	writer.Flush()
	os.Exit(0)
}

func writeFrame(writer *bufio.Writer, resp rpcResponse) {
	data, _ := json.Marshal(resp)
	fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(data))
	writer.Write(data)
	writer.Flush()
}
