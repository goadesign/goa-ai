// This file checks that every MCP client returns the same tool result and error
// shape to the agent runtime.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	stdioHelperEnv                   = "GOA_MCP_STDIO_HELPER"
	stdioHelperRecordEnv             = "GOA_MCP_STDIO_RECORD"
	stdioHelperInitializeResultEnv   = "GOA_MCP_STDIO_INITIALIZE_RESULT"
	stdioHelperProtocolRecordEnv     = "GOA_MCP_STDIO_PROTOCOL_RECORD"
	stdioHelperServerMessagesEnv     = "GOA_MCP_STDIO_SERVER_MESSAGES"
	stdioHelperServerResponseFileEnv = "GOA_MCP_STDIO_SERVER_RESPONSE_FILE"
	stdioHelperResponseEnvelopeEnv   = "GOA_MCP_STDIO_RESPONSE_ENVELOPE"
	rpcMethodToolsCall               = "tools/call"
	validInitializeResultJSON        = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"test-server","version":"1.0.0"},"capabilities":{"tools":{}}}`
)

func init() { otel.SetTextMapPropagator(propagation.TraceContext{}) }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func httpResponse(status int, headers http.Header, body []byte) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func TestHTTPCallerCallTool(t *testing.T) {
	t.Parallel()
	var traceHeader string
	var metaTrace string
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			if err := r.Body.Close(); err != nil {
				return nil, err
			}
			var req rpcRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			switch req.Method {
			case rpcMethodInitialize:
				resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(validInitializeResultJSON)}
				data, err := json.Marshal(resp)
				if err != nil {
					return nil, err
				}
				headers := http.Header{
					"Content-Type": []string{"application/json"},
				}
				return httpResponse(http.StatusOK, headers, data), nil
			case rpcMethodInitialized:
				return httpResponse(http.StatusAccepted, nil, nil), nil
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
				data, err := json.Marshal(resp)
				if err != nil {
					return nil, err
				}
				headers := http.Header{
					"Content-Type": []string{"application/json"},
				}
				return httpResponse(http.StatusOK, headers, data), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, []byte("unknown method")), nil
			}
		}),
	}

	ctx, expectedTrace := contextWithTrace()
	caller, err := NewHTTPCaller(ctx, HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "test-agent", Version: "1.0.0"},
	})
	require.NoError(t, err)
	req := CallRequest{Tool: "search", Payload: json.RawMessage(`{"query":"hi"}`)}
	resp, err := caller.CallTool(ctx, req)
	require.NoError(t, err)
	require.Equal(t, textContent(`{"ok":true}`), resp.Content)
	require.Equal(t, expectedTrace, traceHeader)
	require.Equal(t, expectedTrace, metaTrace)
}

func TestStdioCallerCallTool(t *testing.T) {
	t.Parallel()
	recordPath := t.TempDir() + "/methods"
	ctx, expectedTrace := contextWithTrace()
	caller, err := NewStdioCaller(ctx, StdioOptions{
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestStdioHelper", "--"},
		Env:         []string{stdioHelperEnv + "=1", stdioHelperRecordEnv + "=" + recordPath},
		ClientInfo:  ClientInfo{Name: "test-agent", Version: "1.0.0"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	defer func() {
		if err := caller.Close(); err != nil {
			t.Logf("caller close error: %v", err)
		}
	}()
	resp, err := caller.CallTool(ctx, CallRequest{Tool: "meta", Payload: json.RawMessage(`"noop"`)})
	require.NoError(t, err)
	require.Equal(t, textContent(expectedTrace), resp.Content)
	require.NoError(t, caller.Close())
	// #nosec G304 -- recordPath is created inside this test's temporary directory.
	methods, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	require.Equal(t, "initialize\nnotifications/initialized\ntools/call\n", string(methods))
}

func TestStdioCallerRequiresClientInfoBeforeStartingCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clientInfo ClientInfo
		wantError  string
	}{
		{name: "missing name", clientInfo: ClientInfo{Version: "2.4.1"}, wantError: "mcp: client name is required"},
		{name: "missing version", clientInfo: ClientInfo{Name: "operations-agent"}, wantError: "mcp: client version is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewStdioCaller(context.Background(), StdioOptions{
				Command:    "command-that-must-not-run",
				ClientInfo: test.clientInfo,
			})
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestStdioCallerSendsClientInfo(t *testing.T) {
	t.Parallel()

	caller, err := NewStdioCaller(context.Background(), StdioOptions{
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestStdioHelper", "--"},
		Env:         []string{stdioHelperEnv + "=1"},
		ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, caller.Close())
	}()

	resp, err := caller.CallTool(context.Background(), CallRequest{Tool: "identity", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Equal(t, textContent(`"operations-agent@2.4.1"`), resp.Content)
}

func TestStdioCallerUsesDefaultProtocolVersion(t *testing.T) {
	t.Parallel()

	protocolPath := t.TempDir() + "/protocol-version"
	caller, err := NewStdioCaller(context.Background(), StdioOptions{
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestStdioHelper", "--"},
		Env:         []string{stdioHelperEnv + "=1", stdioHelperProtocolRecordEnv + "=" + protocolPath},
		ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, caller.Close())

	// #nosec G304 -- protocolPath is created inside this test's temporary directory.
	protocol, err := os.ReadFile(protocolPath)
	require.NoError(t, err)
	require.Equal(t, "2025-06-18", string(protocol))
}

func TestStdioCallerRejectsIncompleteInitializeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    string
		wantError string
	}{
		{name: "missing protocol version", result: `{"serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response protocolVersion is required"},
		{name: "missing server info", result: `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo is required"},
		{name: "null server info", result: `{"protocolVersion":"2025-06-18","serverInfo":null,"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo is required"},
		{name: "missing server name", result: `{"protocolVersion":"2025-06-18","serverInfo":{"version":"1.0.0"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo.name is required"},
		{name: "missing server version", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo.version is required"},
		{name: "missing capabilities", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"}}`, wantError: "mcp: initialize response capabilities are required"},
		{name: "null capabilities", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":null}`, wantError: "mcp: initialize response capabilities are required"},
		{name: "missing tools capability", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{}}`, wantError: "mcp: initialize response tools capability is required"},
		{name: "null tools capability", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{"tools":null}}`, wantError: "mcp: initialize response tools capability is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewStdioCaller(context.Background(), StdioOptions{
				Command:     os.Args[0],
				Args:        []string{"-test.run=TestStdioHelper", "--"},
				Env:         []string{stdioHelperEnv + "=1", stdioHelperInitializeResultEnv + "=" + test.result},
				ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
				InitTimeout: time.Second,
			})
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestStdioCallerRejectsMalformedJSONRPCResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "missing version",
			response: `{"id":1,"result":{}}`,
		},
		{
			name:     "wrong version",
			response: `{"jsonrpc":"1.0","id":1,"result":{}}`,
		},
		{
			name:     "result and error",
			response: `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32603,"message":"broken"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewStdioCaller(context.Background(), StdioOptions{
				Command:     os.Args[0],
				Args:        []string{"-test.run=TestStdioHelper", "--"},
				Env:         []string{stdioHelperEnv + "=1", stdioHelperResponseEnvelopeEnv + "=" + test.response},
				ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
				InitTimeout: time.Second,
			})
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
			require.EqualError(t, malformed, "malformed MCP response: invalid JSON-RPC response")
		})
	}
}

func TestStdioCallerHandlesServerMessagesWhileToolCallIsPending(t *testing.T) {
	t.Parallel()

	responsePath := t.TempDir() + "/server-responses"
	caller, err := NewStdioCaller(context.Background(), StdioOptions{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStdioHelper", "--"},
		Env: []string{
			stdioHelperEnv + "=1",
			stdioHelperServerMessagesEnv + "=1",
			stdioHelperServerResponseFileEnv + "=" + responsePath,
		},
		ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, caller.Close())
	}()

	response, err := caller.CallTool(context.Background(), CallRequest{Tool: "identity", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Equal(t, textContent(`"operations-agent@2.4.1"`), response.Content)

	// #nosec G304 -- responsePath is created inside this test's temporary directory.
	data, err := os.ReadFile(responsePath)
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	require.Len(t, lines, 2)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":"server-ping","result":{}}`, string(lines[0]))
	require.JSONEq(t, `{"jsonrpc":"2.0","id":73,"error":{"code":-32601,"message":"method not found"}}`, string(lines[1]))
}

func TestStdioCallerSurvivesConstructorContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	caller, err := NewStdioCaller(ctx, StdioOptions{
		Command:     os.Args[0],
		Args:        []string{"-test.run=TestStdioHelper", "--"},
		Env:         []string{stdioHelperEnv + "=1"},
		ClientInfo:  ClientInfo{Name: "operations-agent", Version: "2.4.1"},
		InitTimeout: time.Second,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, caller.Close())
	}()

	cancel()
	runtime.Gosched()
	response, err := caller.CallTool(context.Background(), CallRequest{Tool: "identity", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Equal(t, textContent(`"operations-agent@2.4.1"`), response.Content)
}

func TestRPCMessageRejectsMalformedMethodMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message rpcMessage
	}{
		{
			name:    "missing version",
			message: rpcMessage{Method: "notifications/progress"},
		},
		{
			name:    "wrong version",
			message: rpcMessage{JSONRPC: "1.0", Method: "notifications/progress"},
		},
		{
			name: "method and result",
			message: rpcMessage{
				JSONRPC: "2.0",
				Method:  "ping",
				ID:      json.RawMessage(`"server-ping"`),
				Result:  json.RawMessage(`{}`),
			},
		},
		{
			name: "method and error",
			message: rpcMessage{
				JSONRPC: "2.0",
				Method:  "ping",
				ID:      json.RawMessage(`"server-ping"`),
				Error:   json.RawMessage(`{"code":-32603,"message":"broken"}`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := test.message.numericResponse()
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
			require.EqualError(t, malformed, "malformed MCP response: invalid JSON-RPC message")
		})
	}
}

func TestRPCMessageRejectsNullResponseMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "method with null error",
			raw:  `{"jsonrpc":"2.0","id":"server-ping","method":"ping","error":null}`,
		},
		{
			name: "response with null identifier",
			raw:  `{"jsonrpc":"2.0","id":null,"result":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var message rpcMessage
			require.NoError(t, json.Unmarshal([]byte(test.raw), &message))
			_, _, err := message.numericResponse()
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
		})
	}
}

func TestRPCMessageRequiresTypedErrorFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		errorJSON string
		wantError string
	}{
		{
			name:      "missing code",
			errorJSON: `{"message":"broken"}`,
			wantError: "malformed MCP response: JSON-RPC response error code is required",
		},
		{
			name:      "null code",
			errorJSON: `{"code":null,"message":"broken"}`,
			wantError: "malformed MCP response: JSON-RPC response error code must be an integer",
		},
		{
			name:      "string code",
			errorJSON: `{"code":"-32603","message":"broken"}`,
			wantError: "malformed MCP response: JSON-RPC response error code must be an integer",
		},
		{
			name:      "fractional code",
			errorJSON: `{"code":-32603.5,"message":"broken"}`,
			wantError: "malformed MCP response: JSON-RPC response error code must be an integer",
		},
		{
			name:      "missing message",
			errorJSON: `{"code":-32603}`,
			wantError: "malformed MCP response: JSON-RPC response error message is required",
		},
		{
			name:      "null message",
			errorJSON: `{"code":-32603,"message":null}`,
			wantError: "malformed MCP response: JSON-RPC response error message must be a string",
		},
		{
			name:      "numeric message",
			errorJSON: `{"code":-32603,"message":17}`,
			wantError: "malformed MCP response: JSON-RPC response error message must be a string",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var message rpcMessage
			require.NoError(t, json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"error":`+test.errorJSON+`}`), &message))
			_, _, err := message.numericResponse()
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestRPCMessageAcceptsExplicitZeroErrorFields(t *testing.T) {
	t.Parallel()

	var message rpcMessage
	require.NoError(t, json.Unmarshal(
		[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":0,"message":""}}`),
		&message,
	))
	response, ok, err := message.numericResponse()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, &rpcError{Code: 0, Message: ""}, response.Error)
}

func TestNormalizeToolResultReturnsTypedExecutionError(t *testing.T) {
	t.Parallel()

	message := "device alias does not exist"
	detail := "choose an alias returned by devices/list"
	content := []contentItem{
		{Type: "text", Text: &message},
		{Type: "text", Text: &detail},
	}
	_, err := normalizeToolResult(toolsCallResult{
		Content:           &content,
		StructuredContent: json.RawMessage(`{"code":"unknown_alias"}`),
		IsError:           true,
	})

	var executionErr *ToolExecutionError
	require.ErrorAs(t, err, &executionErr)
	assert.Equal(t, "MCP tool execution error: device alias does not exist\nchoose an alias returned by devices/list", executionErr.Error())
	assert.Equal(t, textContent(message, detail), executionErr.Response.Content)
	assert.JSONEq(t, `{"code":"unknown_alias"}`, string(executionErr.Response.StructuredContent))
}

func TestNormalizeToolResultPreservesTextAndStructuredContent(t *testing.T) {
	t.Parallel()

	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{
		"content":[
			{"type":"text","text":"plain result"},
			{"type":"text","text":"{\"temperature\":22.5}"}
		],
		"structuredContent":{"temperature":22.5,"unit":"celsius"}
	}`), &result))

	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.Equal(t, textContent("plain result", `{"temperature":22.5}`), response.Content)
	require.True(t, bytes.Equal(
		json.RawMessage(`{"temperature":22.5,"unit":"celsius"}`),
		response.StructuredContent,
	))
}

func TestNormalizeToolResultPreservesEveryContentType(t *testing.T) {
	t.Parallel()

	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{
		"content":[
			{"type":"text","text":"hello","annotations":{"audience":["user"],"priority":0.8},"_meta":{"source":"test"}},
			{"type":"image","data":"aW1hZ2U=","mimeType":"image/png"},
			{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"},
			{"type":"resource_link","name":"guide","title":"Guide","uri":"doc://guide","description":"User guide","mimeType":"text/markdown","size":42},
			{"type":"resource","resource":{"uri":"doc://inline","mimeType":"text/plain","text":"inline"}},
			{"type":"resource","resource":{"uri":"blob://inline","mimeType":"application/octet-stream","blob":"YmxvYg=="}}
		]
	}`), &result))

	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.Len(t, response.Content, 6)
	assert.Equal(t, "hello", response.Content[0].(*TextContent).Text)
	assert.Equal(t, RoleUser, response.Content[0].(*TextContent).Annotations.Audience[0])
	assert.Equal(t, "aW1hZ2U=", response.Content[1].(*ImageContent).Data)
	assert.Equal(t, "YXVkaW8=", response.Content[2].(*AudioContent).Data)
	assert.Equal(t, "doc://guide", response.Content[3].(*ResourceLink).URI)
	assert.Equal(t, "inline", response.Content[4].(*EmbeddedResource).Resource.(*TextResourceContents).Text)
	assert.Equal(t, "YmxvYg==", response.Content[5].(*EmbeddedResource).Resource.(*BlobResourceContents).Blob)
}

func TestNormalizeToolResultAcceptsEmptyContent(t *testing.T) {
	t.Parallel()

	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{
		"content":[],
		"structuredContent":{"temperature":22.5,"unit":"celsius"}
	}`), &result))

	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.Empty(t, response.Content)
	require.JSONEq(t, `{"temperature":22.5,"unit":"celsius"}`, string(response.StructuredContent))
}

func TestNormalizeToolResultRejectsMissingContent(t *testing.T) {
	t.Parallel()

	_, err := normalizeToolResult(toolsCallResult{})
	require.EqualError(t, err, "malformed MCP response: tool response is missing content")
}

func TestNormalizeToolResultAcceptsEmptyExecutionError(t *testing.T) {
	t.Parallel()

	content := []contentItem{}
	_, err := normalizeToolResult(toolsCallResult{Content: &content, IsError: true})
	require.EqualError(t, err, "MCP tool execution error")
}

func TestNormalizeToolResultDoesNotInferStructuredContentFromText(t *testing.T) {
	t.Parallel()

	text := `{"temperature":22.5}`
	content := []contentItem{{Type: "text", Text: &text}}
	response, err := normalizeToolResult(toolsCallResult{
		Content: &content,
	})
	require.NoError(t, err)
	require.Equal(t, textContent(`{"temperature":22.5}`), response.Content)
	require.Empty(t, response.StructuredContent)
}

func TestNormalizeToolResultRejectsNonObjectStructuredContent(t *testing.T) {
	t.Parallel()

	for _, structured := range []string{`["not","an","object"]`, `"text"`, `42`, `true`, `null`} {
		t.Run(structured, func(t *testing.T) {
			t.Parallel()

			var result toolsCallResult
			require.NoError(t, json.Unmarshal([]byte(`{
				"content":[{"type":"text","text":"plain result"}],
				"structuredContent":`+structured+`
			}`), &result))

			_, err := normalizeToolResult(result)
			require.EqualError(t, err, "malformed MCP response: structuredContent must be a JSON object")
		})
	}
}

func TestNormalizeToolResultRejectsIncompleteImageContent(t *testing.T) {
	t.Parallel()

	text := "supported"
	content := []contentItem{
		{Type: "text", Text: &text},
		{Type: "image"},
	}
	_, err := normalizeToolResult(toolsCallResult{
		Content: &content,
	})
	require.EqualError(t, err, "malformed MCP response: content[1]: image content requires data and mimeType")
}

// textContent builds the typed text blocks expected from a tool response.
func textContent(values ...string) []ContentBlock {
	content := make([]ContentBlock, len(values))
	for i, value := range values {
		content[i] = &TextContent{Text: value}
	}
	return content
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

// runStdioHelper acts as an MCP server that verifies the caller sends one JSON
// message per line and waits for initialization before accepting tool calls.
func runStdioHelper() {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	var clientIdentity string
	var ready bool
	for {
		message, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		var req rpcRequest
		if err := json.Unmarshal(message, &req); err != nil {
			continue
		}
		recordStdioMethod(req.Method)
		switch req.Method {
		case rpcMethodInitialize:
			params := req.Params.(map[string]any)
			recordStdioProtocolVersion(params["protocolVersion"].(string))
			capabilities, ok := params["capabilities"].(map[string]any)
			if !ok || len(capabilities) != 0 {
				writeMessageLine(writer, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "empty capabilities are required"}})
				continue
			}
			clientInfo := params["clientInfo"].(map[string]any)
			clientIdentity = clientInfo["name"].(string) + "@" + clientInfo["version"].(string)
			if response := os.Getenv(stdioHelperResponseEnvelopeEnv); response != "" {
				writeRawMessageLine(writer, response)
				continue
			}
			initializeResult := os.Getenv(stdioHelperInitializeResultEnv)
			if initializeResult == "" {
				initializeResult = validInitializeResultJSON
			}
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(initializeResult)}
			writeMessageLine(writer, resp)
		case rpcMethodInitialized:
			ready = true
		case rpcMethodToolsCall:
			if !ready {
				writeMessageLine(writer, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "not initialized"}})
				continue
			}
			params := req.Params.(map[string]any)
			if os.Getenv(stdioHelperServerMessagesEnv) == "1" {
				writeMessageLine(writer, map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/progress",
					"params":  map[string]any{},
				})
				writeMessageLine(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      "server-ping",
					"method":  "ping",
					"params":  map[string]any{},
				})
				writeMessageLine(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      73,
					"method":  "sampling/createMessage",
					"params":  map[string]any{},
				})
				for range 2 {
					message, err := reader.ReadBytes('\n')
					if err != nil {
						return
					}
					recordStdioServerResponse(message)
				}
			}
			if params["name"] == "identity" {
				identityText := strconv.Quote(clientIdentity)
				content := []contentItem{{Type: "text", Text: &identityText}}
				result := toolsCallResult{Content: &content}
				data, err := json.Marshal(result)
				if err != nil {
					panic(err)
				}
				writeMessageLine(writer, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: data})
				continue
			}
			traceVal := ""
			if meta, ok := params["_meta"].(map[string]any); ok {
				if tp, ok := meta["traceparent"].(string); ok {
					traceVal = tp
				}
			}
			if traceVal == "" {
				errResp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "missing traceparent"}}
				writeMessageLine(writer, errResp)
				continue
			}
			content := []contentItem{{Type: "text", Text: &traceVal}}
			result := toolsCallResult{Content: &content}
			data, err := json.Marshal(result)
			if err != nil {
				panic(err)
			}
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: data}
			writeMessageLine(writer, resp)
		default:
			errResp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "unknown method"}}
			writeMessageLine(writer, errResp)
		}
	}
	if err := writer.Flush(); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func recordStdioMethod(method string) {
	path := os.Getenv(stdioHelperRecordEnv)
	if path == "" {
		return
	}
	// #nosec G304,G703 -- the parent test supplies a path inside its temporary directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(file, method); err != nil {
		panic(err)
	}
	if err := file.Close(); err != nil {
		panic(err)
	}
}

// recordStdioProtocolVersion stores the version sent during initialization so
// the parent test can check the public caller always uses its supported version.
func recordStdioProtocolVersion(protocol string) {
	path := os.Getenv(stdioHelperProtocolRecordEnv)
	if path == "" {
		return
	}
	// #nosec G703 -- the parent test supplies a path inside its temporary directory.
	if err := os.WriteFile(path, []byte(protocol), 0o600); err != nil {
		panic(err)
	}
}

// recordStdioServerResponse stores a client response so the parent test can
// check how the client handled a server request while a tool call was pending.
func recordStdioServerResponse(message []byte) {
	path := os.Getenv(stdioHelperServerResponseFileEnv)
	if path == "" {
		return
	}
	// #nosec G304,G703 -- the parent test supplies a path inside its temporary directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	if _, err := file.Write(message); err != nil {
		panic(err)
	}
	if err := file.Close(); err != nil {
		panic(err)
	}
}

// writeMessageLine writes one compact response followed by the newline that
// lets the caller read exactly one response.
func writeMessageLine(writer *bufio.Writer, message any) {
	data, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	if _, err := writer.Write(data); err != nil {
		panic(err)
	}
	if err := writer.WriteByte('\n'); err != nil {
		panic(err)
	}
	if err := writer.Flush(); err != nil {
		panic(err)
	}
}

// writeRawMessageLine sends a test response exactly as the parent supplied it.
func writeRawMessageLine(writer *bufio.Writer, message string) {
	if _, err := writer.WriteString(message + "\n"); err != nil {
		panic(err)
	}
	if err := writer.Flush(); err != nil {
		panic(err)
	}
}
