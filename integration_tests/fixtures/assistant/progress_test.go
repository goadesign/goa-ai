// These tests call regenerated clients and servers. They prove progress delivery
// before completion and preserve the typed client contract across stream retries.
package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	genassistant "example.com/assistant/gen/assistant"
	genclient "example.com/assistant/gen/jsonrpc/mcp_assistant/client"
	genserver "example.com/assistant/gen/jsonrpc/mcp_assistant/server"
	genmcp "example.com/assistant/gen/mcp_assistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
)

type (
	progressTestService struct {
		genassistant.Service
		release <-chan struct{}
	}
)

func (s *progressTestService) ReportWork(ctx context.Context) (string, error) {
	for _, value := range []float64{0, 50, 100} {
		if err := mcpruntime.ReportProgress(ctx, value, new(float64(100)), nil); err != nil {
			return "", err
		}
	}
	select {
	case <-s.release:
		return "finished", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestGeneratedHTTPProgressBeforeResult(t *testing.T) {
	release := make(chan struct{})
	service := &progressTestService{Service: NewAssistant(), release: release}
	mux := goahttp.NewMuxer()
	server := genserver.New(genmcp.NewEndpoints(genmcp.NewMCPAdapter(service, nil)), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	endpoint := httptest.NewServer(mux)
	defer endpoint.Close()
	location, err := url.Parse(endpoint.URL)
	require.NoError(t, err)
	client := genclient.NewClient(location.Scheme, location.Host, endpoint.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	updates := make(chan mcpruntime.Progress, 3)
	deadline, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx := mcpruntime.WithProgress(deadline, func(_ context.Context, p mcpruntime.Progress) error { updates <- p; return nil })
	type completed struct {
		result any
		err    error
	}
	final := make(chan completed, 1)
	go func() {
		result, err := client.ToolsCall()(ctx, &genmcp.ToolsCallPayload{Name: "test_tool_with_progress", Arguments: json.RawMessage(`{}`)})
		final <- completed{result, err}
	}()
	var requestID json.RawMessage
	for _, value := range []float64{0, 50, 100} {
		select {
		case update := <-updates:
			assert.Equal(t, value, update.Value)
			if requestID == nil {
				requestID = update.RequestID
			}
			assert.Equal(t, requestID, update.RequestID)
		case result := <-final:
			t.Fatalf("call returned before progress: %v", result.err)
		case <-ctx.Done():
			t.Fatal("generated progress was not delivered")
		}
	}
	select {
	case result := <-final:
		t.Fatalf("call returned before release: %v", result.err)
	default:
	}
	close(release)
	result := <-final
	require.NoError(t, result.err)
	assert.JSONEq(t, `"finished"`, string(result.result.(*genmcp.ToolsCallResult).StructuredContent))
}

func TestGeneratedHTTPProgressRetry(t *testing.T) {
	var ids []json.RawMessage
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage
			Params struct {
				Meta map[string]json.RawMessage `json:"_meta"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ids = append(ids, request.ID)
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":%s,\"progress\":0}}\n\n", request.Params.Meta["progressToken"]); err != nil {
			return
		}
		if len(ids) == 1 {
			return
		}
		if _, err := io.WriteString(w, fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"content\":[],\"structuredContent\":\"finished\"}}\n\n", request.ID)); err != nil {
			return
		}
	}))
	defer peer.Close()
	location, err := url.Parse(peer.URL)
	require.NoError(t, err)
	client := genclient.NewClient(location.Scheme, location.Host, peer.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	client.Doer = mcpruntime.NewHTTPTransport(peer.Client(), mcpruntime.ClientInfo{}, map[string]mcpruntime.ToolBinding{"test_tool_with_progress": {ReadOnly: true}}, mcpruntime.InputSupport{}, mcpruntime.HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true})
	var updates []mcpruntime.Progress
	ctx := mcpruntime.WithProgress(t.Context(), func(_ context.Context, p mcpruntime.Progress) error { updates = append(updates, p); return nil })
	result, err := client.ToolsCall()(ctx, &genmcp.ToolsCallPayload{Name: "test_tool_with_progress", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"finished"`, string(result.(*genmcp.ToolsCallResult).StructuredContent))
	require.Len(t, ids, 2)
	assert.NotEqual(t, ids[0], ids[1])
	require.Len(t, updates, 2)
	assert.Equal(t, ids[0], updates[0].RequestID)
	assert.Equal(t, ids[1], updates[1].RequestID)
}
