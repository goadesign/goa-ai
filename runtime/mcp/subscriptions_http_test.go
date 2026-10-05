// These tests exercise the HTTP subscription producer through real clients and
// independent wire assertions. They verify filter isolation, exact IDs, source
// rejection, completion, cancellation, and terminal delivery failures.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPSubscriptionProducerConcurrentListeners(t *testing.T) {
	serverErrors := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage
			Params json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			serverErrors <- err
			return
		}
		var payload struct {
			Notifications SubscriptionFilter
		}
		if err := json.Unmarshal(request.Params, &payload); err != nil {
			serverErrors <- err
			return
		}
		serverErrors <- ServeSubscriptions(w, r, request.ID, request.Params, func(w http.ResponseWriter, r *http.Request) {
			accepted := cloneSubscriptionFilter(payload.Notifications)
			assert.NoError(t, AcknowledgeSubscription(r.Context(), accepted))
			if accepted.ToolsListChanged {
				assert.NoError(t, ReportToolsChanged(r.Context()))
				assert.NoError(t, ReportPromptsChanged(r.Context()))
				assert.NoError(t, ReportResourcesChanged(r.Context()))
			} else {
				accepted.ResourceSubscriptions[0] = "file:///unaccepted"
				assert.NoError(t, ReportResourceUpdated(r.Context(), payload.Notifications.ResourceSubscriptions[0]+"/child"))
			}
			assert.NoError(t, json.NewEncoder(w).Encode(struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
			}{rpcVersion, request.ID, json.RawMessage(`{"resultType":"complete","_meta":{"example.test/count":9007199254740993}}`)}))
		})
	}))
	defer server.Close()
	caller := NewHTTPTransport(server.Client(), ClientInfo{Name: "subscription-producer-tests", Version: "1"}, nil, InputSupport{}, HTTPRetryPolicy{})
	filters := []SubscriptionFilter{
		{ToolsListChanged: true, PromptsListChanged: true, ResourcesListChanged: true},
		{ResourceSubscriptions: []string{"file:///requested"}},
	}
	var wg sync.WaitGroup
	ids := make(chan string, len(filters))
	for _, filter := range filters {
		wg.Go(func() {
			var events []SubscriptionEvent
			listenErr := caller.Listen(t.Context(), server.URL, filter, func(_ context.Context, event SubscriptionEvent) error {
				events = append(events, event)
				return nil
			})
			if listenErr != nil {
				t.Errorf("listen failed: %v", listenErr)
			}
			id := ""
			if len(events) > 0 {
				id = string(events[0].RequestID)
			}
			ids <- id
			expected := 2
			if filter.ToolsListChanged {
				expected = 4
			}
			if !assert.Len(t, events, expected) {
				return
			}
			assert.Equal(t, SubscriptionAcknowledged, events[0].Kind)
			assert.Equal(t, filter, events[0].Accepted)
			for _, event := range events {
				assert.Equal(t, string(events[0].RequestID), string(event.RequestID))
			}
			if filter.ToolsListChanged {
				assert.Equal(t, []SubscriptionEventKind{SubscriptionAcknowledged, SubscriptionToolsChanged, SubscriptionPromptsChanged, SubscriptionResourcesChanged}, []SubscriptionEventKind{events[0].Kind, events[1].Kind, events[2].Kind, events[3].Kind})
			} else {
				assert.Len(t, events, 2)
				assert.Equal(t, "file:///requested/child", events[1].URI)
			}
		})
	}
	wg.Wait()
	firstID, secondID := <-ids, <-ids
	assert.NotEqual(t, firstID, secondID)
	assert.NoError(t, <-serverErrors)
	assert.NoError(t, <-serverErrors)
}

func TestHTTPSubscriptionProducerBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		ack     bool
		result  string
		report  func(context.Context) error
		failure string
	}{
		{name: "before acknowledgment", filter: `{}`, report: ReportToolsChanged, failure: "before acknowledgment"},
		{name: "extra kind", filter: `{}`, report: func(ctx context.Context) error {
			return AcknowledgeSubscription(ctx, SubscriptionFilter{ToolsListChanged: true})
		}, failure: "unrequested notification"},
		{name: "extra resource", filter: `{"resourceSubscriptions":["file:///requested"]}`, report: func(ctx context.Context) error {
			return AcknowledgeSubscription(ctx, SubscriptionFilter{ResourceSubscriptions: []string{"file:///other"}})
		}, failure: "unrequested resource"},
		{name: "duplicate acknowledgment", filter: `{}`, ack: true, report: func(ctx context.Context) error { return AcknowledgeSubscription(ctx, SubscriptionFilter{}) }, failure: "more than once"},
		{name: "unaccepted change", filter: `{}`, ack: true, report: ReportToolsChanged, failure: "not acknowledged"},
		{name: "invalid URI", filter: `{"resourceSubscriptions":["file:///requested"]}`, ack: true, report: func(ctx context.Context) error { return ReportResourceUpdated(ctx, "no scheme") }, failure: "URI"},
		{name: "missing acknowledgment", filter: `{}`, result: `{"resultType":"complete"}`, failure: "requires acknowledgment"},
		{name: "unfinished result", filter: `{}`, ack: true, result: `{"resultType":"input_required"}`, failure: "resultType complete"},
		{name: "unexpected data", filter: `{}`, ack: true, result: `{"resultType":"complete","content":[]}`, failure: "unexpected field"},
		{name: "caller-owned ID", filter: `{}`, ack: true, result: `{"resultType":"complete","_meta":{"io.modelcontextprotocol/subscriptionId":1}}`, failure: "ID ownership"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://subscription.test", nil)
			request.Header.Set("Accept", "text/event-stream")
			writer := httptest.NewRecorder()
			err := ServeSubscriptions(writer, request, json.RawMessage(`9007199254740993`), json.RawMessage(`{"notifications":`+test.filter+`}`), func(w http.ResponseWriter, r *http.Request) {
				if test.ack {
					filter, err := decodeSubscriptionFilter(json.RawMessage(test.filter))
					if !assert.NoError(t, err) || !assert.NoError(t, AcknowledgeSubscription(r.Context(), filter)) {
						return
					}
				}
				if test.report != nil {
					assert.ErrorContains(t, test.report(r.Context()), test.failure)
				}
				result := test.result
				if result == "" {
					result = `{"resultType":"complete"}`
				}
				_, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":9007199254740993,"result":%s}`, result)
				assert.NoError(t, err)
			})
			if test.result != "" {
				require.ErrorContains(t, err, test.failure)
			}
			assert.NotContains(t, writer.Body.String(), `"method":"notifications/tools/list_changed"`)
		})
	}
}

func TestHTTPSubscriptionProducerCompletionAndRejection(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://subscription.test", nil)
			request.Header.Set("Accept", "text/event-stream")
			writer := httptest.NewRecorder()
			var retained context.Context
			require.NoError(t, ServeSubscriptions(writer, request, json.RawMessage(`9007199254740993`), json.RawMessage(`{"notifications":{}}`), func(w http.ResponseWriter, r *http.Request) {
				retained = r.Context()
				w.Header().Set("X-Source-Policy", "authorized")
				if accepted {
					if !assert.NoError(t, AcknowledgeSubscription(retained, SubscriptionFilter{})) {
						return
					}
					_, err := fmt.Fprint(w, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"resultType":"complete","_meta":{"example.test/count":9007199254740993}}}`)
					assert.NoError(t, err)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, err := fmt.Fprint(w, `{"jsonrpc":"2.0","id":9007199254740993,"error":{"code":-32000,"message":"source denied"}}`)
					assert.NoError(t, err)
				}
			}))
			require.ErrorContains(t, AcknowledgeSubscription(retained, SubscriptionFilter{}), "finished")
			body := writer.Body.String()
			assert.Equal(t, "authorized", writer.Header().Get("X-Source-Policy"))
			if accepted {
				assert.Equal(t, 2, strings.Count(body, "data: "))
				assert.Contains(t, body, `"example.test/count":9007199254740993`)
				assert.Equal(t, 2, strings.Count(body, `"io.modelcontextprotocol/subscriptionId":9007199254740993`))
				assert.Equal(t, "text/event-stream", writer.Header().Get("Content-Type"))
			} else {
				assert.Equal(t, http.StatusForbidden, writer.Code)
				assert.NotContains(t, body, "notifications/subscriptions/acknowledged")
				assert.Contains(t, body, "source denied")
			}
		})
	}
}

func TestHTTPSubscriptionProducerCancellationAndWriteFailure(t *testing.T) {
	failure := errors.New("connection lost")
	writer := &progressFailureWriter{header: make(http.Header), failure: failure}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://subscription.test", nil)
	request.Header.Set("Accept", "text/event-stream")
	err := ServeSubscriptions(writer, request, json.RawMessage(`"listen"`), json.RawMessage(`{"notifications":{"toolsListChanged":true}}`), func(_ http.ResponseWriter, r *http.Request) {
		assert.ErrorIs(t, AcknowledgeSubscription(r.Context(), SubscriptionFilter{ToolsListChanged: true}), failure)
		assert.ErrorIs(t, ReportToolsChanged(r.Context()), failure)
	})
	require.ErrorIs(t, err, failure)
	assert.Equal(t, 1, writer.writes)

	ctx, cancel := context.WithCancel(t.Context())
	recorder := httptest.NewRecorder()
	err = ServeSubscriptions(recorder, request.WithContext(ctx), json.RawMessage(`"listen"`), json.RawMessage(`{"notifications":{}}`), func(_ http.ResponseWriter, r *http.Request) {
		if !assert.NoError(t, AcknowledgeSubscription(r.Context(), SubscriptionFilter{})) {
			return
		}
		cancel()
		assert.ErrorIs(t, ReportToolsChanged(r.Context()), context.Canceled)
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, recorder.Body.String(), `"resultType"`)
	assert.ErrorContains(t, AcknowledgeSubscription(t.Context(), SubscriptionFilter{}), "active listen")
}

func TestHTTPSubscriptionProducerQuietCancellation(t *testing.T) {
	acknowledged := make(chan struct{})
	sourceEnded := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage
			Params json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			sourceEnded <- err
			return
		}
		sourceEnded <- ServeSubscriptions(w, r, request.ID, request.Params, func(_ http.ResponseWriter, r *http.Request) {
			if !assert.NoError(t, AcknowledgeSubscription(r.Context(), SubscriptionFilter{ResourceSubscriptions: []string{"file:///quiet"}})) {
				return
			}
			<-r.Context().Done()
		})
	}))
	defer server.Close()
	caller := NewHTTPTransport(server.Client(), ClientInfo{Name: "quiet-test", Version: "1"}, nil, InputSupport{}, HTTPRetryPolicy{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- caller.Listen(ctx, server.URL, SubscriptionFilter{ResourceSubscriptions: []string{"file:///quiet"}}, func(_ context.Context, event SubscriptionEvent) error {
			if event.Kind == SubscriptionAcknowledged {
				close(acknowledged)
			}
			return nil
		})
	}()
	<-acknowledged
	cancel()
	require.ErrorIs(t, <-finished, context.Canceled)
	require.ErrorIs(t, <-sourceEnded, context.Canceled)
}
