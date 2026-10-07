// These checks use a synthetic peer to verify the released Task wire contract
// through HTTP and a real stdio process. The peer counts tool creation separately
// from state queries, input updates and cooperative cancellation.
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/mcpprotocol"
)

type (
	taskTestClient interface {
		Caller
		GetTask(context.Context, string) (Task, error)
		UpdateTask(context.Context, string, map[string]json.RawMessage) error
		CancelTask(context.Context, string) error
	}
	taskPeer struct {
		mu                              sync.Mutex
		starts, reads, updates, cancels int
	}
)

const taskPeerID = "job / α"
const taskPeerMetadata = `"taskId":"job / α","status":"working","createdAt":"2026-10-07T12:00:00Z","lastUpdatedAt":"2026-10-07T12:00:01Z","ttlMs":null,"pollIntervalMs":3.0`

func TestTaskHTTPClientLifecycle(t *testing.T) {
	peer := new(taskPeer)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		var request struct {
			Method string
			Params map[string]json.RawMessage
		}
		if !assert.NoError(t, json.Unmarshal(raw, &request)) {
			return
		}
		assert.Equal(t, request.Method, r.Header.Get("Mcp-Method"))
		assert.Equal(t, ProtocolVersion, r.Header.Get("MCP-Protocol-Version"))
		if strings.HasPrefix(request.Method, "tasks/") {
			assert.Equal(t, mcpprotocol.EncodeHeaderValue(taskPeerID), r.Header.Get("Mcp-Name"))
		}
		response, err := peer.response(raw)
		if !assert.NoError(t, err) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write(response)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "task-host", Version: "1"}, InputSupport: InputSupport{Form: true}})
	require.NoError(t, err)
	verifyTaskLifecycle(t, caller)
	peer.mu.Lock()
	defer peer.mu.Unlock()
	assert.Equal(t, 1, peer.starts, "task operations must not repeat tools/call")
	assert.Equal(t, 1, peer.updates)
	assert.Equal(t, 1, peer.cancels)
}

func TestTaskStdioClientLifecycle(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestTaskProtocolPeer$"}, Env: []string{"GOA_AI_MCP_TASK_PEER=1"}, ClientInfo: ClientInfo{Name: "task-host", Version: "1"}, InputSupport: InputSupport{Form: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	verifyTaskLifecycle(t, caller)
}

func TestTaskProtocolPeer(_ *testing.T) {
	if os.Getenv("GOA_AI_MCP_TASK_PEER") != "1" {
		return
	}
	peer := new(taskPeer)
	reader := bufio.NewReader(os.Stdin)
	for {
		raw, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			os.Exit(0)
		}
		if err != nil {
			os.Exit(2)
		}
		response, err := peer.response(raw)
		if err != nil {
			if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
				os.Exit(3)
			}
			os.Exit(2)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(response)); err != nil {
			os.Exit(3)
		}
	}
}

func TestTaskInfoNullableIntegerPresence(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		valid        bool
		null         bool
		value        int64
	}{
		{"unlimited", `"ttlMs":null`, true, true, 0},
		{"zero", `"ttlMs":0`, true, false, 0},
		{"exact whole decimal", `"ttlMs":9007199254740993.0`, true, false, 9007199254740993},
		{"fraction", `"ttlMs":3.5`, false, false, 0},
		{"string", `"ttlMs":"3"`, false, false, 0},
		{"absent", `"unused":null`, false, false, 0},
		{"overflow", `"ttlMs":9223372036854775808`, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := decodeTaskInfo([]byte("{" + strings.Replace(taskPeerMetadata, `"ttlMs":null`, tc.change, 1) + "}"))
			if !tc.valid {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.null {
				assert.Nil(t, info.TTLMs)
			} else {
				require.NotNil(t, info.TTLMs)
				assert.Equal(t, tc.value, *info.TTLMs)
			}
			require.NotNil(t, info.PollIntervalMs)
			assert.Equal(t, int64(3), *info.PollIntervalMs)
			raw, err := json.Marshal(info)
			require.NoError(t, err)
			assert.Contains(t, string(raw), `"ttlMs":`)
		})
	}
}

func TestTaskInfoPreservesTimestampStrings(t *testing.T) {
	raw := strings.Replace(taskPeerMetadata, "2026-10-07T12:00:00Z", "20261007T120000Z", 1)
	info, err := decodeTaskInfo([]byte("{" + raw + "}"))
	require.NoError(t, err)
	assert.Equal(t, "20261007T120000Z", info.CreatedAt)
}

func TestTaskStatesAndMalformedReplies(t *testing.T) {
	for _, tc := range []struct {
		status, extra string
		valid         bool
	}{
		{"working", "", true}, {"cancelled", "", true},
		{"input_required", `,"inputRequests":{}`, true},
		{"input_required", `,"inputRequests":null`, false},
		{"input_required", "", false},
		{"completed", `,"result":{"resultType":"complete","content":[]}`, true},
		{"completed", `,"result":{"resultType":"complete","content":[],"isError":true,"structuredContent":null}`, true},
		{"completed", `,"result":{"resultType":"input_required","inputRequests":{}}`, false},
		{"completed", `,"result":null`, false}, {"completed", "", false},
		{"failed", `,"error":{"code":-32603,"message":"owner failed","data":null}`, true},
		{"failed", `,"error":{"code":0,"message":""}`, true},
		{"failed", `,"error":{"message":"missing code"}`, false},
		{"failed", `,"error":null`, false}, {"failed", "", false},
		{"unknown", "", false},
	} {
		t.Run(tc.status+tc.extra, func(t *testing.T) {
			raw := `{"resultType":"complete",` + strings.Replace(taskPeerMetadata, `"status":"working"`, `"status":"`+tc.status+`"`, 1) + tc.extra + "}"
			var result taskGetResult
			err := json.Unmarshal([]byte(raw), &result)
			if !tc.valid {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, TaskStatus(tc.status), result.task.Info().Status)
			if strings.Contains(tc.extra, `"isError":true`) {
				response, ok := result.task.AsCompleted()
				require.True(t, ok)
				assert.True(t, response.IsError)
				assert.Equal(t, "null", string(response.StructuredContent))
			}
		})
	}
	for _, change := range []struct{ old, new string }{
		{`"pollIntervalMs":3.0`, `"pollIntervalMs":null`},
		{`"createdAt":"2026-10-07T12:00:00Z"`, `"createdAt":100`},
		{`"lastUpdatedAt":"2026-10-07T12:00:01Z"`, `"lastUpdatedAt":null`},
		{`"taskId":"job / α"`, `"taskId":null`},
		{`"status":"working"`, `"status":"working","status":"completed"`},
	} {
		_, err := decodeTaskInfo([]byte("{" + strings.Replace(taskPeerMetadata, change.old, change.new, 1) + "}"))
		assert.Error(t, err)
	}
}

func TestTaskCreationRequiresAdvertisedSupport(t *testing.T) {
	var result toolsCallResult
	require.NoError(t, json.Unmarshal([]byte(`{"resultType":"task",`+taskPeerMetadata+"}"), &result))
	_, err := normalizeCallResult(t.Context(), result, InputSupport{})
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	response, err := normalizeCallResult(WithTaskSupport(t.Context()), result, InputSupport{})
	require.NoError(t, err)
	require.NotNil(t, response.Task)
	assert.Equal(t, taskPeerID, response.Task.TaskID)
	assert.Empty(t, response.StructuredContent)
}

// verifyTaskLifecycle observes one created task, answers a subset of its input,
// tolerates an unchanged observation, then checks the cancellation/completion race.
func verifyTaskLifecycle(t *testing.T, caller taskTestClient) {
	t.Helper()
	response, err := caller.CallTool(WithTaskSupport(t.Context()), CallRequest{Tool: "read", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.NotNil(t, response.Task)
	assert.Equal(t, taskPeerID, response.Task.TaskID)
	task, err := caller.GetTask(t.Context(), taskPeerID)
	require.NoError(t, err)
	assert.Equal(t, TaskWorking, task.Info().Status)
	task, err = caller.GetTask(t.Context(), taskPeerID)
	require.NoError(t, err)
	input, ok := task.AsInputRequired()
	require.True(t, ok)
	require.Len(t, input.Requests, 2)
	require.NoError(t, caller.UpdateTask(t.Context(), taskPeerID, map[string]json.RawMessage{"profile:1": json.RawMessage(`{"action":"accept","content":{"name":"example"}}`)}))
	task, err = caller.GetTask(t.Context(), taskPeerID)
	require.NoError(t, err)
	assert.Equal(t, TaskInputRequired, task.Info().Status, "an acknowledgement does not prove the state already changed")
	require.NoError(t, caller.CancelTask(t.Context(), taskPeerID))
	task, err = caller.GetTask(t.Context(), taskPeerID)
	require.NoError(t, err)
	complete, ok := task.AsCompleted()
	require.True(t, ok)
	assert.Equal(t, `"finished"`, string(complete.StructuredContent))
	require.NoError(t, caller.UpdateTask(t.Context(), taskPeerID, map[string]json.RawMessage{}))
	assert.Error(t, caller.UpdateTask(t.Context(), taskPeerID, nil))
}

// response implements the released flat Task creation and current-state shapes.
// Its counters distinguish a new tool execution from later observations and input.
func (p *taskPeer) response(raw []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var request struct {
		ID     json.RawMessage
		Method string
		Params map[string]json.RawMessage
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(request.Params["_meta"], &meta); err != nil {
		return nil, err
	}
	var capabilities struct{ Extensions map[string]json.RawMessage }
	if err := json.Unmarshal(meta[clientCapabilitiesKey], &capabilities); err != nil {
		return nil, err
	}
	result := `{"resultType":"complete"}`
	switch request.Method {
	case "tools/list":
		result = `{"resultType":"complete","tools":[{"name":"read","inputSchema":{"type":"object"},"outputSchema":{"type":"string"}}],"ttlMs":0,"cacheScope":"private"}`
	case methodToolsCall:
		if capabilities.Extensions[tasksExtension] == nil {
			return nil, errors.New("task creation capability was absent")
		}
		p.starts++
		if p.starts != 1 {
			return nil, errors.New("task operations repeated the original tool call")
		}
		result = `{"resultType":"task",` + taskPeerMetadata + "}"
	case methodTasksGet, methodTasksUpdate, methodTasksCancel:
		if capabilities.Extensions[tasksExtension] == nil {
			return nil, errors.New("task operation capability was absent")
		}
		var taskID string
		if err := json.Unmarshal(request.Params["taskId"], &taskID); err != nil || taskID != taskPeerID {
			return nil, errors.New("task ID changed")
		}
		switch request.Method {
		case methodTasksGet:
			p.reads++
			result = `{"resultType":"complete",` + taskPeerMetadata + "}"
			if p.reads == 2 || p.reads == 3 {
				result = strings.Replace(result, `"status":"working"`, `"status":"input_required"`, 1)
				result = strings.TrimSuffix(result, "}") + `,"inputRequests":{"profile:1":{"method":"elicitation/create","params":{"mode":"form","message":"Select a name","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}},"profile:2":{"method":"elicitation/create","params":{"mode":"form","message":"Select another name","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}}}}}}`
			} else if p.reads >= 4 {
				result = strings.Replace(result, `"status":"working"`, `"status":"completed"`, 1)
				result = strings.TrimSuffix(result, "}") + `,"result":{"resultType":"complete","content":[],"structuredContent":"finished"}}`
			}
		case methodTasksUpdate:
			var answers map[string]json.RawMessage
			if err := json.Unmarshal(request.Params["inputResponses"], &answers); err != nil || answers == nil {
				return nil, errors.New("task update needs an answer object")
			}
			if len(answers) > 0 {
				p.updates++
			}
		case methodTasksCancel:
			p.cancels++
		}
	default:
		return nil, fmt.Errorf("unexpected task method %q", request.Method)
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": json.RawMessage(result)})
}

func TestTaskAcknowledgmentRejectsMalformedFields(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"resultType":null}`, `{"ResultType":"complete"}`, `{"resultType":"task"}`, `{"resultType":"input_required"}`, `{"resultType":"complete","_meta":null}`, `{"resultType":"complete","_meta":[]}`} {
		t.Run(raw, func(t *testing.T) {
			var result taskAcknowledgment
			err := json.Unmarshal([]byte(raw), &result)
			if err == nil {
				err = result.validate()
			}
			assert.Error(t, err)
		})
	}
}

func TestTaskObservationChecksIdentityAndHostSupport(t *testing.T) {
	var result taskGetResult
	raw := `{"resultType":"complete",` + strings.Replace(taskPeerMetadata, `"status":"working"`, `"status":"input_required"`, 1) + `,"inputRequests":{"consent:1":{"method":"elicitation/create","params":{"mode":"url","message":"Approve","url":"https://consent.example"}}}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &result))
	_, err := normalizeTask(t.Context(), "another-task", result.task, InputSupport{URL: true})
	require.ErrorContains(t, err, "another task ID")
	_, err = normalizeTask(t.Context(), taskPeerID, result.task, InputSupport{})
	require.ErrorContains(t, err, "unadvertised url capability")
	_, err = normalizeTask(WithoutHostInput(t.Context()), taskPeerID, result.task, InputSupport{URL: true})
	require.ErrorContains(t, err, "host input is disabled")
	task, err := normalizeTask(t.Context(), taskPeerID, result.task, InputSupport{URL: true})
	require.NoError(t, err)
	input, ok := task.AsInputRequired()
	require.True(t, ok)
	assert.NoError(t, input.Requests["consent:1"].ValidateResponse(json.RawMessage(`{"action":"accept"}`)))
}

func TestTaskHTTPRejectsUnadvertisedOrUnsupportedCreation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ ID json.RawMessage }
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": json.RawMessage(`{"resultType":"task",` + taskPeerMetadata + "}")}))
	}))
	t.Cleanup(server.Close)
	transport := NewHTTPTransport(server.Client(), ClientInfo{Name: "task-host", Version: "1"}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
	for _, tc := range []struct {
		method  string
		support bool
	}{
		{methodToolsCall, false}, {methodPromptsGet, true}, {"resources/read", true}, {methodTasksGet, true},
	} {
		t.Run(tc.method, func(t *testing.T) {
			ctx := t.Context()
			if tc.support {
				ctx = WithTaskSupport(ctx)
			}
			var result toolsCallResult
			err := transport.call(ctx, server.URL, tc.method, map[string]any{"name": "read", "uri": "record://example", "taskId": taskPeerID}, &result)
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
		})
	}
}
