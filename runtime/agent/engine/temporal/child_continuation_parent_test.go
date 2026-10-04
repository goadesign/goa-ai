package temporal

// A newly accepted continuation names its actual Temporal parent while keeping
// the old child checkpoint bytes. This does not establish replay compatibility
// for a parent history that already emitted the previous child request.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

// childMemoCapture observes actual child options without changing the request
// or its future. The SDK test listener receives the emitted memo separately.
type (
	childMemoCapture struct {
		interceptor.WorkerInterceptorBase
		interceptor.WorkflowInboundInterceptorBase
		beforeChild func(workflow.Context)
	}
	childMemoOutbound struct {
		interceptor.WorkflowOutboundInterceptorBase
		beforeChild func(workflow.Context)
	}
)

func TestTemporalChildContinuationBindsExecutionParentAndRetainsCheckpoint(t *testing.T) {
	data, err := os.ReadFile("../../runtime/testdata/retained_child_suspension_v11.json")
	require.NoError(t, err)
	var suspension api.RunSuspension
	require.NoError(t, json.Unmarshal(data, &suspension))
	checkpoint := append([]byte(nil), suspension.Checkpoint...)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetDataConverter(NewAgentDataConverter())
	// The SDK test environment omits child memo delivery. Capture the real
	// adapter's outgoing memo, then supply those bytes before the child starts.
	emittedMemo := make(chan *commonpb.Memo, 1)
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&childMemoCapture{beforeChild: func(ctx workflow.Context) {
			options := workflow.GetChildWorkflowOptions(ctx)
			require.Equal(t, "continued-child", options.WorkflowID)
			memo := &commonpb.Memo{Fields: make(map[string]*commonpb.Payload, len(options.Memo))}
			for key, value := range options.Memo {
				payload, err := NewAgentDataConverter().ToPayload(value)
				require.NoError(t, err)
				memo.Fields[key] = payload
			}
			require.NotNil(t, memo.Fields[startrecipe.MemoKey], "the actual child start must emit its digest")
			select {
			case emittedMemo <- memo:
			default:
				t.Fatal("the fixture emitted more than one child memo")
			}
		}},
	}})
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		require.Equal(t, "continued-child", info.WorkflowExecution.ID)
		require.Nil(t, info.Memo, "remove fixture delivery if the SDK starts delivering child memos")
		select {
		case memo := <-emittedMemo:
			info.Memo = memo
		default:
			t.Fatal("child started without the adapter's emitted memo")
		}
	})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		info := workflow.GetInfo(ctx)
		if info.ParentWorkflowExecution == nil || info.ParentWorkflowExecution.ID != input.ParentRunID {
			return nil, fmt.Errorf("child execution parent does not match accepted input %q", input.ParentRunID)
		}
		if !bytes.Equal(checkpoint, input.Continuation.Suspension.Checkpoint) {
			return nil, fmt.Errorf("child checkpoint bytes changed")
		}
		request := engine.ChildWorkflowRequest{ID: input.RunID, Workflow: "continued.child", TaskQueue: "child.queue", Input: input}
		expected, err := startrecipe.SnapshotChildRequest(request)
		if err != nil {
			return nil, err
		}
		actual, err := (&temporalWorkflowContext{engine: &Engine{}, ctx: ctx}).StartRequestDigest()
		if err != nil {
			return nil, err
		}
		if expected.Digest != actual {
			return nil, fmt.Errorf("accepted child digest changed")
		}
		return &api.RunOutput{AgentID: input.AgentID, RunID: input.RunID}, nil
	}, workflow.RegisterOptions{Name: "continued.child"})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		parentID := workflow.GetInfo(ctx).WorkflowExecution.ID
		input := &api.RunInput{AgentID: "child.agent", RunID: "continued-child", SessionID: "session-1", TurnID: "turn-new", ParentRunID: parentID,
			Continuation: &api.RunContinuationInput{Suspension: &suspension, Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "Confirmed"}}}}
		request := engine.ChildWorkflowRequest{ID: input.RunID, Workflow: "continued.child", TaskQueue: "child.queue", Input: input}
		current, err := startrecipe.SnapshotChildRequest(request)
		if err != nil {
			return err
		}
		oldInput := *input
		oldInput.ParentRunID = ""
		oldRequest := request
		oldRequest.Input = &oldInput
		previous, err := startrecipe.SnapshotChildRequest(oldRequest)
		if err != nil {
			return err
		}
		if current.Digest == previous.Digest {
			return fmt.Errorf("execution parent was omitted from the request digest")
		}
		w := &temporalWorkflowContext{engine: &Engine{}, ctx: ctx}
		child, err := w.StartChildWorkflow(context.Background(), engine.ChildWorkflowRequest{
			ID: input.RunID, Workflow: request.Workflow, TaskQueue: request.TaskQueue, Input: input,
		})
		if err != nil {
			return err
		}
		input.ParentRunID = "caller-mutated-after-start"
		out, err := child.Get(context.Background())
		if err != nil {
			return err
		}
		if out.RunID != "continued-child" {
			return fmt.Errorf("wrong child result %q", out.RunID)
		}
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, checkpoint, []byte(suspension.Checkpoint))
}

// InterceptWorkflow installs observation for each workflow without replacing
// the registered parent or child workflow implementation.
func (c *childMemoCapture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &childMemoCapture{
		WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next},
		beforeChild:                    c.beforeChild,
	}
}

// Init forwards workflow operations through the observer and then the SDK.
func (c *childMemoCapture) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return c.Next.Init(&childMemoOutbound{
		WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next},
		beforeChild:                     c.beforeChild,
	})
}

// ExecuteChildWorkflow records the adapter's outgoing options and preserves
// the SDK call, arguments and returned child future unchanged.
func (c *childMemoOutbound) ExecuteChildWorkflow(ctx workflow.Context, name string, args ...any) workflow.ChildWorkflowFuture {
	c.beforeChild(ctx)
	return c.Next.ExecuteChildWorkflow(ctx, name, args...)
}
