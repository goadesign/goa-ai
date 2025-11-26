package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestFinalizerToolInvokerRunsServiceTool(t *testing.T) {
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.aggregate": {
				Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
					require.Equal(t, tools.Ident("svc.aggregate.finalize"), call.Name)
					require.NotNil(t, call.Payload)
					return &planner.ToolResult{
						Name:   call.Name,
						Result: map[string]any{"code": "ok"},
					}, nil
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.aggregate.finalize"): newAnyJSONSpec("svc.aggregate.finalize", "svc.aggregate"),
		},
	}
	wf := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	factory := &finalizerInvokerFactory{
		runtime:      rt,
		wfCtx:        wf,
		activityName: "execute",
		agentID:      "svc.agent",
	}
	invoker := &finalizerToolInvoker{
		factory: factory,
		meta: toolInvokerMeta{
			RunID:            "run-1",
			SessionID:        "sess-1",
			TurnID:           "turn-1",
			ParentToolCallID: "parent-call",
			AgentID:          "svc.agent",
		},
	}
	res, err := invoker.Invoke(context.Background(), tools.Ident("svc.aggregate.finalize"), map[string]any{"method": "ada.method"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, tools.Ident("svc.aggregate.finalize"), res.Name)
	require.Equal(t, map[string]any{"code": "ok"}, res.Result)
}
