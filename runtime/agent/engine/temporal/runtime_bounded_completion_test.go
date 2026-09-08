package temporal

// These tests run real planner, tool, storage, and completion code through both
// engines. Many individually valid pages must not become one oversized workflow
// result, and a genuinely oversized final result must not be recorded as success.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	boundedCompletionWorker struct {
		fakeWorker
		env *testsuite.TestWorkflowEnvironment
	}
	pageCompletionPlanner struct {
		pages int
		next  atomic.Int32
	}
)

const (
	completionPageBytes      = 160 * 1024
	completionFailureMessage = "upstream returned an exact diagnostic: request rejected before retry"
)

func TestRuntimeCompletesLargeDiagnosticHistory(t *testing.T) {
	for _, backend := range []string{"inmem", "temporal"} {
		t.Run(backend, func(t *testing.T) {
			const pages = 8
			rt, store, execute := boundedCompletionRuntime(t, backend, pages, "test-model", false)
			out, err := execute()
			require.NoError(t, err)
			require.NotNil(t, out.FinalToolResult)
			assert.JSONEq(t, `{"complete":true}`, string(out.FinalToolResult.Result))
			assert.Equal(t, pages+2, out.ToolCount)
			assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: pages + 2, DurationMs: int64(pages + 2), Model: "test-model"}, out.ToolTelemetry)
			assert.Equal(t, 1, out.FinalToolResult.Telemetry.TokensUsed)
			encoded, err := workflowcodec.NewDataConverter().ToPayload(out)
			require.NoError(t, err)
			assert.Less(t, len(encoded.Data), completionPageBytes)
			meta, err := store.LoadRun(t.Context(), "completion-run")
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusCompleted, meta.Status)

			// Page the existing Store rather than reconstructing returned history.
			// Every exact payload, failure message, and telemetry record survives.
			var results []*api.ToolEvent
			cursor := ""
			readPages := 0
			for {
				page, err := rt.ListRunEvents(t.Context(), "completion-run", cursor, 3)
				require.NoError(t, err)
				readPages++
				for _, record := range page.Events {
					if record.Type != hooks.ToolResultReceived {
						continue
					}
					event, err := hooks.DecodeRunlogEvent(record)
					require.NoError(t, err)
					result := event.(*hooks.ToolResultReceivedEvent)
					assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: 1, DurationMs: 1, Model: "test-model"}, result.Telemetry)
					results = append(results, &api.ToolEvent{Name: result.ToolName, Result: result.ResultJSON, Failure: result.Failure, Telemetry: result.Telemetry, ToolCallID: result.ToolCallID})
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			assert.Greater(t, readPages, 1)
			require.Len(t, results, pages+2)
			assert.Equal(t, completionFailureMessage, results[0].Failure.Error.Message)
			for index := 0; index < pages; index++ {
				assert.JSONEq(t, fmt.Sprintf(`{"page":%d,"data":%q}`, index, strings.Repeat("x", completionPageBytes)), string(results[index+1].Result))
			}
			assert.JSONEq(t, `{"complete":true}`, string(results[len(results)-1].Result))
			// This is the removed old schema, deliberately not the new RunOutput.
			_, err = workflowcodec.NewDataConverter().ToPayload(struct{ ToolEvents []*api.ToolEvent }{results})
			require.ErrorContains(t, err, "maximum aggregate size")
		})
	}
}

func TestRuntimeRejectsFinalOutputBeforeRecordingSuccess(t *testing.T) {
	for _, backend := range []string{"inmem", "temporal"} {
		t.Run(backend, func(t *testing.T) {
			// The tool's telemetry fits independently; returning it both on the
			// final tool and the invocation summary exceeds the exact output limit.
			_, store, execute := boundedCompletionRuntime(t, backend, -1, strings.Repeat("m", 550*1024), false)
			out, err := execute()
			require.ErrorContains(t, err, "copy workflow output")
			require.ErrorContains(t, err, "maximum aggregate size")
			assert.Nil(t, out)
			meta, loadErr := store.LoadRun(t.Context(), "completion-run")
			require.NoError(t, loadErr)
			assert.Equal(t, session.RunStatusFailed, meta.Status)
		})
	}
}

func TestRuntimeRejectsOutputBeforeRecordingSuspension(t *testing.T) {
	for _, backend := range []string{"inmem", "temporal"} {
		t.Run(backend, func(t *testing.T) {
			// Suspensions still contain full continuation state. Their size can
			// exceed the output limit even when each prior tool call was valid.
			_, store, execute := boundedCompletionRuntime(t, backend, 4, "test-model", true)
			out, err := execute()
			require.ErrorContains(t, err, "copy workflow output")
			require.ErrorContains(t, err, "maximum aggregate size")
			assert.Nil(t, out)
			meta, loadErr := store.LoadRun(t.Context(), "completion-run")
			require.NoError(t, loadErr)
			assert.Equal(t, session.RunStatusFailed, meta.Status)
			_, loadErr = store.LoadRunSuspension(t.Context(), "completion-run")
			require.ErrorIs(t, loadErr, session.ErrRunSuspensionNotFound)
		})
	}
}

// boundedCompletionRuntime registers the same production handlers with either
// the in-memory engine or the real Temporal adapter backed by its test server.
func boundedCompletionRuntime(t *testing.T, backend string, pages int, modelName string, suspend bool) (*agentruntime.Runtime, *storageinmem.Store, func() (*api.RunOutput, error)) {
	t.Helper()
	var eng engine.Engine
	var env *testsuite.TestWorkflowEnvironment
	if backend == "inmem" {
		eng = engineinmem.New()
	} else {
		var suite testsuite.WorkflowTestSuite
		env = suite.NewTestWorkflowEnvironment()
		env.SetDataConverter(NewAgentDataConverter())
		temporalEngine := newTestEngine(t)
		temporalEngine.workerFactory = func(client.Client, string, worker.Options) worker.Worker {
			return &boundedCompletionWorker{env: env}
		}
		eng = temporalEngine
	}
	store := storageinmem.New()
	_, err := store.CreateSession(t.Context(), "completion-session", time.Now().UTC())
	require.NoError(t, err)
	opts := []agentruntime.RuntimeOption{agentruntime.WithEngine(eng), agentruntime.WithLogger(telemetry.NoopLogger{})}
	if suspend {
		opts = append(opts, agentruntime.WithToolConfirmation(&agentruntime.ToolConfirmationConfig{
			Confirm: map[tools.Ident]*agentruntime.ToolConfirmation{"records.finish": {
				Prompt: func(context.Context, *agentruntime.ToolCall) (string, error) { return "Finish?", nil },
				DeniedResult: func(context.Context, *agentruntime.ToolCall) (any, error) {
					return map[string]any{"complete": false}, nil
				},
			}},
		}))
	}
	rt := agentruntime.New(store, opts...)
	failure, page, final := anyJSONToolSpec("records.failure"), anyJSONToolSpec("records.page"), anyJSONToolSpec("records.finish")
	final.TerminalRun = true
	final.Bookkeeping = true
	specs := []tools.ToolSpec{failure, page, final}
	require.NoError(t, rt.RegisterToolset(agentruntime.ToolsetRegistration{
		Name: "records", Specs: specs,
		Execute: func(_ context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
			result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Telemetry: &telemetry.ToolTelemetry{TokensUsed: 1, DurationMs: 1, Model: modelName}}
			switch call.Name {
			case failure.Name:
				result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError(completionFailureMessage), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
			case page.Name:
				var input struct {
					Page int `json:"page"`
				}
				if err := json.Unmarshal(call.Payload, &input); err != nil {
					return nil, err
				}
				result.Result = map[string]any{"page": input.Page, "data": strings.Repeat("x", completionPageBytes)}
			case final.Name:
				result.Result = map[string]any{"complete": true}
			case tools.ToolUnavailable:
				return nil, fmt.Errorf("runtime unavailable-tool result must not invoke the fixture executor")
			default:
				return nil, fmt.Errorf("unexpected fixture tool %q", call.Name)
			}
			return &agentruntime.ToolExecutionResult{ToolResult: result}, nil
		},
	}))
	require.NoError(t, rt.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
		Definition:       testTemporalAgentDefinition("completion.agent", "completion.workflow", "default.queue", specs),
		Planner:          &pageCompletionPlanner{pages: pages},
		WorkflowHandler:  rt.ExecuteWorkflow,
		PlanActivityName: "completion.plan", ResumeActivityName: "completion.resume", ExecuteToolActivity: "completion.tool",
	}))
	input := &api.RunInput{AgentID: "completion.agent", RunID: "completion-run", SessionID: "completion-session", TurnID: "completion-turn"}
	return rt, store, func() (*api.RunOutput, error) {
		if env != nil {
			env.ExecuteWorkflow("completion.workflow", input)
			if err := env.GetWorkflowError(); err != nil {
				return nil, err
			}
			var out *api.RunOutput
			err := env.GetWorkflowResult(&out)
			return out, err
		}
		handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{ID: input.RunID, Workflow: "completion.workflow", TaskQueue: "default.queue", Input: input})
		if err != nil {
			return nil, err
		}
		return handle.Wait(t.Context())
	}
}

func (w *boundedCompletionWorker) RegisterWorkflowWithOptions(fn any, options workflow.RegisterOptions) {
	w.env.RegisterWorkflowWithOptions(fn, options)
}

func (w *boundedCompletionWorker) RegisterActivityWithOptions(fn any, options activity.RegisterOptions) {
	w.env.RegisterActivityWithOptions(fn, options)
}

func (p *pageCompletionPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	name := tools.Ident("records.failure")
	if p.pages < 0 {
		name = "records.finish"
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: name, Payload: rawjson.Message(`{}`)}}}, nil
}

func (p *pageCompletionPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	next := int(p.next.Add(1)) - 1
	name, payload := tools.Ident("records.page"), rawjson.Message(fmt.Sprintf(`{"page":%d}`, next))
	if next == p.pages {
		name, payload = "records.finish", rawjson.Message(`{}`)
	}
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: name, Payload: payload}}}, nil
}
