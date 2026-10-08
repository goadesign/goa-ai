package temporal

// These tests submit real prepared starts through the Temporal adapter, then
// execute production planner, storage and tool activities in the SDK environment.
// Each input round uses a new worker and the same trusted runtime store.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type mcpInputPlanner struct{ starts, resumes atomic.Int32 }

// PlanStart supplies one domain invocation; the runtime assigns its call identity.
func (p *mcpInputPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	p.starts.Add(1)
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "remote.lookup", Payload: rawjson.Message(`{"query":"original"}`)}}}, nil
}

// PlanResume sees a completed domain result once, without protocol state or input IDs.
func (p *mcpInputPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.resumes.Add(1)
	if len(input.ToolOutputs) != 1 || input.ToolOutputs[0].Failure != nil || string(input.ToolOutputs[0].Result) != `{"answer":42}` {
		return nil, errors.New("planner did not receive the exact completed result")
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}}, nil
}

func TestMCPContinuationThroughTemporalWorkers(t *testing.T) {
	testMCPContinuationThroughTemporalWorkers(t, false)
}

func TestTaskContinuationThroughTemporalWorkers(t *testing.T) {
	testMCPContinuationThroughTemporalWorkers(t, true)
}

// The same prepared-start path restores ordinary input and Task input. Each
// successor uses a new worker, the original result codec and the stored history.
func testMCPContinuationThroughTemporalWorkers(t *testing.T, taskMode bool) {
	store := storageinmem.New()
	_, err := store.CreateSession(t.Context(), "mcp-session", time.Now().UTC())
	require.NoError(t, err)
	plan := &mcpInputPlanner{}
	var calls atomic.Int32
	var observations, updates atomic.Int32
	var idMu sync.Mutex
	var ids []string
	peer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var message struct {
			ID     string                     `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&message)) {
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		var result any
		switch message.Method {
		case "tools/list":
			result = map[string]any{"resultType": "complete", "ttlMs": 0, "cacheScope": "private", "tools": []any{map[string]any{
				"name": "lookup", "inputSchema": json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
				"outputSchema": json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
			}}}
		case "tools/call":
			idMu.Lock()
			ids = append(ids, message.ID)
			idMu.Unlock()
			assert.JSONEq(t, `{"query":"original"}`, string(message.Params["arguments"]))
			if calls.Add(1) == 1 {
				assert.NotContains(t, message.Params, "requestState")
				if taskMode {
					result = map[string]any{"resultType": "task", "taskId": "", "status": "working", "createdAt": "2026-10-07T00:00:00Z", "lastUpdatedAt": "2026-10-07T00:00:00Z", "ttlMs": nil, "pollIntervalMs": 1}
					break
				}
				result = map[string]any{"resultType": "input_required", "requestState": "opaque-state", "inputRequests": map[string]mcp.InputRequest{
					"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"]}}`)},
				}}
			} else {
				assert.JSONEq(t, `"opaque-state"`, string(message.Params["requestState"]))
				assert.JSONEq(t, `{"choice":{"action":"accept","content":{"choice":"yes"}}}`, string(message.Params["inputResponses"]))
				result = map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": json.RawMessage(`{"answer":42}`)}
			}
		case "tasks/get":
			assert.True(t, taskMode)
			assert.JSONEq(t, `""`, string(message.Params["taskId"]))
			result = map[string]any{"resultType": "complete", "taskId": "", "createdAt": "2026-10-07T00:00:00Z", "lastUpdatedAt": "2026-10-07T00:00:00Z", "ttlMs": nil, "pollIntervalMs": 1}
			observed := observations.Add(1)
			if observed <= 2 {
				result.(map[string]any)["status"] = "input_required"
				result.(map[string]any)["inputRequests"] = map[string]mcp.InputRequest{
					"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"]}}`)},
				}
			} else {
				result.(map[string]any)["status"] = "completed"
				result.(map[string]any)["result"] = map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": json.RawMessage(`{"answer":42}`)}
			}
		case "tasks/update":
			assert.True(t, taskMode)
			assert.JSONEq(t, `""`, string(message.Params["taskId"]))
			assert.JSONEq(t, `{"choice":{"action":"accept","content":{"choice":"yes"}}}`, string(message.Params["inputResponses"]))
			updates.Add(1)
			result = map[string]any{"resultType": "complete"}
		default:
			t.Errorf("unexpected remote method %q", message.Method)
			return
		}
		assert.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}))
	}))
	t.Cleanup(peer.Close)
	var saved *api.RunSuspension
	spec := anyJSONToolSpec("remote.lookup")
	definition := testTemporalAgentDefinition("mcp.agent", "mcp.workflow", "default.queue", []tools.ToolSpec{spec})
	for round := 1; round <= 2; round++ {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetTestTimeout(30 * time.Second)
		env.SetDataConverter(NewAgentDataConverter())
		eng := newTestEngine(t)
		eng.client.Close()
		service := &testWorkflowService{}
		eng.client = newWorkflowServiceClient(t, service)
		eng.workerFactory = func(client.Client, string, worker.Options) worker.Worker {
			return &boundedCompletionWorker{env: env}
		}
		rt := agentruntime.New(store, agentruntime.WithEngine(eng))
		remote, err := mcp.NewHTTPCaller(mcp.HTTPOptions{Endpoint: peer.URL, Client: peer.Client(), ClientInfo: mcp.ClientInfo{Name: "temporal-test", Version: "1"}, InputSupport: mcp.InputSupport{Form: true}})
		require.NoError(t, err)
		require.NoError(t, rt.RegisterToolset(agentruntime.ToolsetRegistration{
			Name: "remote", Specs: []tools.ToolSpec{spec}, ActivityRetryPolicy: &engine.RetryPolicy{MaxAttempts: 1},
			Execute: func(ctx context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
				if taskMode {
					ctx = mcp.WithTaskSupport(ctx)
				}
				return agentruntime.ExecuteMCPTool(ctx, remote, call, "lookup", spec)
			},
		}))
		require.NoError(t, rt.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
			Definition: definition, Planner: plan, WorkflowHandler: rt.ExecuteWorkflow,
			PlanActivityName: "mcp.plan", ResumeActivityName: "mcp.resume", ExecuteToolActivity: "mcp.tool",
			Policy: agentruntime.RunPolicy{MaxToolCalls: 1},
		}))
		caller := rt.MustClientFor(definition)
		var prepared *agentruntime.PreparedRun
		if round == 1 {
			prepared, err = caller.Prepare(t.Context(), "mcp-session", []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "lookup"}}}}, agentruntime.WithRunID("mcp-first"))
		} else {
			require.NotNil(t, saved)
			_, rejectErr := caller.PrepareContinuation(t.Context(), "mcp-session", "mcp-first", "mcp-invalid", "turn-invalid", &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: saved.Pending[0].MCP.ToolCallID, Responses: map[string]json.RawMessage{"another-call": json.RawMessage(`{"action":"decline"}`)}}}, agentruntime.WorkflowOptions{})
			require.ErrorIs(t, rejectErr, agentruntime.ErrContinuationRejected)
			prepared, err = caller.PrepareContinuation(t.Context(), "mcp-session", "mcp-first", "mcp-second", "turn-second", &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: saved.Pending[0].MCP.ToolCallID, Responses: map[string]json.RawMessage{"choice": json.RawMessage(`{"action":"accept","content":{"choice":"yes"}}`)}}}, agentruntime.WorkflowOptions{})
		}
		require.NoError(t, err)
		_, err = caller.StartPrepared(t.Context(), prepared)
		require.NoError(t, err)
		request := service.startRequest()
		require.NotNil(t, request)
		var input *api.RunInput
		require.NoError(t, NewAgentDataConverter().FromPayloads(request.Input, &input))
		digest := decodePayload[[]byte](t, request.Memo.Fields[workflowStartRecipeMemoKey])
		env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: request.WorkflowId, TaskQueue: request.TaskQueue.Name})
		require.NoError(t, env.SetMemoOnStart(map[string]any{workflowStartRecipeMemoKey: digest}))
		env.ExecuteWorkflow("mcp.workflow", input)
		require.NoError(t, env.GetWorkflowError())
		var out *api.RunOutput
		require.NoError(t, env.GetWorkflowResult(&out))
		if round == 1 {
			require.NotNil(t, out.Suspension)
			saved = out.Suspension
			require.Equal(t, api.PendingInputKindMCP, saved.Pending[0].Kind)
			assert.Zero(t, plan.resumes.Load())
			events, err := rt.ListRunEvents(t.Context(), "mcp-first", "", 100)
			require.NoError(t, err)
			for _, event := range events.Events {
				assert.NotEqual(t, hooks.ToolResultReceived, event.Type)
				if event.Type == hooks.AwaitMCPInput {
					assert.NotContains(t, string(event.Payload), "opaque-state")
				}
			}
		} else {
			assert.Nil(t, out.Suspension)
			assert.Equal(t, "done", out.Final.Text())
		}
	}
	idMu.Lock()
	if taskMode {
		assert.Len(t, ids, 1, "Task observations must not repeat tools/call")
	} else {
		assert.Len(t, ids, 2)
		if len(ids) == 2 {
			assert.NotEqual(t, ids[0], ids[1], "a new worker must use a new JSON-RPC ID")
		}
	}
	idMu.Unlock()
	if taskMode {
		assert.EqualValues(t, 1, calls.Load())
		assert.EqualValues(t, 3, observations.Load())
		assert.EqualValues(t, 1, updates.Load())
	} else {
		assert.EqualValues(t, 2, calls.Load())
	}
	assert.EqualValues(t, 1, plan.starts.Load())
	assert.EqualValues(t, 1, plan.resumes.Load())
}
