package inmem

// This file checks in-memory workflow and activity execution, cancellation,
// timeout, and worker registration behavior.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestPlannerActivityTypedExecution(t *testing.T) {
	eng := New()
	ctx := context.Background()

	err := eng.RegisterPlannerActivity(ctx, "test_plan", engine.ActivityOptions{}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{
			Result: &api.PlanResult{
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

func TestStartWorkflowRejectsReservedRecipeMemoKey(t *testing.T) {
	eng := New()
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	}))

	_, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Memo: map[string]any{startrecipe.MemoKey: "caller"},
	})
	require.ErrorContains(t, err, "reserved")
}

func TestStorageActivityUnlimitedRetryRecoversBeforeWorkflowContinues(t *testing.T) {
	implementation := New()
	var attempts atomic.Int32
	var continued atomic.Bool
	require.NoError(t, implementation.RegisterStorageActivity(
		t.Context(),
		"record",
		engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{MaxAttempts: 3}},
		func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
			if attempts.Add(1) < 4 {
				return nil, errors.New("temporary storage failure")
			}
			return &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}, nil
		},
	))
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			_, err := ctx.ExecuteStorageActivity(engine.StorageActivityCall{
				Name: "record",
				Command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
					Records: []*api.RecordActivityInput{{}},
				}},
				Options: engine.ActivityOptions{
					RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true},
				},
			})
			if err != nil {
				return nil, err
			}
			continued.Store(true)
			return &api.RunOutput{}, nil
		},
	}))
	handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID:       "run",
		Workflow: "workflow",
		Input:    &api.RunInput{},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, int32(4), attempts.Load())
	require.True(t, continued.Load())
}

func TestStorageActivityContractErrorDoesNotRetry(t *testing.T) {
	implementation := New()
	var attempts atomic.Int32
	require.NoError(t, implementation.RegisterStorageActivity(
		t.Context(),
		"record",
		engine.ActivityOptions{},
		func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
			attempts.Add(1)
			return nil, engine.MarkActivityErrorNonRetryable(errors.New("invalid record"))
		},
	))
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			_, err := ctx.ExecuteStorageActivity(engine.StorageActivityCall{
				Name: "record",
				Command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
					Records: []*api.RecordActivityInput{{}},
				}},
				Options: engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}},
			})
			return nil, err
		},
	}))
	handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID:       "run",
		Workflow: "workflow",
		Input:    &api.RunInput{},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.ErrorContains(t, err, "invalid record")
	require.Equal(t, int32(1), attempts.Load())
}

func TestStorageActivityRetriesWithFreshInputAndCopiesOutput(t *testing.T) {
	eng := New().(*eng)
	attempts := 0
	recorded := &api.StorageActivityResult{Append: &api.AppendRecordsResult{
		Records: []storage.AppendResult{{ID: "stored"}},
	}}
	require.NoError(t, eng.RegisterStorageActivity(
		t.Context(),
		"record",
		engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{
			MaxAttempts: 2, InitialInterval: time.Millisecond,
		}},
		func(_ context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
			attempts++
			require.Equal(t, rawjson.Message(`{"value":"original"}`), command.Append.Records[0].Payload)
			command.Append.Records[0].Payload = []byte(`{"value":"changed"}`)
			if attempts == 1 {
				return nil, errors.New("temporary storage failure")
			}
			return recorded, nil
		},
	))
	wfCtx := &wfCtx{ctx: t.Context(), eng: eng}
	result, err := wfCtx.ExecuteStorageActivity(engine.StorageActivityCall{
		Name: "record",
		Command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
			Records: []*api.RecordActivityInput{{Payload: []byte(`{"value":"original"}`)}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	recorded.Append.Records[0].ID = "changed"
	require.Equal(t, "stored", result.Append.Records[0].ID)
}

func TestWorkflowRetryUsesFreshInput(t *testing.T) {
	eng := New()
	attempts := 0
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(_ engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			attempts++
			require.Equal(t, "original", input.Labels["site"])
			input.Labels["site"] = "changed"
			if attempts == 1 {
				return nil, errors.New("retry")
			}
			return &api.RunOutput{RunID: input.RunID}, nil
		},
	}))
	handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Input: &api.RunInput{
			RunID: "run", Labels: map[string]string{"site": "original"},
		},
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Millisecond},
	})
	require.NoError(t, err)
	result, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, "run", result.RunID)
	require.Equal(t, 2, attempts)
}

func TestWorkflowRunTimeoutSetsTimedOutCompletion(t *testing.T) {
	eng := New()
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			<-ctx.Context().Done()
			return nil, ctx.Context().Err()
		},
	}))
	handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Input: &api.RunInput{}, RunTimeout: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	completion, err := eng.QueryRunCompletion(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, engine.RunStatusTimedOut, completion.Status)
	require.False(t, completion.CompletedAt.IsZero())
}

func TestPlannerActivityReturnsNativeOutputContractError(t *testing.T) {
	eng := New()
	ctx := context.Background()
	outputErr := planner.NewOutputContractError(errors.New("invalid planner result"))

	err := eng.RegisterPlannerActivity(ctx, "test_plan_error", engine.ActivityOptions{}, func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return nil, outputErr
	})
	require.NoError(t, err)

	err = eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "test_workflow_error",
		Handler: func(wfCtx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			_, err := wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
				Name:  "test_plan_error",
				Input: &api.PlanActivityInput{},
			})
			return nil, err
		},
	})
	require.NoError(t, err)

	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:       "test-run-error",
		Workflow: "test_workflow_error",
		Input:    &api.RunInput{},
	})
	require.NoError(t, err)

	_, err = handle.Wait(ctx)
	require.ErrorIs(t, err, outputErr)
}

func TestQueryRunCompletionReturnsExactWorkflowOutput(t *testing.T) {
	eng := New()
	want := &api.RunOutput{RunID: "run-1", Suspension: &api.RunSuspension{
		ID: "suspension-1", Version: api.RunSuspensionVersion,
	}}
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return want, nil
		},
	})
	require.NoError(t, err)
	handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow", Input: &api.RunInput{},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)

	got, err := eng.QueryRunCompletion(t.Context(), "run-1")
	require.NoError(t, err)
	require.Equal(t, engine.RunStatusCompleted, got.Status)
	require.False(t, got.CompletedAt.IsZero())
	require.Same(t, want, got.Output)
	require.NoError(t, got.WorkflowError)
}

func TestStartWorkflowBindsExactRequestWhileQueryable(t *testing.T) {
	eng := New()
	release := make(chan struct{})
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow", TaskQueue: "default-queue",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			<-release
			return &api.RunOutput{RunID: "run-1"}, nil
		},
	})
	require.NoError(t, err)
	request := engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow",
		Input: &api.RunInput{RunID: "run-1"},
	}

	original, err := eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	retry, err := eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	require.Same(t, original, retry)

	changed := request
	changed.Input = &api.RunInput{RunID: "run-1", TurnID: "changed"}
	_, err = eng.StartWorkflow(t.Context(), changed)
	var conflict *engine.WorkflowStartConflictError
	require.ErrorAs(t, err, &conflict)

	explicitDefault := request
	explicitDefault.TaskQueue = "default-queue"
	_, err = eng.StartWorkflow(t.Context(), explicitDefault)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)

	close(release)
	_, err = original.Wait(t.Context())
	require.NoError(t, err)
	closedRetry, err := eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	require.Same(t, original, closedRetry)
}

func TestStartWorkflowSnapshotsCallerInput(t *testing.T) {
	eng := New()
	readInput := make(chan struct{})
	observed := make(chan *api.RunInput, 1)
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(_ engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			<-readInput
			observed <- input
			return &api.RunOutput{RunID: input.RunID}, nil
		},
	})
	require.NoError(t, err)
	input := &api.RunInput{
		RunID:  "run-1",
		Labels: map[string]string{"tenant": "accepted"},
		Messages: []*model.Message{{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "accepted"},
			},
		}},
	}
	handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow", Input: input,
	})
	require.NoError(t, err)

	input.Labels["tenant"] = "mutated"
	input.Messages[0].Parts[0] = model.TextPart{Text: "mutated"}
	close(readInput)
	snapshot := <-observed
	require.Equal(t, "accepted", snapshot.Labels["tenant"])
	require.Equal(t, model.TextPart{Text: "accepted"}, snapshot.Messages[0].Parts[0])
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
}

func TestStartWorkflowOwnsExecutionContext(t *testing.T) {
	eng := New()
	started := make(chan struct{})
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			close(started)
			<-ctx.Context().Done()
			return nil, ctx.Context().Err()
		},
	})
	require.NoError(t, err)
	submissionCtx, cancelSubmission := context.WithCancel(t.Context())
	executionHandle, err := eng.StartWorkflow(submissionCtx, engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow", Input: &api.RunInput{RunID: "run-1"},
	})
	require.NoError(t, err)
	<-started

	cancelSubmission()
	select {
	case <-executionHandle.(*handle).done:
		t.Fatal("submission context canceled the accepted workflow")
	case <-time.After(20 * time.Millisecond):
	}
	require.NoError(t, executionHandle.Cancel(t.Context()))
	_, err = executionHandle.Wait(t.Context())
	require.ErrorIs(t, err, context.Canceled)
}

func TestStartChildWorkflowInheritsParentCancellation(t *testing.T) {
	implementation := New().(*eng)
	childStarted := make(chan struct{})
	childCanceled := make(chan struct{})
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "child",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			close(childStarted)
			<-ctx.Context().Done()
			close(childCanceled)
			return nil, ctx.Context().Err()
		},
	}))
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "parent",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			_, err := ctx.StartChildWorkflow(context.Background(), engine.ChildWorkflowRequest{
				ID:       "child-run",
				Workflow: "child",
				Input:    &api.RunInput{RunID: "child-run"},
			})
			if err != nil {
				return nil, err
			}
			<-ctx.Context().Done()
			return nil, ctx.Context().Err()
		},
	}))

	parent, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "parent-run", Workflow: "parent", Input: &api.RunInput{RunID: "parent-run"},
	})
	require.NoError(t, err)
	<-childStarted

	require.NoError(t, parent.Cancel(t.Context()))
	select {
	case <-childCanceled:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not reach child workflow")
	}
	_, err = parent.Wait(t.Context())
	require.ErrorIs(t, err, context.Canceled)
	completion, err := implementation.QueryRunCompletion(t.Context(), "child-run")
	require.NoError(t, err)
	require.ErrorIs(t, completion.WorkflowError, context.Canceled)
	require.Equal(t, engine.RunStatusCanceled, completion.Status)
}

func TestRequestCancellationWaitsForWorkflowHandler(t *testing.T) {
	implementation := New().(*eng)
	workflowStarted := make(chan struct{})
	registerHandler := make(chan struct{})
	handled := make(chan engine.CancellationRequest, 1)
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			close(workflowStarted)
			<-registerHandler
			if err := ctx.SetCancellationHandler(func(handlerCtx engine.WorkflowContext, request engine.CancellationRequest) error {
				require.Equal(t, "run", handlerCtx.WorkflowID())
				handled <- request
				return nil
			}); err != nil {
				return nil, err
			}
			<-ctx.Context().Done()
			return nil, ctx.Context().Err()
		},
	}))
	handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Input: &api.RunInput{RunID: "run"},
	})
	require.NoError(t, err)
	<-workflowStarted

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- implementation.RequestCancellation(t.Context(), engine.CancellationRequest{
			RunID: "run", Reason: "user_requested",
		})
	}()
	select {
	case err := <-requestDone:
		t.Fatalf("cancellation completed before handler registration: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(registerHandler)

	require.Equal(t, engine.CancellationRequest{RunID: "run", Reason: "user_requested"}, <-handled)
	require.NoError(t, <-requestDone)
	_, err = handle.Wait(t.Context())
	require.ErrorIs(t, err, context.Canceled)
}

func TestRequestCancellationRetriesExactReasonAndRejectsConflict(t *testing.T) {
	implementation := New().(*eng)
	registered := make(chan struct{})
	var calls atomic.Int32
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			if err := ctx.SetCancellationHandler(func(engine.WorkflowContext, engine.CancellationRequest) error {
				calls.Add(1)
				return nil
			}); err != nil {
				return nil, err
			}
			close(registered)
			<-ctx.Context().Done()
			return nil, ctx.Context().Err()
		},
	}))
	_, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Input: &api.RunInput{RunID: "run"},
	})
	require.NoError(t, err)
	<-registered

	request := engine.CancellationRequest{RunID: "run", Reason: "user_requested"}
	require.NoError(t, implementation.RequestCancellation(t.Context(), request))
	require.NoError(t, implementation.RequestCancellation(t.Context(), request))
	err = implementation.RequestCancellation(t.Context(), engine.CancellationRequest{
		RunID: "run", Reason: "session_ended",
	})
	var conflict *engine.CancellationConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "run", conflict.RunID)
	require.Equal(t, "session_ended", conflict.Reason)
	require.Equal(t, int32(1), calls.Load())
}

func TestRequestCancellationRejectsWorkflowThatAlreadyCompleted(t *testing.T) {
	implementation := New().(*eng)
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			if err := ctx.SetCancellationHandler(func(engine.WorkflowContext, engine.CancellationRequest) error {
				return nil
			}); err != nil {
				return nil, err
			}
			return &api.RunOutput{RunID: "run"}, nil
		},
	}))
	handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "workflow", Input: &api.RunInput{RunID: "run"},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)

	err = implementation.RequestCancellation(t.Context(), engine.CancellationRequest{
		RunID: "run", Reason: "user_requested",
	})
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
}

func TestStartWorkflowDistinguishesNativeMemoTypes(t *testing.T) {
	eng := New()
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{RunID: "run-1"}, nil
		},
	})
	require.NoError(t, err)
	_, err = eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow", Input: &api.RunInput{RunID: "run-1"},
		Memo: map[string]any{"value": []byte("same bytes")},
	})
	require.NoError(t, err)
	_, err = eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "test_workflow", Input: &api.RunInput{RunID: "run-1"},
		Memo: map[string]any{"value": "same bytes"},
	})
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestStartWorkflowContinuationIdentity(t *testing.T) {
	eng := New()
	err := eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "test_workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{RunID: "run-2"}, nil
		},
	})
	require.NoError(t, err)
	request := engine.WorkflowStartRequest{
		ID: "run-2", Workflow: "test_workflow",
		Input: &api.RunInput{
			RunID: "run-2",
			Continuation: &api.RunContinuationInput{
				Suspension: &api.RunSuspension{ID: "suspension-1", Version: api.RunSuspensionVersion},
				Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
					ID: "clarification-1", Answer: "accepted",
				}},
			},
		},
	}
	original, err := eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	exact, err := eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	require.Same(t, original, exact)

	changed := request
	changed.Input = &api.RunInput{
		RunID: "run-2",
		Continuation: &api.RunContinuationInput{
			Suspension: request.Input.Continuation.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID: "clarification-1", Answer: "changed",
			}},
		},
	}
	_, err = eng.StartWorkflow(t.Context(), changed)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestStartChildWorkflowRejectsEveryExplicitIDReuse(t *testing.T) {
	tests := []struct {
		name    string
		handler engine.WorkflowFunc
		settle  func(t *testing.T, handle engine.ChildWorkflowHandle)
	}{
		{
			name: "open",
			handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
				<-ctx.Context().Done()
				return nil, ctx.Context().Err()
			},
			settle: func(t *testing.T, handle engine.ChildWorkflowHandle) {
				t.Cleanup(func() {
					require.NoError(t, handle.Cancel(t.Context()))
				})
			},
		},
		{
			name: "completed",
			handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
				return &api.RunOutput{}, nil
			},
			settle: func(t *testing.T, handle engine.ChildWorkflowHandle) {
				_, err := handle.Get(t.Context())
				require.NoError(t, err)
			},
		},
		{
			name: "failed",
			handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
				return nil, errors.New("failed")
			},
			settle: func(t *testing.T, handle engine.ChildWorkflowHandle) {
				_, err := handle.Get(t.Context())
				require.ErrorContains(t, err, "failed")
			},
		},
		{
			name: "canceled",
			handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
				<-ctx.Context().Done()
				return nil, ctx.Context().Err()
			},
			settle: func(t *testing.T, handle engine.ChildWorkflowHandle) {
				require.NoError(t, handle.Cancel(t.Context()))
				_, err := handle.Get(t.Context())
				require.ErrorIs(t, err, context.Canceled)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			implementation := New().(*eng)
			require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
				Name:    "child",
				Handler: test.handler,
			}))
			parent := &wfCtx{
				ctx:             t.Context(),
				id:              "parent",
				runID:           "parent",
				eng:             implementation,
				seq:             &sequenceCounter{},
				startedChildren: make(map[string]struct{}),
			}
			handle, err := parent.StartChildWorkflow(t.Context(), engine.ChildWorkflowRequest{
				ID:       "child-id",
				Workflow: "child",
				Input:    &api.RunInput{},
			})
			require.NoError(t, err)
			test.settle(t, handle)

			_, err = parent.StartChildWorkflow(t.Context(), engine.ChildWorkflowRequest{
				ID:       "child-id",
				Workflow: "changed",
				Input:    &api.RunInput{RunID: "changed"},
			})
			require.ErrorIs(t, err, engine.ErrChildWorkflowIDReuse)
			var reuse *engine.ChildWorkflowIDReuseError
			require.ErrorAs(t, err, &reuse)
			require.Equal(t, "child-id", reuse.ID)
		})
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

func TestAgentChildActivityTypedExecution(t *testing.T) {
	eng := New()
	recorded := &api.AgentChildActivityOutput{
		Success: &api.AgentChildActivitySuccess{
			RenderedPrompts: []prompt.RenderEvent{{
				PromptID: "child.prompt",
				Version:  "v1",
			}},
		},
	}
	require.NoError(t, eng.RegisterAgentChildActivity(
		t.Context(),
		"prepare_child",
		engine.ActivityOptions{},
		func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
			return recorded, nil
		},
	))
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "workflow",
		Handler: func(wfCtx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
				Name:  "prepare_child",
				Input: &api.AgentChildActivityInput{},
			})
			if err != nil {
				return nil, err
			}
			if output.Success.RenderedPrompts[0].PromptID != recorded.Success.RenderedPrompts[0].PromptID ||
				output.Success.RenderedPrompts[0].Version != recorded.Success.RenderedPrompts[0].Version {
				return nil, errors.New("agent child activity changed its recorded output")
			}
			return &api.RunOutput{}, nil
		},
	}))
	handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID:       "parent-run",
		Workflow: "workflow",
		Input:    &api.RunInput{},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
}

func TestAgentChildActivityRetriesWithFreshRecordedInput(t *testing.T) {
	eng := New().(*eng)
	var attempts int
	require.NoError(t, eng.RegisterAgentChildActivity(
		t.Context(),
		"prepare_child",
		engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{
			MaxAttempts: 3, InitialInterval: time.Millisecond, BackoffCoefficient: 2,
		}},
		func(_ context.Context, input *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
			attempts++
			require.Equal(t, "one", input.Call.Labels["site"])
			input.Call.Labels["site"] = "mutated"
			if attempts < 3 {
				return nil, errors.New("temporary prompt store failure")
			}
			return &api.AgentChildActivityOutput{Success: &api.AgentChildActivitySuccess{}}, nil
		},
	))
	wfCtx := &wfCtx{ctx: t.Context(), eng: eng}
	output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name: "prepare_child",
		Input: &api.AgentChildActivityInput{Call: api.ToolCall{
			Labels: map[string]string{"site": "one"},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, output.Success)
	require.Equal(t, 3, attempts)
}

func TestAgentChildActivityGivesEachRetryItsOwnStartToCloseTimeout(t *testing.T) {
	eng := New().(*eng)
	var attempts atomic.Int32
	require.NoError(t, eng.RegisterAgentChildActivity(
		t.Context(),
		"prepare_child",
		engine.ActivityOptions{
			StartToCloseTimeout:    10 * time.Millisecond,
			ScheduleToCloseTimeout: time.Second,
			RetryPolicy: engine.RetryPolicy{
				MaxAttempts:     2,
				InitialInterval: time.Millisecond,
			},
		},
		func(ctx context.Context, _ *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
			if attempts.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return &api.AgentChildActivityOutput{Success: &api.AgentChildActivitySuccess{}}, nil
			}
		},
	))
	wfCtx := &wfCtx{ctx: t.Context(), eng: eng}

	output, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name: "prepare_child", Input: &api.AgentChildActivityInput{},
	})

	require.NoError(t, err)
	require.NotNil(t, output.Success)
	require.EqualValues(t, 2, attempts.Load())
}

func TestAgentChildActivityDoesNotRetryPermanentFailure(t *testing.T) {
	eng := New().(*eng)
	var attempts int
	require.NoError(t, eng.RegisterAgentChildActivity(
		t.Context(),
		"prepare_child",
		engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{
			MaxAttempts: 3, InitialInterval: time.Millisecond,
		}},
		func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
			attempts++
			return nil, engine.MarkActivityErrorNonRetryable(errors.New("invalid child request"))
		},
	))
	wfCtx := &wfCtx{ctx: t.Context(), eng: eng}
	_, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
		Name: "prepare_child", Input: &api.AgentChildActivityInput{},
	})
	require.ErrorContains(t, err, "invalid child request")
	require.Equal(t, 1, attempts)
}

func TestAgentChildActivityRejectsOversizedRecordedValues(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		eng := New().(*eng)
		require.NoError(t, eng.RegisterAgentChildActivity(
			t.Context(), "prepare_child", engine.ActivityOptions{},
			func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
				t.Fatal("oversized input reached activity handler")
				return nil, nil
			},
		))
		wfCtx := &wfCtx{ctx: t.Context(), eng: eng}
		_, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
			Name: "prepare_child",
			Input: &api.AgentChildActivityInput{Call: api.ToolCall{
				Payload: rawjson.Message(`"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"`),
			}},
		})
		require.ErrorContains(t, err, "exceed maximum aggregate size")
	})

	t.Run("output", func(t *testing.T) {
		eng := New().(*eng)
		require.NoError(t, eng.RegisterAgentChildActivity(
			t.Context(), "prepare_child", engine.ActivityOptions{},
			func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
				return &api.AgentChildActivityOutput{Success: &api.AgentChildActivitySuccess{
					RenderedPrompts: []prompt.RenderEvent{{
						PromptID: prompt.Ident(strings.Repeat("x", engine.MaxPayloadBytes)),
					}},
				}}, nil
			},
		))
		wfCtx := &wfCtx{ctx: t.Context(), eng: eng}
		_, err := wfCtx.ExecuteAgentChildActivity(engine.AgentChildActivityCall{
			Name: "prepare_child", Input: &api.AgentChildActivityInput{},
		})
		require.ErrorContains(t, err, "exceed maximum aggregate size")
	})
}
