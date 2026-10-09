// These checks send the same encoded task observation through subscriptions.
// The request-owned writer must preserve the full state and exact JSON values,
// require accepted task identities, and retain ownership of correlation metadata.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskNotificationProducerFullSnapshots(t *testing.T) {
	for _, tc := range []struct{ status, extra string }{
		{"working", ""},
		{"input_required", `,"inputRequests":{}`},
		{"completed", `,"result":{"resultType":"complete","content":[],"structuredContent":{"integer":9007199254740993},"isError":true}`},
		{"failed", `,"error":{"code":-32603,"message":"job failed","data":{"integer":9007199254740993}}`},
		{"cancelled", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			snapshot := json.RawMessage(`{"resultType":"complete",` + strings.Replace(taskPeerMetadata, `"status":"working"`, `"status":"`+tc.status+`"`, 1) + tc.extra + `,"_meta":{"example.test/value":9007199254740993}}`)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://task.test", nil)
			request.Header.Set("Accept", "text/event-stream")
			writer := httptest.NewRecorder()
			err := ServeSubscriptions(writer, request, json.RawMessage(`9007199254740993`), json.RawMessage(`{"notifications":{"taskIds":["job / α"]}}`), func(w http.ResponseWriter, r *http.Request) {
				if !assert.NoError(t, AcknowledgeSubscription(r.Context(), SubscriptionFilter{TaskIDs: []string{taskPeerID}})) {
					return
				}
				if !assert.NoError(t, ReportTaskChanged(r.Context(), snapshot)) {
					return
				}
				_, err := fmt.Fprint(w, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"resultType":"complete"}}`)
				assert.NoError(t, err)
			})
			require.NoError(t, err)
			var events []rpcMessage
			_, err = readEventStream(strings.NewReader(writer.Body.String()), func(message rpcMessage) error { events = append(events, message); return nil })
			require.NoError(t, err)
			require.Len(t, events, 2)
			assert.Equal(t, string(SubscriptionTaskChanged), events[1].Method)
			var expected, actual map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(snapshot, &expected))
			require.NoError(t, json.Unmarshal(events[1].Params, &actual))
			delete(expected, "resultType")
			var meta map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(actual["_meta"], &meta))
			assert.Equal(t, "9007199254740993", string(meta[subscriptionIDKey]))
			delete(meta, subscriptionIDKey)
			actual["_meta"], err = json.Marshal(meta)
			require.NoError(t, err)
			assert.Equal(t, expected, actual)
			var observed taskGetResult
			require.NoError(t, json.Unmarshal(snapshot, &observed))
			assert.Equal(t, TaskStatus(tc.status), observed.task.Info().Status)
		})
	}
}

func TestTaskNotificationProducerBoundaries(t *testing.T) {
	valid := `{"resultType":"complete",` + taskPeerMetadata + `}`
	for _, tc := range []struct {
		name, snapshot, failure string
		ack                     bool
	}{
		{"before acknowledgment", valid, "before acknowledgment", false},
		{"unaccepted identity", strings.Replace(valid, taskPeerID, "another job", 1), "not acknowledged", true},
		{"missing snapshot", `null`, "requires", true},
		{"unfinished response", strings.Replace(valid, `"complete"`, `"input_required"`, 1), "requires resultType complete", true},
		{"missing retention", strings.Replace(valid, `,"ttlMs":null`, "", 1), "ttlMs", true},
		{"malformed completed result", strings.Replace(valid, `"working"`, `"completed"`, 1), "completed task", true},
		{"invalid metadata", strings.TrimSuffix(valid, "}") + `,"_meta":null}`, "_meta", true},
		{"source-owned correlation", strings.TrimSuffix(valid, "}") + `,"_meta":{"io.modelcontextprotocol/subscriptionId":1}}`, "ID ownership", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://task.test", nil)
			request.Header.Set("Accept", "text/event-stream")
			writer := httptest.NewRecorder()
			err := ServeSubscriptions(writer, request, json.RawMessage(`1`), json.RawMessage(`{"notifications":{"taskIds":["job / α"]}}`), func(w http.ResponseWriter, r *http.Request) {
				if tc.ack {
					if !assert.NoError(t, AcknowledgeSubscription(r.Context(), SubscriptionFilter{TaskIDs: []string{taskPeerID}})) {
						return
					}
				}
				assert.ErrorContains(t, ReportTaskChanged(r.Context(), json.RawMessage(tc.snapshot)), tc.failure)
				_, err := fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete"}}`)
				assert.NoError(t, err)
			})
			if tc.ack {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "requires acknowledgment")
			}
			assert.NotContains(t, writer.Body.String(), `"method":"notifications/tasks"`)
		})
	}
	assert.ErrorContains(t, ReportTaskChanged(t.Context(), json.RawMessage(valid)), "active listen")
}

func TestTaskCapabilityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, meta string
		code       int
	}{
		{"supported", `{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`, 0},
		{"open extension settings", `{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{"future":true}}}}`, 0},
		{"missing declaration", `{"io.modelcontextprotocol/clientCapabilities":{}}`, MissingRequiredClientCapability},
		{"missing task extension", `{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"other":{}}}}`, MissingRequiredClientCapability},
		{"missing metadata", ``, JSONRPCInvalidParams},
		{"null metadata", `null`, JSONRPCInvalidParams},
		{"missing capabilities", `{}`, JSONRPCInvalidParams},
		{"null capabilities", `{"io.modelcontextprotocol/clientCapabilities":null}`, JSONRPCInvalidParams},
		{"null extensions", `{"io.modelcontextprotocol/clientCapabilities":{"extensions":null}}`, JSONRPCInvalidParams},
		{"scalar extension", `{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":true}}}`, JSONRPCInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := ValidateTaskCapabilities(json.RawMessage(tc.meta))
			if tc.code == 0 {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, tc.code, failure.Code)
			if tc.code == MissingRequiredClientCapability {
				assert.JSONEq(t, `{"requiredCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`, string(failure.Data))
			}
		})
	}
}
