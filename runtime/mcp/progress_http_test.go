// These tests exchange HTTP progress before a final unary response. They also
// verify stream failure, cancellation, and per-attempt identity during retries.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	progressFailureWriter struct {
		header  http.Header
		writes  int
		failure error
	}
)

func (w *progressFailureWriter) Header() http.Header       { return w.header }
func (w *progressFailureWriter) WriteHeader(int)           {}
func (w *progressFailureWriter) Write([]byte) (int, error) { w.writes++; return 0, w.failure }

func TestHTTPProgressBeforeFinalResponse(t *testing.T) {
	observed := make(chan Progress, 3)
	release := make(chan struct{})
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage
			Params json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			serverErrors <- err
			return
		}
		serverErrors <- ServeProgress(w, r, request.Params, func(w http.ResponseWriter, r *http.Request) {
			for _, value := range []float64{0, 50, 100} {
				if err := ReportProgress(r.Context(), value, new(float64(100)), new("working")); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			if err := json.NewEncoder(w).Encode(struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
			}{rpcVersion, request.ID, json.RawMessage(`{"resultType":"complete","content":[],"structuredContent":42}`)}); err != nil {
				http.Error(w, err.Error(), 500)
			}
		})
	}))
	defer server.Close()

	transport := NewHTTPTransport(server.Client(), ClientInfo{Name: "progress-tests", Version: "1"}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
	deadline, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx := WithProgress(deadline, func(_ context.Context, update Progress) error { observed <- update; return nil })
	completed := make(chan error, 1)
	go func() {
		result, err := transport.CallTool(ctx, server.URL, CallRequest{Tool: "work", Payload: json.RawMessage(`{}`)})
		if err == nil && string(result.StructuredContent) != "42" {
			err = errors.New("final result changed")
		}
		completed <- err
	}()
	var firstID json.RawMessage
	for _, value := range []float64{0, 50, 100} {
		select {
		case update := <-observed:
			assert.InDelta(t, value, update.Value, 0)
			assert.Equal(t, new(float64(100)), update.Total)
			if firstID == nil {
				firstID = update.RequestID
			}
			assert.Equal(t, firstID, update.RequestID)
		case <-ctx.Done():
			t.Fatal("progress was not delivered before the final result")
		}
	}
	select {
	case err := <-completed:
		t.Fatalf("operation completed before release: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-completed)
	require.NoError(t, <-serverErrors)
}

// TestTaskServerOmitsProgress verifies that an explicit client token cannot
// make a Task operation emit progress or change its ordinary response framing.
func TestTaskServerOmitsProgress(t *testing.T) {
	for _, method := range []string{methodTasksGet, methodTasksUpdate, methodTasksCancel} {
		t.Run(method, func(t *testing.T) {
			writer := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Mcp-Method", method)
			require.NoError(t, ServeProgress(writer, request, json.RawMessage(`{"_meta":{"progressToken":"unsupported"}}`), func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, ReportProgress(r.Context(), 1, nil, nil))
				assert.NoError(t, ReportProgress(r.Context(), 1, nil, nil))
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":"task-operation","result":{"resultType":"complete"}}`)
				assert.NoError(t, err)
			}))
			assert.Equal(t, "application/json", writer.Header().Get("Content-Type"))
			assert.NotContains(t, writer.Body.String(), "notifications/progress")
			assert.Contains(t, writer.Body.String(), `"id":"task-operation"`)
		})
	}
}

func TestHTTPProgressResponseModesAndClosedContext(t *testing.T) {
	for _, test := range []struct {
		name, accept, params, media string
		reports                     bool
	}{
		{"requested", "application/json, text/event-stream", `{"_meta":{"progressToken":9007199254740993}}`, "text/event-stream", true},
		{"unrequested", "application/json, text/event-stream", `{}`, "application/json", false},
		{"JSON only", "application/json", `{"_meta":{"progressToken":"t"}}`, "application/json", false},
		{"no reports", "text/event-stream", `{"_meta":{"progressToken":"t"}}`, "application/json", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			request.Header.Set("Accept", test.accept)
			var retained context.Context
			require.NoError(t, ServeProgress(writer, request, json.RawMessage(test.params), func(w http.ResponseWriter, r *http.Request) {
				retained = r.Context()
				if test.reports {
					assert.NoError(t, ReportProgress(r.Context(), -1, nil, nil))
					assert.Error(t, ReportProgress(r.Context(), -1, nil, nil))
					assert.NoError(t, ReportProgress(r.Context(), 0.5, nil, nil))
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":"original","result":{"resultType":"complete","content":[]}}`)
				assert.NoError(t, err)
			}))
			assert.Equal(t, test.media, writer.Header().Get("Content-Type"))
			if test.reports {
				assert.Contains(t, writer.Body.String(), `"progressToken":9007199254740993`)
				assert.ErrorContains(t, ReportProgress(retained, 1, nil, nil), "finished")
			} else {
				assert.NotContains(t, writer.Body.String(), "notifications/progress")
			}
		})
	}
}

func TestHTTPProgressWriteFailureStopsStream(t *testing.T) {
	failure := errors.New("connection lost")
	writer := &progressFailureWriter{header: make(http.Header), failure: failure}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "text/event-stream")
	err := ServeProgress(writer, request, json.RawMessage(`{"_meta":{"progressToken":"t"}}`), func(w http.ResponseWriter, r *http.Request) {
		assert.ErrorIs(t, ReportProgress(r.Context(), 0, nil, nil), failure)
		assert.ErrorIs(t, ReportProgress(r.Context(), 1, nil, nil), failure)
		_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete"}}`)
		assert.NoError(t, err)
	})
	require.ErrorIs(t, err, failure)
	assert.Equal(t, 1, writer.writes)
}

func TestHTTPProgressAcceptRanges(t *testing.T) {
	for _, test := range []struct {
		value    string
		accepted bool
	}{
		{"text/event-stream", true}, {"text/*", true}, {"*/*", true}, {"application/json", false},
		{"text/event-stream;q=0, */*;q=1", false}, {"text/*;q=0, */*;q=1", false},
		{"text/event-stream;q=0.1, text/*;q=0", true}, {"text/event-stream;q=NaN", false}, {"text/event-stream;q=2", false}, {"text/event-stream;q=-1", false},
	} {
		t.Run(test.value, func(t *testing.T) { assert.Equal(t, test.accepted, acceptsEventStream([]string{test.value})) })
	}
}

func TestHTTPProgressRetryIdentity(t *testing.T) {
	var ids, tokens []json.RawMessage
	var updates []Progress
	transport := NewHTTPTransport(transportFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			ID     json.RawMessage
			Params struct {
				Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if err := r.Body.Close(); err != nil {
			return nil, err
		}
		token := request.Params.Meta["progressToken"]
		ids = append(ids, request.ID)
		tokens = append(tokens, token)
		body := fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":%s,\"progress\":0}}\n\n", token)
		if len(ids) == 2 {
			body += fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"content\":[],\"structuredContent\":42}}\n\n", request.ID)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}), ClientInfo{}, HTTPBindings{Tools: map[string]ToolBinding{"work": {ReadOnly: true}}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true})
	ctx := WithProgress(t.Context(), func(_ context.Context, p Progress) error { updates = append(updates, p); return nil })
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"original","method":"tools/call","params":{"name":"work","arguments":{}}}`))
	require.NoError(t, err)
	response, err := transport.Do(request)
	require.NoError(t, err)
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	var result rpcMessage
	require.NoError(t, json.Unmarshal(data, &result))
	assert.JSONEq(t, `"original"`, string(result.ID))
	require.Len(t, ids, 2)
	assert.NotEqual(t, ids[0], ids[1])
	assert.NotEqual(t, tokens[0], tokens[1])
	require.Len(t, updates, 2)
	assert.Equal(t, ids[0], updates[0].RequestID)
	assert.Equal(t, ids[1], updates[1].RequestID)
	assert.Zero(t, updates[0].Value)
	assert.Zero(t, updates[1].Value)
}

func TestHTTPProgressCallbackFailureDoesNotRetry(t *testing.T) {
	failure := errors.New("host rejected update")
	calls := 0
	transport := NewHTTPTransport(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			ID     json.RawMessage
			Params struct {
				Meta map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if err := r.Body.Close(); err != nil {
			return nil, err
		}
		body := fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":%s,\"progress\":0}}\n\n", request.Params.Meta["progressToken"])
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}), ClientInfo{}, HTTPBindings{Tools: map[string]ToolBinding{"work": {ReadOnly: true}}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true})
	ctx := WithProgress(t.Context(), func(context.Context, Progress) error { return failure })
	_, err := transport.CallTool(ctx, "https://example.test/mcp", CallRequest{Tool: "work", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, failure)
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, 1, calls)
}
