package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const sdkStdioHelperEnv = "GOA_MCP_SDK_STDIO_HELPER"

func init() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

func TestCallToolAcrossProtocols(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		newCaller func(t *testing.T, ctx context.Context) *SessionCaller
	}{
		{
			name: "stdio",
			newCaller: func(t *testing.T, ctx context.Context) *SessionCaller {
				t.Helper()

				caller, err := NewStdioCaller(ctx, StdioOptions{
					Command:     os.Args[0],
					Args:        []string{"-test.run=TestSDKStdioServerProcess", "--"},
					Env:         append(os.Environ(), sdkStdioHelperEnv+"=1"),
					InitTimeout: 5 * time.Second,
				})
				require.NoError(t, err)
				return caller
			},
		},
		{
			name: "streamable_http",
			newCaller: func(t *testing.T, ctx context.Context) *SessionCaller {
				t.Helper()

				server := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
					return newSDKTestServer()
				}, &sdkmcp.StreamableHTTPOptions{
					DisableLocalhostProtection: true,
				}))
				t.Cleanup(server.Close)

				caller, err := NewHTTPCaller(ctx, HTTPOptions{
					Endpoint:    server.URL,
					InitTimeout: 5 * time.Second,
				})
				require.NoError(t, err)
				return caller
			},
		},
		{
			name: "sse",
			newCaller: func(t *testing.T, ctx context.Context) *SessionCaller {
				t.Helper()

				server := httptest.NewServer(sdkmcp.NewSSEHandler(func(*http.Request) *sdkmcp.Server {
					return newSDKTestServer()
				}, nil))
				t.Cleanup(server.Close)

				caller, err := NewSSECaller(ctx, HTTPOptions{
					Endpoint:    server.URL,
					InitTimeout: 5 * time.Second,
				})
				require.NoError(t, err)
				return caller
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, expectedTrace := contextWithTrace()

			multiCaller := tt.newCaller(t, ctx)
			assertMultiContentResponse(t, multiCaller, ctx)
			require.NoError(t, multiCaller.Close())

			traceCaller := tt.newCaller(t, ctx)
			assertTraceMetadataForwarding(t, traceCaller, ctx, expectedTrace)
			require.NoError(t, traceCaller.Close())
		})
	}
}

func TestNormalizeSDKToolResultConcatenatesTextAcrossAllContentItems(t *testing.T) {
	t.Parallel()

	first := &sdkmcp.TextContent{Text: "{\"result\":\"hello "}
	second := &sdkmcp.TextContent{Text: "world\"}"}
	resp, err := normalizeSDKToolResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{first, second},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"result":"hello world"}`, string(resp.Result))

	var structured []map[string]any
	require.NoError(t, json.Unmarshal(resp.Structured, &structured))
	require.Len(t, structured, 2)
	assert.Equal(t, "text", structured[0]["type"])
	assert.Equal(t, "text", structured[1]["type"])
}

func TestNormalizeSDKToolResultFallsBackToStructuredItemWhenNoTextExists(t *testing.T) {
	t.Parallel()

	resp, err := normalizeSDKToolResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.ImageContent{
				Data:     []byte("fake-image"),
				MIMEType: "image/png",
			},
		},
	})
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "image", result["type"])
	assert.Equal(t, "image/png", result["mimeType"])
	assert.Equal(t, "ZmFrZS1pbWFnZQ==", result["data"])

	var structured []map[string]any
	require.NoError(t, json.Unmarshal(resp.Structured, &structured))
	require.Len(t, structured, 1)
	assert.Equal(t, "image", structured[0]["type"])
}

func TestSDKStdioServerProcess(t *testing.T) {
	if os.Getenv(sdkStdioHelperEnv) != "1" {
		t.Skip("helper subprocess")
	}

	runSDKStdioServerProcess()
}

func assertMultiContentResponse(t *testing.T, caller *SessionCaller, ctx context.Context) {
	t.Helper()

	resp, err := caller.CallTool(ctx, CallRequest{
		Tool:    "multi_content_tool",
		Payload: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	var result string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "hello world!", result)

	var structured []map[string]any
	require.NoError(t, json.Unmarshal(resp.Structured, &structured))
	require.Len(t, structured, 3)

	assert.Equal(t, "text", structured[0]["type"])
	assert.Equal(t, "hello ", structured[0]["text"])
	assert.Equal(t, "text", structured[1]["type"])
	assert.Equal(t, "world", structured[1]["text"])
	assert.Equal(t, "text", structured[2]["type"])
	assert.Equal(t, "!", structured[2]["text"])
}

func assertTraceMetadataForwarding(t *testing.T, caller *SessionCaller, ctx context.Context, expectedTrace string) {
	t.Helper()

	resp, err := caller.CallTool(ctx, CallRequest{
		Tool:    "trace_meta_tool",
		Payload: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	var traceparent string
	require.NoError(t, json.Unmarshal(resp.Result, &traceparent))
	assert.Equal(t, expectedTrace, traceparent)
}

func contextWithTrace() (context.Context, string) {
	traceID := trace.TraceID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00}
	spanID := trace.SpanID{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)
	expected := fmt.Sprintf("00-%s-%s-01", traceID.String(), spanID.String())
	return ctx, expected
}

func newSDKTestServer() *sdkmcp.Server {
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:    "test-server",
			Version: "1.0.0",
		},
		nil,
	)

	server.AddTool(&sdkmcp.Tool{
		Name:        "multi_content_tool",
		Description: "Returns multiple text items",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "hello "},
				&sdkmcp.TextContent{Text: "world"},
				&sdkmcp.TextContent{Text: "!"},
			},
		}, nil
	})

	server.AddTool(&sdkmcp.Tool{
		Name:        "trace_meta_tool",
		Description: "Returns the incoming trace metadata",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		traceparent, _ := req.Params.Meta["traceparent"].(string)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: traceparent},
			},
		}, nil
	})

	return server
}

func runSDKStdioServerProcess() {
	server := newSDKTestServer()
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
