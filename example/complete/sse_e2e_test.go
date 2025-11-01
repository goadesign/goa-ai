package assistantapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	orcclient "example.com/assistant/gen/jsonrpc/orchestrator/client"
	orcserver "example.com/assistant/gen/jsonrpc/orchestrator/server"
	orchestrator "example.com/assistant/gen/orchestrator"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// TestOrchestratorRunSSE boots the JSON-RPC SSE server and verifies that a
// run produces a tool_call and tool_result followed by a final message.
func TestOrchestratorRunSSE(t *testing.T) {
	ctx := context.Background()

	// Service and endpoints
	svc := NewOrchestrator()
	eps := orchestrator.NewEndpoints(svc)

	// Transport mux and server
	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	svr := orcserver.New(eps, mux, dec, enc, func(context.Context, http.ResponseWriter, error) {})
	orcserver.Mount(mux, svr)

	// Start test HTTP server
	ts := httptest.NewServer(mux)
	defer ts.Close()

	u, _ := url.Parse(ts.URL)

	// JSON-RPC SSE client
	cl := orcclient.NewClient(u.Scheme, u.Host, ts.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	runEP := cl.Run()

	// Minimal payload with a user query that triggers MCP search
	payload := &orchestrator.AgentRunPayload{
		SessionID: ptr("sess-e2e"),
		Messages:  []*orchestrator.AgentMessage{{Role: "user", Content: "status update"}},
	}

	// Start stream
	raw, err := runEP(ctx, payload)
	require.NoError(t, err)

	stream, ok := raw.(*orcclient.RunClientStream)
	require.True(t, ok, "unexpected stream type %T", raw)
	defer stream.Close()

	var (
		sawToolCall    bool
		sawToolResult  bool
		sawMessage     bool
		sawPlannerNote bool
	)

	// Drain stream until EOF
	for {
		chunk, err := stream.Recv(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "EOF") || err == io.EOF {
				break
			}
			require.NoError(t, err)
		}
		switch chunk.Type {
		case "tool_call":
			sawToolCall = true
		case "tool_result":
			sawToolResult = true
		case "message":
			sawMessage = true
			if chunk.Message != nil && strings.HasPrefix(*chunk.Message, "[planner]") {
				sawPlannerNote = true
			}
		}
	}

	require.True(t, sawToolCall, "expected tool_call chunk")
	require.True(t, sawToolResult, "expected tool_result chunk")
	require.True(t, sawMessage, "expected final message chunk")
	require.True(t, sawPlannerNote, "expected at least one planner note message")
}

func ptr[T any](v T) *T { return &v }
