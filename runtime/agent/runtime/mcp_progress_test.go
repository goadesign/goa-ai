// These tests run the actual tool activity through an MCP HTTP peer. Work
// updates reach only the selected host stream; the result remains terminal.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	progressStreamSink struct {
		recordingStreamSink
		failure error
	}
)

func (s *progressStreamSink) Send(ctx context.Context, event stream.Event) error {
	if event.Type() == stream.EventToolProgress && s.failure != nil {
		return s.failure
	}
	return s.recordingStreamSink.Send(ctx, event)
}

func TestMCPProgressActivityHostVisibility(t *testing.T) {
	for _, test := range []struct {
		name                   string
		enabled, session, fail bool
	}{
		{"host receives work", true, true, false}, {"profile hides work", false, true, false}, {"sessionless call", true, false, false}, {"host failure stops operation", true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			hostFailure := errors.New("host stream unavailable")
			sink := &progressStreamSink{}
			if test.fail {
				sink.failure = hostFailure
			}
			rt := New(newTestStore(), WithStream(sink, stream.StreamProfile{ToolStart: true, ToolProgress: test.enabled}))
			spec := newAnyJSONSpec("remote.work")
			seedTestToolset(rt, "remote", spec)
			serverErrors := make(chan error, 1)
			received := make(chan json.RawMessage, 1)
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     json.RawMessage
					Params json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					serverErrors <- err
					return
				}
				received <- request.Params
				serverErrors <- mcp.ServeProgress(w, r, request.Params, func(w http.ResponseWriter, r *http.Request) {
					if err := mcp.ReportProgress(r.Context(), 12.5, new(float64(25)), new("Halfway")); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					// A progress notification is observable before any final tool result.
					events, err := rt.ListRunEvents(r.Context(), "run-1", "", 100)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					if countRunEventsByType(events, hooks.ToolResultReceived) != 0 {
						http.Error(w, "progress created a durable result", 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					result := struct {
						JSONRPC string          `json:"jsonrpc"`
						ID      json.RawMessage `json:"id"`
						Result  json.RawMessage `json:"result"`
					}{"2.0", request.ID, json.RawMessage(`{"resultType":"complete","content":[],"structuredContent":{"done":true}}`)}
					if err := json.NewEncoder(w).Encode(result); err != nil {
						http.Error(w, err.Error(), 500)
					}
				})
			}))
			defer peer.Close()
			transport := mcp.NewHTTPTransport(peer.Client(), mcp.ClientInfo{}, mcp.HTTPBindings{}, mcp.InputSupport{}, mcp.HTTPRetryPolicy{})
			rt.toolsets["remote"] = ToolsetRegistration{Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
				reply, err := transport.CallTool(ctx, peer.URL, mcp.CallRequest{Tool: "work", Payload: json.RawMessage(call.Payload)})
				if err != nil {
					return nil, err
				}
				decoded, err := spec.Result.Codec.FromJSON(reply.StructuredContent)
				if err != nil {
					return nil, err
				}
				return Executed(&planner.ToolResult{Name: call.Name, Result: decoded}), nil
			}}
			sessionID := ""
			if test.session {
				sessionID = "session-1"
			}
			input := &RunInput{AgentID: "test.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
			seedRunMeta(t, rt, input)
			result, err := rt.ExecuteToolActivity(t.Context(), &ToolInput{RunID: input.RunID, AgentID: input.AgentID, SessionID: sessionID, TurnID: input.TurnID, ToolsetName: "remote", ToolName: tools.Ident("remote.work"), ToolCallID: "call-1", ParentToolCallID: "parent-1", Payload: rawjson.Message(`{}`)})
			if test.fail {
				require.ErrorIs(t, err, hostFailure)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, `{"done":true}`, string(result.Payload))
				require.NoError(t, <-serverErrors)
			}
			params := <-received
			events := sink.snapshot()
			if test.enabled && test.session && !test.fail {
				require.Len(t, events, 1)
				update, ok := events[0].(stream.ToolProgress)
				require.True(t, ok)
				assert.Equal(t, stream.EventToolProgress, update.Type())
				assert.Equal(t, "run-1", update.RunID())
				assert.Equal(t, "session-1", update.SessionID())
				assert.Equal(t, "call-1", update.Data.ToolCallID)
				assert.Equal(t, "parent-1", update.Data.ParentToolCallID)
				assert.Equal(t, "remote.work", update.Data.ToolName)
				assert.True(t, json.Valid(update.Data.RequestID))
				assert.InDelta(t, 12.5, update.Data.Value, 0)
				assert.Equal(t, new(float64(25)), update.Data.Total)
				assert.Equal(t, new("Halfway"), update.Data.Message)
			} else {
				assert.Empty(t, events)
			}
			if test.session {
				assert.Contains(t, string(params), "progressToken")
			} else {
				assert.NotContains(t, string(params), "progressToken")
			}
		})
	}
}
