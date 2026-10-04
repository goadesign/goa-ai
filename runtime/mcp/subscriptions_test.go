// These tests use independent HTTP and stdio peers to exercise subscription
// ordering, filter selection, correlation, cancellation, and transport loss.
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
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPSubscriptionFiltersAndCompletion(t *testing.T) {
	filter := SubscriptionFilter{ToolsListChanged: true, PromptsListChanged: true, ResourcesListChanged: true, ResourceSubscriptions: []string{"file:///directory"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, ProtocolVersion, r.Header.Get("MCP-Protocol-Version"))
		assert.Equal(t, "subscriptions/listen", r.Header.Get("Mcp-Method"))
		assert.Empty(t, r.Header.Get("Mcp-Session-Id"))
		assert.Empty(t, r.Header.Get("Mcp-Name"))
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Notifications SubscriptionFilter         `json:"notifications"`
				Meta          map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, filter, request.Params.Notifications)
		assert.JSONEq(t, `{"name":"subscription-tests","version":"1"}`, string(request.Params.Meta[clientInfoKey]))
		assert.JSONEq(t, `{}`, string(request.Params.Meta[clientCapabilitiesKey]))
		assert.NotContains(t, request.Params.Meta, "progressToken")
		w.Header().Set("Content-Type", "text/event-stream")
		messages := []map[string]any{
			subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{"toolsListChanged": true, "resourcesListChanged": true, "resourceSubscriptions": []string{"file:///directory"}}}),
			subscriptionPeerNotification("notifications/tools/list_changed", request.ID, nil),
			subscriptionPeerNotification("notifications/resources/list_changed", request.ID, nil),
			subscriptionPeerNotification("notifications/resources/updated", request.ID, map[string]any{"uri": "file:///directory/child"}),
			subscriptionPeerComplete(request.ID),
		}
		for _, message := range messages {
			if err := writeSubscriptionPeerSSE(w, message); !assert.NoError(t, err) {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "subscription-tests", Version: "1"}})
	require.NoError(t, err)
	var events []SubscriptionEvent
	ctx := WithProgress(t.Context(), func(context.Context, Progress) error { t.Error("listen must not request progress"); return nil })
	err = caller.Listen(ctx, filter, func(_ context.Context, event SubscriptionEvent) error {
		events = append(events, event)
		// The receiver must retain its own acknowledgment after a host changes
		// the event it received. A sub-resource notification remains valid.
		if event.Kind == SubscriptionAcknowledged {
			event.Accepted.ResourceSubscriptions[0] = "file:///other"
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 4)
	assert.Equal(t, []SubscriptionEventKind{SubscriptionAcknowledged, SubscriptionToolsChanged, SubscriptionResourcesChanged, SubscriptionResourceUpdated}, []SubscriptionEventKind{events[0].Kind, events[1].Kind, events[2].Kind, events[3].Kind})
	assert.False(t, events[0].Accepted.PromptsListChanged)
	assert.Equal(t, "file:///directory/child", events[3].URI)
	for _, event := range events[1:] {
		assert.Equal(t, events[0].RequestID, event.RequestID)
	}
}

func TestHTTPSubscriptionRejectsMalformedPeers(t *testing.T) {
	cases := []struct {
		name     string
		messages func(json.RawMessage) []map[string]any
	}{
		{"before acknowledgment", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/tools/list_changed", id, nil)}
		}},
		{"extra kind in acknowledgment", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{"promptsListChanged": true}})}
		}},
		{"extra resource in acknowledgment", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{"file:///other"}}})}
		}},
		{"wrong ID", func(_ json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", json.RawMessage(`"other"`), map[string]any{"notifications": map[string]any{}})}
		}},
		{"missing ID", func(_ json.RawMessage) []map[string]any {
			return []map[string]any{{"jsonrpc": "2.0", "method": "notifications/subscriptions/acknowledged", "params": map[string]any{"notifications": map[string]any{}}}}
		}},
		{"null filter", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": nil})}
		}},
		{"null filter flag", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{"toolsListChanged": nil}})}
		}},
		{"unacknowledged change", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{}}), subscriptionPeerNotification("notifications/tools/list_changed", id, nil)}
		}},
		{"duplicate acknowledgment", func(id json.RawMessage) []map[string]any {
			ack := subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{}})
			return []map[string]any{ack, ack}
		}},
		{"missing resource URI", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{"file:///directory"}}}), subscriptionPeerNotification("notifications/resources/updated", id, nil)}
		}},
		{"invalid resource URI", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{"file:///directory"}}}), subscriptionPeerNotification("notifications/resources/updated", id, map[string]any{"uri": "relative"})}
		}},
		{"unknown notification", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{}}), subscriptionPeerNotification("notifications/progress", id, map[string]any{"progressToken": 1, "progress": 1})}
		}},
		{"server cancellation on HTTP", func(id json.RawMessage) []map[string]any {
			return []map[string]any{{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id}}}
		}},
		{"completion without acknowledgment", func(id json.RawMessage) []map[string]any { return []map[string]any{subscriptionPeerComplete(id)} }},
		{"completion without subscription ID", func(id json.RawMessage) []map[string]any {
			return []map[string]any{subscriptionPeerNotification("notifications/subscriptions/acknowledged", id, map[string]any{"notifications": map[string]any{}}), {"jsonrpc": "2.0", "id": id, "result": map[string]any{"resultType": "complete"}}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID json.RawMessage `json:"id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, message := range test.messages(request.ID) {
					if err := writeSubscriptionPeerSSE(w, message); !assert.NoError(t, err) {
						return
					}
				}
			}))
			t.Cleanup(server.Close)
			caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
			require.NoError(t, err)
			err = caller.Listen(t.Context(), SubscriptionFilter{ToolsListChanged: true, ResourceSubscriptions: []string{"file:///directory"}}, func(context.Context, SubscriptionEvent) error { return nil })
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func TestHTTPSubscriptionInterruptionDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		assert.NoError(t, writeSubscriptionPeerSSE(w, subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{}})))
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}, RetryPolicy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}})
	require.NoError(t, err)
	err = caller.Listen(t.Context(), SubscriptionFilter{}, func(context.Context, SubscriptionEvent) error { return nil })
	var interrupted *interruptedResponseError
	require.ErrorAs(t, err, &interrupted)
	assert.Equal(t, 1, calls)
}

func TestHTTPSubscriptionCallbackFailureClosesRequest(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if err := writeSubscriptionPeerSSE(w, subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{}})); !assert.NoError(t, err) {
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	failure := errors.New("host cannot accept subscription")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = caller.Listen(ctx, SubscriptionFilter{}, func(context.Context, SubscriptionEvent) error { return failure })
	require.ErrorIs(t, err, failure)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("callback failure did not close HTTP request")
	}
}

func TestSubscriptionConstructionRejectsInvalidInput(t *testing.T) {
	transport := NewHTTPTransport(http.DefaultClient, ClientInfo{Name: "tests", Version: "1"}, nil, InputSupport{}, HTTPRetryPolicy{})
	assert.Error(t, transport.Listen(t.Context(), "http://unused", SubscriptionFilter{}, nil))
}

func TestStdioSubscriptionsInterleaveAndCancellation(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioSubscriptionPeer$"}, Env: []string{"GOA_AI_MCP_SUBSCRIPTION_PEER=1"}, ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	type listenerResult struct {
		uri    string
		events []SubscriptionEvent
		err    error
	}
	var wg sync.WaitGroup
	results := make(chan listenerResult, 2)
	for _, uri := range []string{"file:///one", "file:///two"} {
		wg.Go(func() {
			var events []SubscriptionEvent
			err := caller.Listen(ctx, SubscriptionFilter{ResourceSubscriptions: []string{uri}}, func(_ context.Context, event SubscriptionEvent) error {
				events = append(events, event)
				return nil
			})
			results <- listenerResult{uri: uri, events: events, err: err}
		})
	}
	wg.Wait()
	close(results)
	var observed []string
	for result := range results {
		require.NoError(t, result.err)
		require.Len(t, result.events, 2)
		assert.Equal(t, SubscriptionAcknowledged, result.events[0].Kind)
		assert.Equal(t, SubscriptionResourceUpdated, result.events[1].Kind)
		assert.Equal(t, result.uri, result.events[1].URI)
		assert.Equal(t, result.events[0].RequestID, result.events[1].RequestID)
		observed = append(observed, string(result.events[0].RequestID))
	}
	assert.Len(t, slices.Compact(observed), 2)

	blocked, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = caller.Listen(blocked, SubscriptionFilter{ResourceSubscriptions: []string{"file:///cancel"}}, func(context.Context, SubscriptionEvent) error { return nil })
	stop()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	response, err := caller.CallTool(ctx, CallRequest{Tool: "after-cancel", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"after-cancel"`, string(response.StructuredContent))
	failure := errors.New("host callback failed")
	err = caller.Listen(ctx, SubscriptionFilter{ResourceSubscriptions: []string{"file:///cancel"}}, func(context.Context, SubscriptionEvent) error { return failure })
	require.ErrorIs(t, err, failure)
	response, err = caller.CallTool(ctx, CallRequest{Tool: "after-callback", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"after-callback"`, string(response.StructuredContent))
	err = caller.Listen(ctx, SubscriptionFilter{ResourceSubscriptions: []string{"file:///server-cancel"}}, func(context.Context, SubscriptionEvent) error { return nil })
	var cancelled *SubscriptionCancelledError
	require.ErrorAs(t, err, &cancelled)
	assert.Equal(t, "source stopped", *cancelled.Reason)
	response, err = caller.CallTool(ctx, CallRequest{Tool: "after-server-cancel", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"after-server-cancel"`, string(response.StructuredContent))
}

func TestStdioSubscriptionPeer(_ *testing.T) {
	if os.Getenv("GOA_AI_MCP_SUBSCRIPTION_PEER") != "1" {
		return
	}
	if err := serveStdioSubscriptionPeer(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(3)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

// serveStdioSubscriptionPeer interleaves two listeners and ordinary tools. A
// cancelled listener emits a late event that must not reach another operation.
func serveStdioSubscriptionPeer() error {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	type listen struct {
		id  json.RawMessage
		uri string
	}
	var pair []listen
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var request struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			return err
		}
		switch request.Method {
		case "subscriptions/listen":
			var filter struct {
				ResourceSubscriptions []string `json:"resourceSubscriptions"` //nolint:tagliatelle // MCP wire name.
			}
			if err := json.Unmarshal(request.Params["notifications"], &filter); err != nil {
				return err
			}
			if len(filter.ResourceSubscriptions) != 1 {
				return errors.New("peer expects one URI")
			}
			uri := filter.ResourceSubscriptions[0]
			if uri == "file:///one" || uri == "file:///two" {
				pair = append(pair, listen{request.ID, uri})
				if len(pair) != 2 {
					continue
				}
				slices.Reverse(pair)
				for _, entry := range pair {
					if err := encoder.Encode(subscriptionPeerNotification("notifications/subscriptions/acknowledged", entry.id, map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{entry.uri}}})); err != nil {
						return err
					}
				}
				for _, entry := range pair {
					if err := encoder.Encode(subscriptionPeerNotification("notifications/resources/updated", entry.id, map[string]any{"uri": entry.uri})); err != nil {
						return err
					}
				}
				for _, entry := range pair {
					if err := encoder.Encode(subscriptionPeerComplete(entry.id)); err != nil {
						return err
					}
				}
				pair = nil
				continue
			}
			if err := encoder.Encode(subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{uri}}})); err != nil {
				return err
			}
			if uri == "file:///burst" {
				for range 8 {
					if err := encoder.Encode(subscriptionPeerNotification("notifications/resources/updated", request.ID, map[string]any{"uri": uri})); err != nil {
						return err
					}
				}
			}

			if uri == "file:///server-cancel" {
				if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": request.ID, "reason": "source stopped"}}); err != nil {
					return err
				}
			}
		case "notifications/cancelled":
			if err := encoder.Encode(subscriptionPeerNotification("notifications/resources/updated", request.Params["requestId"], map[string]any{"uri": "file:///late"})); err != nil {
				return err
			}
		case "tools/call":
			var name string
			if err := json.Unmarshal(request.Params["name"], &name); err != nil {
				return err
			}
			if name == "ignore-cancellation" {
				for _, params := range []map[string]any{{"requestId": "unknown"}, {"requestId": nil}, {"requestId": request.ID, "reason": nil}, {"requestId": request.ID}} {
					if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": params}); err != nil {
						return err
					}
				}
			}

			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": name}}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected method %s", request.Method)
		}
	}
}

// subscriptionPeerNotification constructs raw peer messages independently of
// the consumer's decoding types and state checks.
func subscriptionPeerNotification(method string, id json.RawMessage, fields map[string]any) map[string]any {
	params := map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/subscriptionId": id}}
	for key, value := range fields {
		params[key] = value
	}
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func subscriptionPeerComplete(id json.RawMessage) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"resultType": "complete", "_meta": map[string]any{"io.modelcontextprotocol/subscriptionId": id}}}
}

func writeSubscriptionPeerSSE(w http.ResponseWriter, message map[string]any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

func TestHTTPSubscriptionCancellationDuringQuietStream(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if err := writeSubscriptionPeerSSE(w, subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{}})); !assert.NoError(t, err) {
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	err = caller.Listen(ctx, SubscriptionFilter{}, func(context.Context, SubscriptionEvent) error { cancel(); return nil })
	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("quiet HTTP subscription did not stop")
	}
}

func TestSubscriptionReceiverPreservesLargeAndStringRequestIDs(t *testing.T) {
	for _, id := range []string{`""`, `"listen/one"`, `90071992547409930001`, `1e2`} {
		t.Run(id, func(t *testing.T) {
			var received SubscriptionEvent
			ctx := context.WithValue(t.Context(), subscriptionHandlerKey{}, subscriptionHandler(func(_ context.Context, event SubscriptionEvent) error { received = event; return nil }))
			receiver, err := newSubscriptionReceiver(ctx, json.RawMessage(id), json.RawMessage(`{"resourceSubscriptions":["peer-owned-address"]}`))
			require.NoError(t, err)
			data, err := json.Marshal(subscriptionPeerNotification("notifications/subscriptions/acknowledged", json.RawMessage(id), map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{"peer-owned-address"}}}))
			require.NoError(t, err)
			var message rpcMessage
			require.NoError(t, json.Unmarshal(data, &message))
			require.NoError(t, receiver.notification(ctx, message))
			assert.Equal(t, id, string(received.RequestID))
		})
	}
}

func TestStdioSubscriptionBlockedCallbackReleasesReader(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioSubscriptionPeer$"}, Env: []string{"GOA_AI_MCP_SUBSCRIPTION_PEER=1"}, ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	blocked, stop := context.WithCancel(ctx)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- caller.Listen(blocked, SubscriptionFilter{ResourceSubscriptions: []string{"file:///burst"}}, func(ctx context.Context, _ SubscriptionEvent) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("subscription did not start")
	}
	stop()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("blocked callback did not stop")
	}
	response, err := caller.CallTool(ctx, CallRequest{Tool: "reader-released", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"reader-released"`, string(response.StructuredContent))
}

func TestSubscriptionFilterWireValidation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{ "toolsListChanged":null }`, `{ "promptsListChanged":1 }`, `{ "resourcesListChanged":"true" }`, `{ "resourceSubscriptions":null }`, `{ "resourceSubscriptions":[null] }`, `{ "resourceSubscriptions":[1] }`} {
		t.Run(raw, func(t *testing.T) { _, err := decodeSubscriptionFilter(json.RawMessage(raw)); assert.Error(t, err) })
	}
	filter, err := decodeSubscriptionFilter(json.RawMessage(`{"resourceSubscriptions":["", "peer-owned-address"],"toolsListChanged":false}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"", "peer-owned-address"}, filter.ResourceSubscriptions)
}

func TestStdioSubscriptionIgnoresInvalidAndUnrelatedCancellation(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioSubscriptionPeer$"}, Env: []string{"GOA_AI_MCP_SUBSCRIPTION_PEER=1"}, ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response, err := caller.CallTool(ctx, CallRequest{Tool: "ignore-cancellation", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"ignore-cancellation"`, string(response.StructuredContent))
}

func TestHTTPSubscriptionStopsBufferedDeliveryAfterCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, message := range []map[string]any{
			subscriptionPeerNotification("notifications/subscriptions/acknowledged", request.ID, map[string]any{"notifications": map[string]any{"toolsListChanged": true}}),
			subscriptionPeerNotification("notifications/tools/list_changed", request.ID, nil),
			subscriptionPeerComplete(request.ID),
		} {
			if err := writeSubscriptionPeerSSE(w, message); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := 0
	err = caller.Listen(ctx, SubscriptionFilter{ToolsListChanged: true}, func(context.Context, SubscriptionEvent) error { observed++; cancel(); return nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, observed)
}

func TestHTTPSubscriptionNotificationCannotArriveOnToolResponse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if err := writeSubscriptionPeerSSE(w, subscriptionPeerNotification("notifications/tools/list_changed", request.ID, nil)); err != nil {
			return
		}
	}))
	t.Cleanup(server.Close)
	transport := NewHTTPTransport(server.Client(), ClientInfo{Name: "tests", Version: "1"}, map[string]ToolBinding{"lookup": {ReadOnly: true}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true})
	_, err := transport.CallTool(t.Context(), server.URL, CallRequest{Tool: "lookup", Payload: json.RawMessage(`{}`)})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, 1, calls)
}
