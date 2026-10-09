// These tests send Task notifications through the existing HTTP and stdio
// listeners. Peers must acknowledge exact task IDs before full state can reach
// the host, and Task input must satisfy the host's advertised capabilities.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTaskSubscriptionsFullState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage
			Params struct {
				Notifications SubscriptionFilter
				Meta          map[string]json.RawMessage `json:"_meta"` //nolint:tagliatelle // MCP wire name.
			}
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, []string{taskPeerID, "failed-job", "cancelled-job"}, request.Params.Notifications.TaskIDs)
		assert.JSONEq(t, `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`, string(request.Params.Meta[clientCapabilitiesKey]))
		assert.NotContains(t, request.Params.Meta, "progressToken")
		w.Header().Set("Content-Type", "text/event-stream")
		if !assert.NoError(t, writeSubscriptionPeerSSE(w, subscriptionPeerNotification(string(SubscriptionAcknowledged), request.ID, map[string]any{"notifications": request.Params.Notifications}))) {
			return
		}
		for _, item := range []struct{ id, status, extra string }{
			{taskPeerID, "working", ""},
			{taskPeerID, "input_required", `,"inputRequests":{}`},
			{taskPeerID, "completed", `,"result":{"resultType":"complete","content":[{"type":"text","text":"done"}],"structuredContent":{"count":9007199254740993},"isError":true}`},
			{"failed-job", "failed", `,"error":{"code":-32603,"message":"job failed","data":{"reason":"synthetic"}}`},
			{"cancelled-job", "cancelled", ""},
		} {
			fields := make(map[string]any)
			raw := "{" + strings.Replace(taskPeerMetadata, `"status":"working"`, `"status":"`+item.status+`"`, 1) + item.extra + "}"
			// Preserve the exact number in the completed result, rather than decode
			// peer JSON into floating-point values while constructing this fixture.
			var exact map[string]json.RawMessage
			if !assert.NoError(t, json.Unmarshal([]byte(raw), &exact)) {
				return
			}
			exact["taskId"] = json.RawMessage(`"` + item.id + `"`)
			for name, value := range exact {
				fields[name] = value
			}
			if !assert.NoError(t, writeSubscriptionPeerSSE(w, subscriptionPeerNotification(string(SubscriptionTaskChanged), request.ID, fields))) {
				return
			}
		}
		assert.NoError(t, writeSubscriptionPeerSSE(w, subscriptionPeerComplete(request.ID)))
	}))
	defer server.Close()
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "task-subscription-tests", Version: "1"}})
	require.NoError(t, err)
	var tasks []Task
	err = caller.Listen(t.Context(), SubscriptionFilter{TaskIDs: []string{taskPeerID, "failed-job", "cancelled-job"}}, func(_ context.Context, event SubscriptionEvent) error {
		if event.Kind == SubscriptionAcknowledged {
			// Host mutation must not change the receiver's accepted task IDs.
			event.Accepted.TaskIDs[0] = "another-task"
			return nil
		}
		if assert.Equal(t, SubscriptionTaskChanged, event.Kind) && assert.NotNil(t, event.Task) {
			tasks = append(tasks, *event.Task)
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, tasks, 5)
	assert.Equal(t, TaskWorking, tasks[0].Info().Status)
	input, ok := tasks[1].AsInputRequired()
	require.True(t, ok)
	assert.Empty(t, input.Requests)
	completed, ok := tasks[2].AsCompleted()
	require.True(t, ok)
	assert.True(t, completed.IsError)
	assert.JSONEq(t, `{"count":9007199254740993}`, string(completed.StructuredContent))
	failed, ok := tasks[3].AsFailed()
	require.True(t, ok)
	assert.Equal(t, JSONRPCInternalError, failed.Code)
	assert.Equal(t, TaskCancelled, tasks[4].Info().Status)
}

func TestStdioTaskSubscriptions(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioSubscriptionPeer$"}, Env: []string{"GOA_AI_MCP_SUBSCRIPTION_PEER=1"}, ClientInfo: ClientInfo{Name: "task-subscription-tests", Version: "1"}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	var tasks []Task
	err = caller.Listen(t.Context(), SubscriptionFilter{TaskIDs: []string{taskPeerID, "another-job"}}, func(_ context.Context, event SubscriptionEvent) error {
		if event.Task != nil {
			tasks = append(tasks, *event.Task)
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, tasks, 4)
	assert.Equal(t, taskPeerID, tasks[0].Info().TaskID)
	assert.Equal(t, TaskCancelled, tasks[1].Info().Status)
	assert.Equal(t, "another-job", tasks[2].Info().TaskID)
}

func TestTaskSubscriptionRejectsUnacceptedOrMalformedState(t *testing.T) {
	for _, item := range []struct {
		name, raw string
		support   InputSupport
		failure   string
	}{
		{"unaccepted task", `{"taskId":"other","status":"working","createdAt":"date","lastUpdatedAt":"date","ttlMs":null}`, InputSupport{}, "not acknowledged"},
		{"missing retention", `{"taskId":"job / α","status":"working","createdAt":"date","lastUpdatedAt":"date"}`, InputSupport{}, "ttlMs"},
		{"missing input", `{"taskId":"job / α","status":"input_required","createdAt":"date","lastUpdatedAt":"date","ttlMs":null}`, InputSupport{}, "inputRequests"},
		{"unsupported form", `{"taskId":"job / α","status":"input_required","createdAt":"date","lastUpdatedAt":"date","ttlMs":null,"inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"value?","requestedSchema":{"type":"object","properties":{}}}}}}`, InputSupport{}, "unadvertised form"},
		{"malformed result", `{"taskId":"job / α","status":"completed","createdAt":"date","lastUpdatedAt":"date","ttlMs":null,"result":{"resultType":"input_required","inputRequests":{}}}`, InputSupport{}, "complete tool result"},
	} {
		t.Run(item.name, func(t *testing.T) {
			delivered := 0
			ctx := WithSubscriptionEvents(WithTaskSupport(t.Context()), func(context.Context, SubscriptionEvent) error {
				delivered++
				return nil
			})
			receiver, err := newSubscriptionReceiver(ctx, json.RawMessage(`1`), json.RawMessage(`{"taskIds":["job / α"]}`), item.support)
			require.NoError(t, err)
			require.NoError(t, receiver.acknowledge(SubscriptionFilter{TaskIDs: []string{taskPeerID}}))
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(item.raw), &fields))
			raw, err := json.Marshal(subscriptionPeerNotification(string(SubscriptionTaskChanged), json.RawMessage(`1`), fields))
			require.NoError(t, err)
			var message rpcMessage
			require.NoError(t, json.Unmarshal(raw, &message))
			require.ErrorContains(t, receiver.notification(ctx, message), item.failure)
			assert.Zero(t, delivered)
		})
	}
}

func TestTaskSubscriptionFilterContract(t *testing.T) {
	for _, raw := range []string{`{"taskIds":null}`, `{"taskIds":[null]}`, `{"taskIds":[1]}`} {
		_, err := decodeSubscriptionFilter(json.RawMessage(raw))
		require.Error(t, err)
	}
	filter, err := decodeSubscriptionFilter(json.RawMessage(`{"taskIds":["", "job / α"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"", taskPeerID}, filter.TaskIDs)
	ctx := WithSubscriptionEvents(t.Context(), func(context.Context, SubscriptionEvent) error {
		return nil
	})
	_, err = newSubscriptionReceiver(ctx, json.RawMessage(`1`), json.RawMessage(`{"taskIds":["job / α"]}`), InputSupport{})
	require.ErrorContains(t, err, "declared Tasks support")
	state := subscriptionState{requested: SubscriptionFilter{TaskIDs: []string{taskPeerID}}}
	require.ErrorContains(t, state.acknowledge(SubscriptionFilter{TaskIDs: []string{"other"}}), "unrequested task")
	require.ErrorContains(t, state.change(SubscriptionTaskChanged, taskPeerID), "before acknowledgment")
}

func TestTaskSubscriptionHostInputSupport(t *testing.T) {
	for _, item := range []struct {
		name     string
		disabled bool
	}{
		{name: "supported form"},
		{name: "host input disabled", disabled: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			delivered := 0
			ctx := WithSubscriptionEvents(WithTaskSupport(t.Context()), func(_ context.Context, event SubscriptionEvent) error {
				delivered++
				input, ok := event.Task.AsInputRequired()
				require.True(t, ok)
				assert.Contains(t, input.Requests, "")
				return nil
			})
			if item.disabled {
				ctx = WithoutHostInput(ctx)
			}
			receiver, err := newSubscriptionReceiver(ctx, json.RawMessage(`1`), json.RawMessage(`{"taskIds":["job / α"]}`), InputSupport{Form: true})
			require.NoError(t, err)
			require.NoError(t, receiver.acknowledge(SubscriptionFilter{TaskIDs: []string{taskPeerID}}))
			message := rpcMessage{Method: string(SubscriptionTaskChanged), Params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/subscriptionId":1},"taskId":"job / α","status":"input_required","createdAt":"date","lastUpdatedAt":"date","ttlMs":null,"inputRequests":{"":{"method":"elicitation/create","params":{"mode":"form","message":"value?","requestedSchema":{"type":"object","properties":{}}}}}}`)}
			err = receiver.notification(ctx, message)
			if item.disabled {
				require.ErrorContains(t, err, "host input is disabled")
				assert.Zero(t, delivered)
			} else {
				require.NoError(t, err)
				assert.Equal(t, 1, delivered)
			}
		})
	}
}

func TestHTTPRequestTaskSubscriptionsRequireCapability(t *testing.T) {
	for _, item := range []struct {
		name, capabilities, filter string
		code                       int
	}{
		{"task capability", `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`, `{"taskIds":["job"]}`, 0},
		{"missing capability", `{}`, `{"taskIds":["job"]}`, MissingRequiredClientCapability},
		{"other extension", `{"extensions":{"example.test/other":{}}}`, `{"taskIds":["job"]}`, MissingRequiredClientCapability},
		{"malformed extensions", `{"extensions":null}`, `{"taskIds":["job"]}`, JSONRPCInvalidParams},
		{"malformed Tasks", `{"extensions":{"io.modelcontextprotocol/tasks":true}}`, `{"taskIds":["job"]}`, JSONRPCInvalidParams},
		{"resource-only listener", `{}`, `{"resourceSubscriptions":["file:///document"]}`, 0},
		{"empty task selection", `{}`, `{"taskIds":[]}`, 0},
	} {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://tasks.example/mcp", nil)
			request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
			request.Header.Set("Mcp-Method", methodSubscriptionsListen)
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":` + item.filter + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"` + ProtocolVersion + `","io.modelcontextprotocol/clientCapabilities":` + item.capabilities + `}}}`)
			failure := ValidateHTTPRequest(request, body, nil)
			if item.code == 0 {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, item.code, failure.Code)
			if item.code == MissingRequiredClientCapability {
				assert.JSONEq(t, `{"requiredCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`, string(failure.Data))
			}
		})
	}
}
