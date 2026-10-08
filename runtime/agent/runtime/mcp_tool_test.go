// These tests use a synthetic MCP peer to prove that the shared executor saves
// Task handles and acknowledgments before another network request. Completed
// output uses the original codec; Task errors cannot correct tool arguments.
package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

func TestExecuteMCPToolTaskOperationsStaySeparate(t *testing.T) {
	for _, mode := range []string{"working", "input_required", "completed", "tool_error", "failed", "cancelled", "update", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var message struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
					t.Error(err)
					return
				}
				methods = append(methods, message.Method)
				var result string
				metadata := `"taskId":"","createdAt":"2026-10-07T12:00:00Z","lastUpdatedAt":"2026-10-07T12:00:01Z","ttlMs":null,"pollIntervalMs":3`
				switch message.Method {
				case "tools/list":
					result = `{"resultType":"complete","tools":[{"name":"lookup","inputSchema":{"type":"object"}}],"ttlMs":0,"cacheScope":"private"}`
				case "tools/call":
					result = `{"resultType":"task","status":"working",` + metadata + `}`
				case "tasks/update", "tasks/cancel":
					result = `{"resultType":"complete"}`
				case "tasks/get":
					status := mode
					additional := ""
					switch mode {
					case "input_required":
						additional = `,"inputRequests":{"":{"method":"elicitation/create","params":{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}}}`
					case "completed", "tool_error":
						status = "completed"
						additional = `,"result":{"resultType":"complete","content":[{"type":"text","text":"answer"}],"structuredContent":{"answer":42}}`
						if mode == "tool_error" {
							additional = `,"result":{"resultType":"complete","content":[{"type":"text","text":"rejected"}],"isError":true}`
						}
					case "failed":
						additional = `,"error":{"code":-32602,"message":"Task execution rejected input"}`
					}
					result = `{"resultType":"complete","status":"` + status + `",` + metadata + additional + `}`
				default:
					t.Errorf("unexpected method %q", message.Method)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				if _, err := fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, message.ID, result); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: mcp.ClientInfo{Name: "operation-host", Version: "1"}, InputSupport: mcp.InputSupport{Form: true}})
			require.NoError(t, err)
			decoded := 0
			spec := newAnyJSONSpec(tools.Ident("remote.lookup"))
			spec.Result.Codec.FromJSON = func(raw []byte) (any, error) {
				decoded++
				assert.JSONEq(t, `{"answer":42}`, string(raw))
				return "decoded with original codec", nil
			}
			call := &ToolCall{Name: spec.Name, Payload: []byte(`{"query":"original"}`)}
			ctx := mcp.WithTaskSupport(t.Context())
			created, err := ExecuteMCPTool(ctx, caller, call, "lookup", spec)
			require.NoError(t, err)
			require.NotNil(t, created.mcpPending)
			id, hint, ok := created.mcpPending.AsTaskWait()
			require.True(t, ok)
			assert.Empty(t, id)
			assert.EqualValues(t, 3, *hint)
			assert.Equal(t, []string{"tools/list", "tools/call"}, methods)
			assert.Zero(t, decoded)
			call.ExecutionSequence = 1
			switch mode {
			case "update":
				call.ExecutionContinuation, err = tooloperation.NewTaskUpdate(id, map[string]json.RawMessage{})
			case "cancel":
				call.ExecutionContinuation, err = tooloperation.NewTaskCancel(id)
			default:
				call.ExecutionContinuation, err = tooloperation.NewTaskGet(id)
			}
			require.NoError(t, err)
			out, err := ExecuteMCPTool(ctx, caller, call, "lookup", spec)
			require.NoError(t, err)
			switch mode {
			case "working", "update", "cancel":
				require.NotNil(t, out.mcpPending)
				_, _, ok := out.mcpPending.AsTaskWait()
				assert.True(t, ok)
				assert.Nil(t, out.ToolResult)
			case "input_required":
				require.NotNil(t, out.mcpPending)
				_, _, input, ok := out.mcpPending.AsTaskInput()
				require.True(t, ok)
				assert.Contains(t, input.Requests, "")
				assert.Nil(t, out.ToolResult)
			case "completed":
				require.NotNil(t, out.ToolResult)
				assert.Nil(t, out.mcpPending)
				assert.Equal(t, "decoded with original codec", out.ToolResult.Result)
				assert.Equal(t, 1, decoded)
			case "tool_error", "failed", "cancelled":
				require.NotNil(t, out.ToolResult.Failure)
				assert.Nil(t, out.mcpPending)
				if mode == "tool_error" {
					assert.Equal(t, planner.FailureDomainRejection, out.ToolResult.Failure.Kind)
				} else {
					assert.Equal(t, planner.RecoveryFinish, out.ToolResult.Failure.Recovery.Action)
					assert.NotEqual(t, planner.FailureInvalidCall, out.ToolResult.Failure.Kind)
				}
			}
			assert.Len(t, methods, 3, "creation, observation and acknowledgment must be separate activities")
			assert.Equal(t, "tools/call", methods[1])
			assert.NotEqual(t, "tools/call", methods[2])
		})
	}
}
