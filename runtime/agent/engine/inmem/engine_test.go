package inmem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestPlannerActivityTypedExecution(t *testing.T) {
	eng := New()
	ctx := context.Background()

	err := eng.RegisterPlannerActivity(ctx, "test_plan", engine.ActivityOptions{}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{
			Result: &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role: model.ConversationRoleAssistant,
					},
				},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("register planner activity: %v", err)
	}

	err = eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			out, err2 := wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
				Name:  "test_plan",
				Input: &api.PlanActivityInput{},
			})
			if err2 != nil {
				return nil, err2
			}
			if out == nil || out.Result == nil || out.Result.FinalResponse == nil {
				t.Errorf("expected non-nil plan output/result/final response")
			}
			return &api.RunOutput{}, nil
		},
	})
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}

	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:       "test-run-1",
		Workflow: "test_workflow",
		Input:    &api.RunInput{},
	})
	if err != nil {
		t.Fatalf("start workflow: %v", err)
	}

	_, err = handle.Wait(ctx)
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
}

func TestPlannerActivityTimeoutOwnership(t *testing.T) {
	waitForCancellation := func(ctx context.Context, _ *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	tests := []struct {
		name          string
		parentTimeout time.Duration
		options       engine.ActivityOptions
		handler       func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)
		want          error
		wantCause     error
	}{
		{
			name: "schedule to close owns total deadline",
			options: engine.ActivityOptions{
				ScheduleToCloseTimeout: 10 * time.Millisecond,
				StartToCloseTimeout:    time.Second,
			},
			handler:   waitForCancellation,
			want:      engine.ErrPlannerActivityDeadlineExceeded,
			wantCause: context.DeadlineExceeded,
		},
		{
			name:          "caller deadline retains ownership",
			parentTimeout: 10 * time.Millisecond,
			options: engine.ActivityOptions{
				ScheduleToCloseTimeout: time.Second,
				StartToCloseTimeout:    time.Second,
			},
			handler:   waitForCancellation,
			want:      context.DeadlineExceeded,
			wantCause: context.DeadlineExceeded,
		},
		{
			name: "provider timeout remains activity failure",
			options: engine.ActivityOptions{
				ScheduleToCloseTimeout: time.Second,
				StartToCloseTimeout:    time.Second,
			},
			handler: func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
				return nil, context.DeadlineExceeded
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eng := New().(*eng)
			var handlerCause error
			err := eng.RegisterPlannerActivity(
				context.Background(),
				"test_plan",
				engine.ActivityOptions{},
				func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
					out, err := test.handler(ctx, input)
					handlerCause = context.Cause(ctx)
					return out, err
				},
			)
			require.NoError(t, err)
			parent := context.Background()
			cancel := func() {
			}
			if test.parentTimeout > 0 {
				parent, cancel = context.WithTimeout(parent, test.parentTimeout)
			}
			defer cancel()
			wfCtx := &wfCtx{
				ctx: parent,
				eng: eng,
			}

			_, err = wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
				Name:    "test_plan",
				Input:   &api.PlanActivityInput{},
				Options: test.options,
			})

			require.ErrorIs(t, err, test.want)
			if !errors.Is(test.want, engine.ErrPlannerActivityDeadlineExceeded) {
				require.NotErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
			}
			if test.wantCause == nil {
				require.NoError(t, handlerCause)
			} else {
				require.ErrorIs(t, handlerCause, test.wantCause)
			}
		})
	}
}

func TestToolActivityFutureTypedExecution(t *testing.T) {
	eng := New()
	ctx := context.Background()

	err := eng.RegisterExecuteToolActivity(ctx, "test_tool", engine.ActivityOptions{}, func(ctx context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
		return &api.ToolOutput{
			Payload: []byte("null"),
		}, nil
	})
	if err != nil {
		t.Fatalf("register tool activity: %v", err)
	}

	err = eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			fut, err2 := wfCtx.ExecuteToolActivityAsync(engine.ToolActivityCall{
				Name: "test_tool",
				Input: &api.ToolInput{
					RunID:       "test-run-1",
					AgentID:     "agent",
					ToolsetName: "svc.tools",
					ToolName:    "svc.tools.tool",
					ToolCallID:  "tool-1",
					Payload:     []byte("null"),
				},
			})
			if err2 != nil {
				return nil, err2
			}
			out, err2 := fut.Get(wfCtx.Context())
			if err2 != nil {
				return nil, err2
			}
			if out == nil || string(out.Payload) != "null" {
				t.Errorf("unexpected tool output: %+v", out)
			}
			return &api.RunOutput{}, nil
		},
	})
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}

	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:       "test-run-1",
		Workflow: "test_workflow",
		Input:    &api.RunInput{},
	})
	if err != nil {
		t.Fatalf("start workflow: %v", err)
	}

	_, err = handle.Wait(ctx)
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
}

func TestSignalTypedDelivery(t *testing.T) {
	eng := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			req, err2 := wfCtx.PauseRequests().Receive(wfCtx.Context())
			if err2 != nil {
				return nil, err2
			}
			if req == nil {
				t.Fatal("pause request is nil")
			}
			if req.RunID != "test-run-1" || req.Reason != "human" {
				t.Errorf("unexpected pause request: %+v", req)
			}
			return &api.RunOutput{}, nil
		},
	})
	if err != nil {
		t.Fatalf("register workflow: %v", err)
	}

	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:       "test-run-1",
		Workflow: "test_workflow",
		Input:    &api.RunInput{},
	})
	if err != nil {
		t.Fatalf("start workflow: %v", err)
	}

	err = handle.Signal(ctx, api.SignalPause, &api.PauseRequest{
		RunID:  "test-run-1",
		Reason: "human",
	})
	if err != nil {
		t.Fatalf("signal workflow: %v", err)
	}

	_, err = handle.Wait(ctx)
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
}
