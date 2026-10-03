// These tests check the current MCP result and JSON-RPC error boundaries.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

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
	_, err := normalizeToolResult(toolsCallResult{ResultType: "complete",
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
		"resultType":"complete","content":[
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
		"resultType":"complete","content":[
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
		"resultType":"complete","content":[],
		"structuredContent":{"temperature":22.5,"unit":"celsius"}
	}`), &result))

	response, err := normalizeToolResult(result)
	require.NoError(t, err)
	require.Empty(t, response.Content)
	require.JSONEq(t, `{"temperature":22.5,"unit":"celsius"}`, string(response.StructuredContent))
}

func TestNormalizeToolResultRejectsMissingContent(t *testing.T) {
	t.Parallel()

	_, err := normalizeToolResult(toolsCallResult{ResultType: "complete"})
	require.EqualError(t, err, "malformed MCP response: tool response is missing content")
}

func TestNormalizeToolResultAcceptsEmptyExecutionError(t *testing.T) {
	t.Parallel()

	content := []contentItem{}
	_, err := normalizeToolResult(toolsCallResult{ResultType: "complete", Content: &content, IsError: true})
	require.EqualError(t, err, "MCP tool execution error")
}

func TestNormalizeToolResultDoesNotInferStructuredContentFromText(t *testing.T) {
	t.Parallel()

	text := `{"temperature":22.5}`
	content := []contentItem{{Type: "text", Text: &text}}
	response, err := normalizeToolResult(toolsCallResult{ResultType: "complete",
		Content: &content,
	})
	require.NoError(t, err)
	require.Equal(t, textContent(`{"temperature":22.5}`), response.Content)
	require.Empty(t, response.StructuredContent)
}

func TestNormalizeToolResultRejectsIncompleteImageContent(t *testing.T) {
	t.Parallel()

	text := "supported"
	content := []contentItem{
		{Type: "text", Text: &text},
		{Type: "image"},
	}
	_, err := normalizeToolResult(toolsCallResult{ResultType: "complete",
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

// A null structured result is valid, but null control values must not become
// successful defaults when the external response enters the caller.
func TestToolResultRejectsNullControls(t *testing.T) {
	for _, field := range []string{"content", "inputRequests", "requestState", "isError"} {
		t.Run(field, func(t *testing.T) {
			var result toolsCallResult
			err := json.Unmarshal([]byte(`{"resultType":"complete","`+field+`":null}`), &result)
			assert.ErrorContains(t, err, "cannot be null")
		})
	}
}
