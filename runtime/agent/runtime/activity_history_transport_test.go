package runtime

// This regression runs the complete workflow through an engine that serializes
// every command. Individually bounded tool results grow the saved transcript
// beyond one engine payload while planning and finalization remain executable.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type historyTransportEngine struct {
	engine.Engine
	mu            sync.Mutex
	commands      [][]byte
	childCommands [][]byte
}

func (e *historyTransportEngine) RegisterAgentChildActivity(ctx context.Context, name string, options engine.ActivityOptions, handler func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error {
	return e.Engine.RegisterAgentChildActivity(ctx, name, options, func(ctx context.Context, input *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.childCommands = append(e.childCommands, encoded)
		e.mu.Unlock()
		return handler(ctx, input)
	})
}

func (e *historyTransportEngine) RegisterPlannerActivity(ctx context.Context, name string, options engine.ActivityOptions, handler func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	return e.Engine.RegisterPlannerActivity(ctx, name, options, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.commands = append(e.commands, encoded)
		e.mu.Unlock()
		return handler(ctx, input)
	})
}

func TestActivityHistoryTransportGrowsBeyondEngineLimitAndFinalizes(t *testing.T) {
	const childAgent = "child.agent"
	eng := &historyTransportEngine{Engine: engineinmem.New()}
	rt := New(newTestStore(), WithEngine(eng), WithLogger(telemetry.NoopLogger{}))
	tool := newAnyJSONSpec("catalog.read")
	resultText := strings.Repeat("bounded evidence ", 3500)
	require.Less(t, len(resultText)+2, transcript.MaxToolResultContentBytes)
	var toolExecutions int
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "catalog", Specs: []tools.ToolSpec{tool},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			toolExecutions++
			return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: resultText}, nil
		}),
	}))
	childRegistration := AgentRegistration{
		Definition: testRegistrationDefinition(childAgent, engine.WorkflowDefinition{
			Name: "child.workflow", Handler: rt.ExecuteWorkflow,
		}, nil),
		WorkflowHandler: rt.ExecuteWorkflow,
		Planner: &stubPlanner{start: func(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			require.Len(t, in.Messages, 1)
			require.Equal(t, `{}`, in.Messages[0].Text())
			return finalPlannerResult("child inspected the request"), nil
		}},
		PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), childRegistration))
	childSpec := newAnyJSONSpec("child.inspect")
	childSpec.IsAgentTool = true
	childSpec.AgentID = childAgent
	var childHistoryBytes int
	childToolset := NewAgentToolsetRegistration(AgentToolConfig{
		Definition: childRegistration.Definition, Name: "child",
		PreChildValidator: func(_ context.Context, in *AgentToolValidationInput) *tools.ValidationError {
			encoded, err := json.Marshal(in.Messages)
			require.NoError(t, err)
			childHistoryBytes = len(encoded)
			require.Greater(t, childHistoryBytes, engine.MaxPayloadBytes)
			return nil
		},
	})
	childToolset.Specs = []tools.ToolSpec{childSpec}
	require.NoError(t, rt.RegisterToolset(childToolset))
	next := func() *planner.PlanResult {
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: rawjson.Message(`{}`)}}}
	}
	var originalJSON []byte
	var finalHistoryBytes int
	var resumeHistoryBytes []int
	impl := &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			encoded, err := json.Marshal(input.Messages)
			require.NoError(t, err)
			// Exact bytes matter: semantic JSON equality would hide changed encoding.
			require.Equal(t, originalJSON, encoded) //nolint:testifylint
			return next(), nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			encoded, err := json.Marshal(input.Messages)
			require.NoError(t, err)
			if input.Finalize != nil {
				finalHistoryBytes = len(encoded)
				require.Equal(t, planner.TerminationReasonToolCap, input.Finalize.Reason)
				require.Equal(t, input.Finalize.Message, input.Messages[len(input.Messages)-1].Text())
				return finalPlannerResult("complete evidence preserved"), nil
			}
			resumeHistoryBytes = append(resumeHistoryBytes, len(encoded))
			if len(resumeHistoryBytes) == 2 {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name: childSpec.Name, Payload: rawjson.Message(`{}`),
				}}}, nil
			}
			return next(), nil
		},
	}
	reg := correctionTestRegistration(rt, impl, []tools.ToolSpec{tool, childSpec})
	reg.Policy.MaxToolCalls = 3
	require.NoError(t, rt.RegisterAgent(t.Context(), reg))
	_, err := createSessionForTest(t.Context(), rt.Store, "transport-session")
	require.NoError(t, err)
	initial := []*model.Message{userMsg(strings.Repeat("x", 940000))}
	originalJSON, err = json.Marshal(initial)
	require.NoError(t, err)
	require.Less(t, len(originalJSON), maxPlanActivityInputBytes)
	out, err := rt.MustClient("catalog.agent").Run(t.Context(), "transport-session", initial, WithRunID("transport-run"), WithTurnID("turn"))
	require.NoError(t, err)
	require.Equal(t, "complete evidence preserved", out.Final.Text())
	require.Equal(t, 2, toolExecutions)
	require.Len(t, resumeHistoryBytes, 3)
	require.Greater(t, resumeHistoryBytes[1], maxPlanActivityInputBytes)
	require.Greater(t, finalHistoryBytes, engine.MaxPayloadBytes)
	eng.mu.Lock()
	commands := append([][]byte(nil), eng.commands...)
	childCommands := append([][]byte(nil), eng.childCommands...)
	eng.mu.Unlock()
	require.Len(t, commands, 6)
	require.Len(t, childCommands, 1)
	require.Greater(t, childHistoryBytes, engine.MaxPayloadBytes)
	for _, command := range append(commands, childCommands...) {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(command, &fields))
		require.NotContains(t, fields, "Messages")
		require.NotEqual(t, json.RawMessage(`""`), fields["HistoryEndID"])
		require.Less(t, len(command), 10000)
	}
	t.Logf("planner histories=%v, child history=%d bytes, finalization history=%d bytes", resumeHistoryBytes, childHistoryBytes, finalHistoryBytes)
	saved, err := transcript.BuildMessagesFromRunLog(t.Context(), rt.Store, "transport-run")
	require.NoError(t, err)
	require.Equal(t, initial[0].Text(), saved[0].Text())
	for _, message := range saved {
		require.NotContains(t, message.Text(), "FINALIZE NOW:", "finalization instructions are invocation-only")
	}
}
