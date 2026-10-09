// Task transport tests prove that repeated reads and cancellation stay separate
// from the creating tool and uncertain answer submission. The workflow retains
// accepted identity and exact timer guidance while an endpoint is unavailable.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/mcp"
)

func TestMCPTaskDeliveryClassifiesExactFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"rate limit", &mcp.HTTPResponseError{StatusCode: 429}, true},
		{"unavailable", &mcp.HTTPResponseError{StatusCode: 503}, true},
		{"authorization", &mcp.HTTPResponseError{StatusCode: 401}, false},
		{"missing endpoint", &mcp.HTTPResponseError{StatusCode: 404}, false},
		{"connection reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"lost response", io.ErrUnexpectedEOF, true},
		{"attempt deadline", context.DeadlineExceeded, true},
		{"cancelled", context.Canceled, false},
		{"invalid response", mcp.NewMalformedResponseError(io.EOF), false},
		{"local bug", mcp.NewInternalError(io.EOF), false},
		{"protocol rejection", &mcp.Error{Code: mcp.JSONRPCInternalError, Message: "rejected"}, false},
		{"unknown error", errors.New("unknown failure"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.retry, temporaryMCPTaskDelivery(test.err))
		})
	}
}

func TestMCPTaskHTTPDeliveryPolicy(t *testing.T) {
	for _, method := range []string{"tools/call", "tasks/get", "tasks/update", "tasks/cancel"} {
		t.Run(method, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
					return
				}
				if request.Method == "tools/list" {
					w.Header().Set("Content-Type", "application/json")
					_, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","ttlMs":0,"cacheScope":"private","tools":[{"name":"lookup","inputSchema":{"type":"object"}}]}}`, request.ID)
					assert.NoError(t, err)
					return
				}
				assert.Equal(t, method, request.Method)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(peer.Close)
			caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint: peer.URL, Client: peer.Client(), ClientInfo: mcp.ClientInfo{Name: "delivery-test", Version: "1"}})
			require.NoError(t, err)
			spec := newAnyJSONSpec("remote.lookup")
			call := &ToolCall{Name: spec.Name, Payload: []byte(`{}`)}
			switch method {
			case "tasks/get":
				call.ExecutionContinuation, err = tooloperation.NewTaskGet("")
			case "tasks/update":
				call.ExecutionContinuation, err = tooloperation.NewTaskUpdate("", map[string]json.RawMessage{})
			case "tasks/cancel":
				call.ExecutionContinuation, err = tooloperation.NewTaskCancel("")
			}
			require.NoError(t, err)
			if call.ExecutionContinuation != nil {
				call.ExecutionSequence = 1
			}
			out, err := ExecuteMCPTool(t.Context(), caller, call, "lookup", spec)
			if method == "tools/call" {
				require.NoError(t, err)
				require.NotNil(t, out.ToolResult.Failure)
				assert.Equal(t, planner.RecoveryFinish, out.ToolResult.Failure.Recovery.Action)
			} else {
				require.Error(t, err)
				assert.Nil(t, out)
				assert.Equal(t, method == "tasks/update", engine.IsActivityErrorNonRetryable(err))
			}
		})
	}
}

func TestTaskReadOutageKeepsLastPollingGuidance(t *testing.T) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	rt := New(newTestStore())
	seedTestToolset(rt, "remote.tools", spec)
	hint := int64(37)
	var creates, reads, cancels int
	root := &routeWorkflowContext{ctx: t.Context(), hookRuntime: rt, toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){}}
	root.toolRoutes["execute"] = func(_ context.Context, input *ToolInput) (*ToolOutput, error) {
		if operation := input.ExecutionContinuation; operation != nil {
			if _, reading := operation.AsTaskGet(); reading {
				reads++
				assert.EqualValues(t, reads, input.ExecutionSequence)
				if reads == 1 {
					return nil, io.ErrUnexpectedEOF
				}
				return &ToolOutput{Payload: []byte(`{"answer":42}`)}, nil
			}
			cancels++
			t.Error("a temporary read failure must retain the Task")
		}
		creates++
		pending, err := tooloperation.NewPendingTaskWait("", &hint)
		require.NoError(t, err)
		return &ToolOutput{PendingExecution: pending}, nil
	}
	wf := newTaskTimerWorkflow(root)
	runCtx := run.Context{RunID: "read-outage", SessionID: "session-task", TurnID: "turn-task"}
	results, timedOut, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}}, "test.agent", &runCtx, testToolHistory(t, rt, "test.agent", runCtx, nil), []ToolCall{{Name: spec.Name, ToolCallID: "task-call", Payload: []byte(`{}`)}}, 0, nil, time.Time{}, nil)
	require.NoError(t, err)
	assert.False(t, timedOut)
	require.Len(t, results, 1)
	assert.Nil(t, results[0].ToolResult.Failure)
	assert.Equal(t, []time.Duration{37 * time.Millisecond, 37 * time.Millisecond}, wf.durations)
	assert.Equal(t, 1, creates)
	assert.Equal(t, 2, reads)
	assert.Zero(t, cancels)
	for _, options := range wf.options[1:] {
		assert.Equal(t, 1, options.RetryPolicy.MaxAttempts)
		assert.False(t, options.RetryPolicy.UnlimitedAttempts)
	}
}

func TestTaskCancellationDeliveryWaitsAcrossFailedActivities(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint(permanent), func(t *testing.T) {
			spec := newAnyJSONSpec("remote.tools.lookup")
			rt := New(newTestStore())
			seedTestToolset(rt, "remote.tools", spec)
			var attempts int
			root := &routeWorkflowContext{ctx: t.Context(), hookRuntime: rt, toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){}}
			root.toolRoutes["execute"] = func(_ context.Context, input *ToolInput) (*ToolOutput, error) {
				attempts++
				id, cancelling := input.ExecutionContinuation.AsTaskCancel()
				assert.True(t, cancelling)
				assert.Empty(t, id)
				assert.EqualValues(t, 8, input.ExecutionSequence)
				if permanent {
					return nil, engine.MarkActivityErrorNonRetryable(errors.New("authorization rejected"))
				}
				if attempts <= 2 {
					return nil, io.ErrUnexpectedEOF
				}
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				require.NoError(t, err)
				return &ToolOutput{PendingExecution: pending}, nil
			}
			wf := newTaskTimerWorkflow(root)
			execution := &toolBatchExec{r: rt, agentID: "test.agent", runID: "cancel-delivery", activityName: "execute", finishBy: time.Now().Add(-time.Second), toolActOptions: engine.ActivityOptions{StartToCloseTimeout: time.Minute, ScheduleToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}}, ownedTasks: map[string]futureInfo{}}
			info := futureInfo{call: ToolCall{Name: spec.Name, ToolCallID: "task-call", Payload: []byte(`{}`), ExecutionSequence: 7}, task: &taskExecution{TaskID: ""}}
			err := execution.cancelAcceptedTask(wf, info)
			if permanent {
				require.ErrorContains(t, err, "authorization rejected")
				assert.Equal(t, 1, attempts)
				assert.Empty(t, wf.durations)
				assert.Contains(t, execution.ownedTasks, "task-call")
			} else {
				require.NoError(t, err)
				assert.Equal(t, 3, attempts)
				assert.Equal(t, []time.Duration{time.Second, time.Second}, wf.durations)
				assert.Empty(t, execution.ownedTasks)
			}
			for _, options := range wf.options {
				assert.Equal(t, 1, options.RetryPolicy.MaxAttempts)
				assert.False(t, options.RetryPolicy.UnlimitedAttempts)
				assert.Equal(t, time.Minute, options.StartToCloseTimeout)
				assert.Equal(t, time.Minute, options.ScheduleToCloseTimeout)
			}
		})
	}
}

func TestTaskObservedTerminalFailureNeedsNoCancellation(t *testing.T) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	rt := New(newTestStore())
	seedTestToolset(rt, "remote.tools", spec)
	var creates, reads int
	root := &routeWorkflowContext{ctx: t.Context(), hookRuntime: rt, toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){}}
	root.toolRoutes["execute"] = func(_ context.Context, input *ToolInput) (*ToolOutput, error) {
		if operation := input.ExecutionContinuation; operation != nil {
			_, reading := operation.AsTaskGet()
			assert.True(t, reading, "a terminal Task must not receive cancellation")
			reads++
			return &ToolOutput{Failure: mcpTaskFailure(spec.Name, &mcp.Error{Code: mcp.JSONRPCInternalError, Message: "Task failed"}).Failure}, nil
		}
		creates++
		zero := int64(0)
		pending, err := tooloperation.NewPendingTaskWait("", &zero)
		require.NoError(t, err)
		return &ToolOutput{PendingExecution: pending}, nil
	}
	wf := newTaskTimerWorkflow(root)
	runCtx := run.Context{RunID: "terminal-task", SessionID: "session-task", TurnID: "turn-task"}
	results, timedOut, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, "test.agent", &runCtx, testToolHistory(t, rt, "test.agent", runCtx, nil), []ToolCall{{Name: spec.Name, ToolCallID: "task-call", Payload: []byte(`{}`)}}, 0, nil, time.Time{}, nil)
	require.NoError(t, err)
	assert.False(t, timedOut)
	require.Len(t, results, 1)
	assert.Equal(t, planner.RecoveryFinish, results[0].ToolResult.Failure.Recovery.Action)
	assert.Equal(t, 1, creates)
	assert.Equal(t, 1, reads)
}
