// Package codegen checks that Goa core and the MCP plugin write one attached service
// from the same generation run.
package codegen

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const callerLifecycleGeneratedTestSource = `// This file checks that the generated caller starts the generated MCP service before calling a tool.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	mcpfmt "generated.local/gen/mcp_fmt"
	mcpfmtsrv "generated.local/gen/jsonrpc/mcp_fmt/server"
	goahttp "goa.design/goa/v3/http"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

type echoService struct {
	calls int
}

type countingDoer struct {
	calls int
}

func writeSSE(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode SSE message: %v", err)
	}
	if _, err := io.WriteString(w, "data: "+string(data)+"\n\n"); err != nil {
		t.Fatalf("write SSE message: %v", err)
	}
}

func (d *countingDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return nil, context.Canceled
}

func (s *echoService) Echo(_ context.Context, value string) (string, error) {
	s.calls++
	return value, nil
}

func TestCallerRejectsIncompleteClientInfoBeforeRequest(t *testing.T) {
	doer := new(countingDoer)
	client := NewClient(
		"http",
		"mcp.invalid",
		doer,
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)
	_, err := NewCaller(context.Background(), client, mcpruntime.ClientInfo{Version: "1.0.0"})
	if err == nil || err.Error() != "mcp: client name is required" {
		t.Fatalf("NewCaller() error = %v, want missing client name", err)
	}
	if doer.calls != 0 {
		t.Fatalf("initialize requests = %d, want 0", doer.calls)
	}
}

func TestCallerRejectsDifferentProtocolVersion(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode initialize request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request["id"],
			"result": map[string]any{
				"protocolVersion": "2099-01-01",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "other-server",
					"version": "1.0.0",
				},
			},
		}); err != nil {
			t.Fatalf("encode initialize response: %v", err)
		}
	}))
	defer httpServer.Close()

	serverURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client := NewClient(
		serverURL.Scheme,
		serverURL.Host,
		httpServer.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)
	_, err = NewCaller(context.Background(), client, mcpruntime.ClientInfo{
		Name:    "generated-test",
		Version: "1.0.0",
	})
	if err == nil || !strings.Contains(err.Error(), "server selected protocol version \"2099-01-01\"") {
		t.Fatalf("NewCaller() error = %v, want protocol version mismatch", err)
	}
}

func TestCallerReadsEventStreamAndAnswersServerPing(t *testing.T) {
	pingReply := make(chan struct{}, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode MCP message: %v", err)
		}
		method, _ := request["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Content-Type", "text/event-stream")
			writeSSE(t, w, map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"protocolVersion": mcpfmt.DefaultProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "plain-server", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			writeSSE(t, w, map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/message",
				"params":  map[string]any{"level": "info", "data": "working"},
			})
			writeSSE(t, w, map[string]any{
				"jsonrpc": "2.0",
				"id":      "server-ping",
				"method":  "ping",
				"params":  map[string]any{},
			})
			writeSSE(t, w, map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "hello"}},
				},
			})
		default:
			if request["id"] != "server-ping" || request["result"] == nil {
				t.Fatalf("unexpected MCP message: %#v", request)
			}
			pingReply <- struct{}{}
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer httpServer.Close()

	serverURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client := NewClient(
		serverURL.Scheme,
		serverURL.Host,
		httpServer.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)
	caller, err := NewCaller(context.Background(), client, mcpruntime.ClientInfo{
		Name:    "generated-test",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("initialize caller: %v", err)
	}
	result, err := caller.CallTool(context.Background(), mcpruntime.CallRequest{
		Tool:    "echo",
		Payload: json.RawMessage("\"hello\""),
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0] != "hello" {
		t.Fatalf("tool content = %#v, want one hello block", result.Content)
	}
	select {
	case <-pingReply:
	default:
		t.Fatal("generated client did not answer the server ping")
	}
}

func TestCallerInitializesBeforeCallingTool(t *testing.T) {
	service := new(echoService)
	adapter := mcpfmt.NewMCPAdapter(service, nil)
	endpoints := mcpfmt.NewEndpoints(adapter)
	mux := goahttp.NewMuxer()
	server := mcpfmtsrv.New(
		endpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		func(_ context.Context, _ http.ResponseWriter, err error) {
			t.Errorf("serve MCP request: %v", err)
		},
	)
	mcpfmtsrv.Mount(mux, server)
	var methods []string
	var hasIDs []bool
	var protocolVersions []string
	var acceptHeaders []string
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read MCP request: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request map[string]json.RawMessage
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		var method string
		if err := json.Unmarshal(request["method"], &method); err != nil {
			t.Fatalf("decode MCP method: %v", err)
		}
		methods = append(methods, method)
		_, hasID := request["id"]
		hasIDs = append(hasIDs, hasID)
		protocolVersions = append(protocolVersions, r.Header.Get("MCP-Protocol-Version"))
		acceptHeaders = append(acceptHeaders, r.Header.Get("Accept"))
		if method == "initialize" {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(request["params"], &params); err != nil {
				t.Fatalf("decode initialize parameters: %v", err)
			}
			if string(params["capabilities"]) != "{}" {
				t.Fatalf("initialize capabilities = %s, want {}", params["capabilities"])
			}
		}
		mux.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(observed)
	defer httpServer.Close()

	serverURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client := NewClient(
		serverURL.Scheme,
		serverURL.Host,
		httpServer.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)
	caller, err := NewCaller(context.Background(), client, mcpruntime.ClientInfo{
		Name:    "generated-test",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("initialize caller: %v", err)
	}
	if len(methods) != 2 || methods[0] != "initialize" || methods[1] != "notifications/initialized" {
		t.Fatalf("initialization methods = %#v", methods)
	}
	if !hasIDs[0] || hasIDs[1] {
		t.Fatalf("initialization ID presence = %#v, want [true false]", hasIDs)
	}
	if protocolVersions[0] != "" || protocolVersions[1] != mcpfmt.DefaultProtocolVersion {
		t.Fatalf("initialization protocol headers = %#v", protocolVersions)
	}
	for _, accept := range acceptHeaders {
		if accept != "application/json, text/event-stream" {
			t.Fatalf("Accept header = %q", accept)
		}
	}

	result, err := caller.CallTool(context.Background(), mcpruntime.CallRequest{
		Tool:    "echo",
		Payload: json.RawMessage(` + "`\"hello\"`" + `),
	})
	if err != nil {
		t.Fatalf("call echo tool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0] != "hello" {
		t.Fatalf("tool content = %#v, want one hello block", result.Content)
	}
	if service.calls != 1 {
		t.Fatalf("service calls = %d, want 1", service.calls)
	}
	if protocolVersions[2] != mcpfmt.DefaultProtocolVersion {
		t.Fatalf("tool protocol header = %q", protocolVersions[2])
	}
}

func TestCallerStartsNewSessionAfterExpiryWithoutRepeatingTool(t *testing.T) {
	var initializeCalls int
	var toolCalls int
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode MCP message: %v", err)
		}
		method, _ := request["method"].(string)
		switch method {
		case "initialize":
			initializeCalls++
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", initializeCalls))
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"protocolVersion": mcpfmt.DefaultProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "session-server", "version": "1.0.0"},
				},
			}); err != nil {
				t.Fatalf("encode initialize response: %v", err)
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls++
			if toolCalls == 1 {
				if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
					t.Fatalf("first tool session = %q, want session-1", got)
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "session-2" {
				t.Fatalf("second tool session = %q, want session-2", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "hello"}},
				},
			}); err != nil {
				t.Fatalf("encode tool response: %v", err)
			}
		default:
			t.Fatalf("unexpected MCP method %q", method)
		}
	}))
	defer httpServer.Close()

	serverURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client := NewClient(
		serverURL.Scheme,
		serverURL.Host,
		httpServer.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)
	caller, err := NewCaller(context.Background(), client, mcpruntime.ClientInfo{
		Name:    "generated-test",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("initialize caller: %v", err)
	}
	_, err = caller.CallTool(context.Background(), mcpruntime.CallRequest{
		Tool:    "echo",
		Payload: json.RawMessage(` + "`\"hello\"`" + `),
	})
	if err == nil {
		t.Fatal("expired tool call succeeded")
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls after expiry = %d, want 1", toolCalls)
	}
	if initializeCalls != 2 {
		t.Fatalf("initialize calls after expiry = %d, want 2", initializeCalls)
	}
	result, err := caller.CallTool(context.Background(), mcpruntime.CallRequest{
		Tool:    "echo",
		Payload: json.RawMessage(` + "`\"hello\"`" + `),
	})
	if err != nil {
		t.Fatalf("call tool with replacement session: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0] != "hello" {
		t.Fatalf("tool content = %#v, want one hello block", result.Content)
	}
	if toolCalls != 2 {
		t.Fatalf("tool calls after recovery = %d, want 2", toolCalls)
	}
}

func TestMCPTransportEnforcesStreamableHTTPRequests(t *testing.T) {
	service := new(echoService)
	adapter := mcpfmt.NewMCPAdapter(service, nil)
	endpoints := mcpfmt.NewEndpoints(adapter)
	mux := goahttp.NewMuxer()
	server := mcpfmtsrv.New(
		endpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		func(_ context.Context, _ http.ResponseWriter, err error) {
			t.Errorf("serve MCP request: %v", err)
		},
	)
	mcpfmtsrv.Mount(mux, server)

	batch := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(` + "`" + `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":"hello"}}]` + "`" + `))
	batch.Header.Set("Content-Type", "application/json")
	batchResponse := httptest.NewRecorder()
	mux.ServeHTTP(batchResponse, batch)
	if batchResponse.Code != http.StatusOK {
		t.Fatalf("batch HTTP status = %d, want 200", batchResponse.Code)
	}
	var response struct {
		Error struct {
			Code int ` + "`" + `json:"code"` + "`" + `
		} ` + "`" + `json:"error"` + "`" + `
	}
	if err := json.Unmarshal(batchResponse.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if response.Error.Code != -32600 {
		t.Fatalf("batch error code = %d, want -32600", response.Error.Code)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(` + "`" + `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":"hello"}}` + "`" + `))
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "https://other.example")
	crossOriginResponse := httptest.NewRecorder()
	mux.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin HTTP status = %d, want 403", crossOriginResponse.Code)
	}

	sameHostOrigin := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(` + "`" + `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":"hello"}}` + "`" + `))
	sameHostOrigin.Header.Set("Content-Type", "application/json")
	sameHostOrigin.Header.Set("Origin", "https://example.com")
	sameHostOriginResponse := httptest.NewRecorder()
	mux.ServeHTTP(sameHostOriginResponse, sameHostOrigin)
	if sameHostOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("unlisted same-host Origin HTTP status = %d, want 403", sameHostOriginResponse.Code)
	}

	get := httptest.NewRequest(http.MethodGet, mcpfmtsrv.ToolsCallMcpFmtPath(), nil)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET HTTP status = %d, want 405", getResponse.Code)
	}

	crossOriginGET := httptest.NewRequest(http.MethodGet, mcpfmtsrv.ToolsCallMcpFmtPath(), nil)
	crossOriginGET.Header.Set("Origin", "https://other.example")
	crossOriginGETResponse := httptest.NewRecorder()
	mux.ServeHTTP(crossOriginGETResponse, crossOriginGET)
	if crossOriginGETResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin GET HTTP status = %d, want 403", crossOriginGETResponse.Code)
	}

	pingBody := ` + "`" + `{"jsonrpc":"2.0","id":"ping-1","method":"ping","params":{}}` + "`" + `
	browserMux := goahttp.NewMuxer()
	browserServer := mcpfmtsrv.New(
		endpoints,
		browserMux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		func(_ context.Context, _ http.ResponseWriter, err error) {
			t.Errorf("serve browser MCP request: %v", err)
		},
	)
	mcpfmtsrv.MountWithOrigins(browserMux, browserServer, []string{"https://app.example"})
	allowedOrigin := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(pingBody))
	allowedOrigin.Header.Set("Content-Type", "application/json")
	allowedOrigin.Header.Set("MCP-Protocol-Version", mcpfmt.DefaultProtocolVersion)
	allowedOrigin.Header.Set("Origin", "https://app.example")
	allowedOriginResponse := httptest.NewRecorder()
	browserMux.ServeHTTP(allowedOriginResponse, allowedOrigin)
	if allowedOriginResponse.Code != http.StatusOK {
		t.Fatalf("allowed Origin HTTP status = %d, want 200", allowedOriginResponse.Code)
	}

	differentOrigin := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(pingBody))
	differentOrigin.Header.Set("Content-Type", "application/json")
	differentOrigin.Header.Set("MCP-Protocol-Version", mcpfmt.DefaultProtocolVersion)
	differentOrigin.Header.Set("Origin", "https://APP.example")
	differentOriginResponse := httptest.NewRecorder()
	browserMux.ServeHTTP(differentOriginResponse, differentOrigin)
	if differentOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("different Origin HTTP status = %d, want 403", differentOriginResponse.Code)
	}

	allowedOriginGET := httptest.NewRequest(http.MethodGet, mcpfmtsrv.ToolsCallMcpFmtPath(), nil)
	allowedOriginGET.Header.Set("Origin", "https://app.example")
	allowedOriginGETResponse := httptest.NewRecorder()
	browserMux.ServeHTTP(allowedOriginGETResponse, allowedOriginGET)
	if allowedOriginGETResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("allowed Origin GET HTTP status = %d, want 405", allowedOriginGETResponse.Code)
	}

	missingVersion := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(pingBody))
	missingVersion.Header.Set("Content-Type", "application/json")
	missingVersionResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingVersionResponse, missingVersion)
	if missingVersionResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing protocol version HTTP status = %d, want 400", missingVersionResponse.Code)
	}

	wrongVersion := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(pingBody))
	wrongVersion.Header.Set("Content-Type", "application/json")
	wrongVersion.Header.Set("MCP-Protocol-Version", "2099-01-01")
	wrongVersionResponse := httptest.NewRecorder()
	mux.ServeHTTP(wrongVersionResponse, wrongVersion)
	if wrongVersionResponse.Code != http.StatusBadRequest {
		t.Fatalf("unsupported protocol version HTTP status = %d, want 400", wrongVersionResponse.Code)
	}

	validVersion := httptest.NewRequest(http.MethodPost, mcpfmtsrv.ToolsCallMcpFmtPath(), strings.NewReader(pingBody))
	validVersion.Header.Set("Content-Type", "application/json")
	validVersion.Header.Set("MCP-Protocol-Version", mcpfmt.DefaultProtocolVersion)
	validVersionResponse := httptest.NewRecorder()
	mux.ServeHTTP(validVersionResponse, validVersion)
	if validVersionResponse.Code != http.StatusOK {
		t.Fatalf("supported protocol version HTTP status = %d, want 200", validVersionResponse.Code)
	}
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0", service.calls)
	}
}
`

const registerStringGeneratedTestSource = `// This file checks that generated remote-tool registration preserves every JSON string character.
package mcpfmt

import (
	"context"
	"encoding/json"
	"testing"

	"goa.design/goa-ai/runtime/agent/rawjson"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

func TestRegisteredStringToolDecodesEveryControlCharacter(t *testing.T) {
	want := "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
	caller := mcpruntime.CallerFunc(func(context.Context, mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
		return mcpruntime.CallResponse{Content: []string{want}}, nil
	})
	rt := agentsruntime.New()
	if err := RegisterFmtFmtToolset(context.Background(), rt, caller); err != nil {
		t.Fatal(err)
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &agentsruntime.ToolInput{
		ToolsetName: "fmt.fmt",
		ToolName:    tools.Ident("echo"),
		Payload:     rawjson.Message(` + "`{}`" + `),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Failure != nil {
		t.Fatalf("string result failed: %s", out.Failure.Error.Message)
	}
	var got string
	if err := json.Unmarshal(out.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("string result = %q, want %q", got, want)
	}
}
`

func TestMCPPluginUsesCorePlanForAttachedService(t *testing.T) {
	goaAIDirectory := testModuleDirectory(t, "goa.design/goa-ai")
	goaDirectory := testModuleDirectory(t, "goa.design/goa/v3")
	// The generated module is intentionally separate from the workspace that
	// compiled this test.
	t.Setenv("GOWORK", "off")
	restoreMCP := resetMCPCodegenState(t)
	defer restoreMCP()
	previousRoot := expr.Root
	defer func() {
		expr.Root = previousRoot
		eval.Reset()
	}()

	service, methods := testService("calc", "add", "subtract")
	namedTypes := setNamedMCPMethodTypes(methods["add"], methods["subtract"])
	formatter, formatterMethods := testService("formatter", "render")
	locatedRenderPayload := testLocatedRenderPayload()
	formatterMethods["render"].Payload = &expr.AttributeExpr{Type: locatedRenderPayload}
	selector, selectorMethods := testService("selector", "read_value", "read-value")
	contextService, contextMethods := testService("context", "ping")
	fmtService, fmtMethods := testService("fmt", "echo")
	fmtMethods["echo"].Payload = &expr.AttributeExpr{Type: expr.String}
	prompts, _ := testService("prompts")
	staticPrompts, _ := testService("static_prompts")
	resources, resourceMethods := testService("resources", "read_document")
	root := testRootExpr([]*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, resources}, []*expr.HTTPServiceExpr{
		jsonrpcService(service, "/calc"),
		jsonrpcService(formatter, "/formatter"),
		jsonrpcService(selector, "/selector"),
		jsonrpcService(contextService, "/context"),
		jsonrpcService(fmtService, "/fmt"),
		jsonrpcService(prompts, "/prompts"),
		jsonrpcService(staticPrompts, "/static-prompts"),
		jsonrpcService(resources, "/resources"),
	})
	httpService := root.API.HTTP.ServiceFor(service, root.API.HTTP)
	httpEndpoint := httpService.EndpointFor(methods["add"])
	httpEndpoint.Routes = []*expr.RouteExpr{{
		Method:   http.MethodPost,
		Path:     "/calculate",
		Endpoint: httpEndpoint,
	}}
	root.API.Name = "calc"
	root.API.Version = "1.0"
	root.API.GRPC = &expr.GRPCExpr{}
	root.API.RandomizerFactory = expr.NewDeterministicRandomizerFactory()
	root.Types = append(root.Types, namedTypes...)
	root.Types = append(root.Types, locatedRenderPayload)
	root.WalkSets(func(eval.ExpressionSet) {})
	for _, current := range []*expr.ServiceExpr{service, formatter, selector, contextService, fmtService, prompts, staticPrompts, resources} {
		for _, method := range current.Methods {
			method.Prepare()
		}
	}
	httpService.Prepare()
	httpEndpoint.Prepare()
	httpEndpoint.Finalize()
	expr.Root = root
	eval.Reset()
	require.NoError(t, eval.Register(root))
	require.NoError(t, eval.Register(mcpexpr.Root))
	mcpexpr.Root.RegisterMCP(service, &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
			{Name: "subtract", Method: methods["subtract"]},
		},
	})
	mcpexpr.Root.RegisterMCP(formatter, &mcpexpr.MCPExpr{
		Name:    "formatter",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "render", Method: formatterMethods["render"]},
		},
	})
	mcpexpr.Root.RegisterMCP(selector, &mcpexpr.MCPExpr{
		Name:    "selector",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "read_value", Method: selectorMethods["read_value"]},
			{Name: "read-value", Method: selectorMethods["read-value"]},
		},
	})
	mcpexpr.Root.RegisterMCP(contextService, &mcpexpr.MCPExpr{
		Name:    "context",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "ping", Method: contextMethods["ping"]},
		},
	})
	mcpexpr.Root.RegisterMCP(fmtService, &mcpexpr.MCPExpr{
		Name:    "fmt",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "echo", Method: fmtMethods["echo"]},
		},
	})
	mcpexpr.Root.RegisterMCP(prompts, &mcpexpr.MCPExpr{
		Name:    "prompts",
		Version: "1.0.0",
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "daily_report",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Summarize today.",
			}},
		}},
	})
	mcpexpr.Root.RegisterMCP(staticPrompts, &mcpexpr.MCPExpr{
		Name:    "static-prompts",
		Version: "1.0.0",
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "help",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Explain the available actions.",
			}},
		}},
	})
	mcpexpr.Root.RegisterMCP(resources, &mcpexpr.MCPExpr{
		Name:    "resources",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", MimeType: "application/json", Method: resourceMethods["read_document"]},
		},
	})
	// This test builds MCP expressions directly, so finish the expressions before
	// passing them to the generator just as Goa's DSL evaluation does.
	for _, mcp := range mcpexpr.Root.MCPServers {
		mcp.Finalize()
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte(fmt.Sprintf(`module generated.local

go 1.25

require (
	goa.design/goa-ai v0.0.0
	goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s

replace goa.design/goa/v3 => %s
`, filepath.ToSlash(goaAIDirectory), filepath.ToSlash(goaDirectory))),
		0o600,
	))

	_, err := goagenerator.Generate(dir, "gen", false)

	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "gen", "mcp_calc", "service.go"))
	require.FileExists(t, filepath.Join(dir, "gen", "mcp_calc", "adapter_server.go"))
	require.FileExists(t, filepath.Join(dir, "gen", "http", "calc", "server", "server.go"))
	generatedRoot, err := os.OpenRoot(filepath.Join(dir, "gen"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, generatedRoot.Close())
	})
	_, err = generatedRoot.Stat("mcp_calc/adapter/client")
	require.ErrorIs(t, err, os.ErrNotExist)
	serviceSource, err := generatedRoot.ReadFile("mcp_calc/service.go")
	require.NoError(t, err)
	adapterSource, err := generatedRoot.ReadFile("mcp_calc/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(serviceSource), "package mcpcalc")
	require.Contains(t, string(adapterSource), "package mcpcalc")
	codec, err := generatedRoot.ReadFile("mcp_calc/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(codec), "func DecodeAddPayload(")
	require.Contains(t, string(codec), "func EncodeAddResult(")
	require.Contains(t, string(codec), "func DecodeAddResult(")
	require.Contains(t, string(codec), "func EncodeAddResult(in *calc.CalculationResponse)")
	require.Contains(t, string(codec), "func DecodeAddResult(data []byte) (out *calc.CalculationResponse, err error)")
	require.Contains(t, string(codec), "CalculationRequest")
	require.Contains(t, string(codec), "Operand")
	require.Contains(t, string(codec), "v *calc.Calculation")
	require.Contains(t, string(codec), "func DecodeSubtractPayload(")
	adapterServer, err := generatedRoot.ReadFile("mcp_calc/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(adapterServer), "mcpcodec.DecodeAddPayload(arguments)")
	require.Contains(t, string(adapterServer), "mcpcodec.EncodeAddResult(result)")
	require.NotContains(t, string(adapterServer), "\n\t\"bytes\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"io\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"net/http\"\n")
	require.NotContains(t, string(adapterServer), "\n\t\"strings\"\n")
	register, err := generatedRoot.ReadFile("mcp_calc/register.go")
	require.NoError(t, err)
	require.Contains(t, string(register), `Name:        "*shared.CalculationRequest"`)
	require.Contains(t, string(register), `Name:   "*calc.CalculationResponse"`)
	require.Contains(t, string(register), `Name:        "*shared2.SubtractRequest"`)
	require.Contains(t, string(register), `Name:   "*shared2.SubtractResponse"`)
	_, err = os.Stat(filepath.Join(dir, "gen", "jsonrpc", "calc", "client"))
	require.ErrorIs(t, err, os.ErrNotExist)
	server, err := generatedRoot.ReadFile("jsonrpc/mcp_calc/server/server.go")
	require.NoError(t, err)
	require.Contains(t, string(server), "\n\t\"bytes\"\n")
	require.Contains(t, string(server), "withMCPTransport(h, allowedOrigins, h.ServeHTTP)")
	require.Contains(t, string(server), "func MountWithOrigins(")
	require.Contains(t, string(server), `mux.Handle("GET", "/calc", mcpGETHandler(allowedOrigins))`)
	require.Contains(t, string(server), `r.Header.Get("MCP-Protocol-Version") != mcpcalc.DefaultProtocolVersion`)
	require.Contains(t, string(server), `jsonrpc.MakeErrorResponse(nil, jsonrpc.InvalidRequest, "Invalid request", nil)`)
	formatterCodec, err := generatedRoot.ReadFile("mcp_formatter/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(formatterCodec), "func DecodeRenderPayload(")
	require.Contains(t, string(formatterCodec), "func EncodeRenderResult(")
	require.Contains(t, string(formatterCodec), "func DecodeRenderResult(")
	formatterServer, err := generatedRoot.ReadFile("mcp_formatter/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(formatterServer), "mcpcodec.DecodeRenderPayload(arguments)")
	require.Contains(t, string(formatterServer), "text := string(result)")
	_, err = generatedRoot.Stat("mcp_prompts/prompt_provider.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	promptServer, err := generatedRoot.ReadFile("mcp_prompts/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(promptServer), `case "daily_report":`)
	require.NotContains(t, string(promptServer), "PromptProvider")
	require.NotContains(t, string(promptServer), "promptProvider")
	_, err = generatedRoot.Stat("mcp_static_prompts/prompt_provider.go")
	require.ErrorIs(t, err, os.ErrNotExist)
	selectorService, err := generatedRoot.ReadFile("selector/service.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorService), "ReadValue(context.Context) (res string, err error)")
	require.Contains(t, string(selectorService), "ReadValueEndpoint(context.Context) (res string, err error)")
	selectorServer, err := generatedRoot.ReadFile("mcp_selector/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorServer), "a.service.ReadValue(ctx)")
	require.Contains(t, string(selectorServer), "a.service.ReadValueEndpoint(ctx)")
	selectorJSONRPCStream, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/stream.go")
	require.Error(t, err)
	require.Empty(t, selectorJSONRPCStream)
	selectorCaller, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/caller.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorCaller), `mcpselector "generated.local/gen/mcp_selector"`)
	require.Contains(t, string(selectorCaller), "if err := InitializeSession(ctx, client, clientInfo); err != nil {")
	selectorSession, err := generatedRoot.ReadFile("jsonrpc/mcp_selector/client/session.go")
	require.NoError(t, err)
	require.Contains(t, string(selectorSession), "func InitializeSession(")
	resourceServer, err := generatedRoot.ReadFile("mcp_resources/adapter_server.go")
	require.NoError(t, err)
	require.Contains(t, string(resourceServer), `case "doc://list":`)
	require.Contains(t, string(resourceServer), "result, err := a.service.ReadDocument(ctx)")
	require.NotContains(t, string(resourceServer), "ParseQuery")
	resourceCodec, err := generatedRoot.ReadFile("mcp_resources/internal/codec/codec.go")
	require.NoError(t, err)
	require.Contains(t, string(resourceCodec), "func EncodeReadDocumentResult")
	require.NotContains(t, string(resourceCodec), "ReadDocumentPayloadTransport")
	callerTest, err := generatedRoot.OpenFile(
		"jsonrpc/mcp_fmt/client/caller_lifecycle_test.go",
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0o600,
	)
	require.NoError(t, err)
	_, err = callerTest.WriteString(callerLifecycleGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, callerTest.Close())
	registerTest, err := generatedRoot.OpenFile(
		"mcp_fmt/register_string_test.go",
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0o600,
	)
	require.NoError(t, err)
	_, err = registerTest.WriteString(registerStringGeneratedTestSource)
	require.NoError(t, err)
	require.NoError(t, registerTest.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/...")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// testLocatedRenderPayload returns a service payload moved to a generated
// package whose preferred name collides with the private MCP codec package.
func testLocatedRenderPayload() *expr.UserTypeExpr {
	return &expr.UserTypeExpr{
		TypeName: "RenderPayload",
		UID:      "mcp-integration-render-payload",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"mcpcodec"}},
		},
	}
}

// setNamedMCPMethodTypes gives one generated codec authored service types at
// both ends, with another authored type nested inside each one.
func setNamedMCPMethodTypes(add, subtract *expr.MethodExpr) []expr.UserType {
	operand := &expr.UserTypeExpr{
		TypeName: "Operand",
		UID:      "mcp-integration-operand",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"alpha/shared"}},
		},
	}
	request := &expr.UserTypeExpr{
		TypeName: "CalculationRequest",
		UID:      "mcp-integration-request",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "operand", Attribute: &expr.AttributeExpr{Type: operand}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"operand"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"alpha/shared"}},
		},
	}
	subtrahend := &expr.UserTypeExpr{
		TypeName: "Subtrahend",
		UID:      "mcp-integration-subtrahend",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	subtractRequest := &expr.UserTypeExpr{
		TypeName: "SubtractRequest",
		UID:      "mcp-integration-subtract-request",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "subtrahend", Attribute: &expr.AttributeExpr{Type: subtrahend}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"subtrahend"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	subtractResponse := &expr.UserTypeExpr{
		TypeName: "SubtractResponse",
		UID:      "mcp-integration-subtract-response",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "difference", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"difference"}},
			Meta:       expr.MetaExpr{"struct:pkg:path": {"zeta/shared"}},
		},
	}
	calculation := &expr.UserTypeExpr{
		TypeName: "Calculation",
		UID:      "mcp-integration-calculation",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.Int}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"value"}},
		},
	}
	response := &expr.UserTypeExpr{
		TypeName: "CalculationResponse",
		UID:      "mcp-integration-response",
		AttributeExpr: &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "calculation", Attribute: &expr.AttributeExpr{Type: calculation}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"calculation"}},
		},
	}
	add.Payload = &expr.AttributeExpr{Type: request}
	add.Result = &expr.AttributeExpr{Type: response}
	subtract.Payload = &expr.AttributeExpr{Type: subtractRequest}
	subtract.Result = &expr.AttributeExpr{Type: subtractResponse}
	return []expr.UserType{operand, request, subtrahend, subtractRequest, subtractResponse, calculation, response}
}

// testModuleDirectory returns the local module selected by the workspace that
// compiled this test.
func testModuleDirectory(t *testing.T, module string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"list", "-m", "-f", "{{.Dir}}", module}
	// #nosec G204 -- the module name comes from this test file.
	command := exec.CommandContext(ctx, "go", args...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}
