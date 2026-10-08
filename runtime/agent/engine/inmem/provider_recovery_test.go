package inmem

// These tests use issued children and real workflow waits. The unprotected
// callback state deliberately makes the race detector check serialization
// between callbacks and the ordinary workflow handler.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestProviderRecoverySerializesWorkflowWaits(t *testing.T) {
	for _, wait := range []string{"await", "timer", "planner", "storage", "tool", "tool_future", "prepare_child", "child"} {
		t.Run(wait, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			implementation := New()
			queued := make(chan struct{})
			applied := make(chan struct{})
			var shared int
			registerRecoveryActivities(t, implementation, applied)
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "child",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					var replied bool
					control, err := w.ProviderRecovery().Open(w, recoveryFailure("publication"), func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
						if _, ok := message.(engine.ProviderRecoveryPermission); !ok {
							return fmt.Errorf("unexpected reply %T", message)
						}
						replied = true
						return nil
					})
					if err != nil {
						return nil, err
					}
					if _, err := control.Send(w, engine.ProviderWaitRequest{Delay: time.Second}); err != nil {
						return nil, err
					}
					now := w.Now()
					if _, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{StartedAt: now, Through: now}); err != nil {
						return nil, err
					}
					close(queued)
					err = w.Await(func() bool { return replied })
					return &api.RunOutput{RunID: "child"}, err
				},
			}))
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "root",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					if err := w.ProviderRecovery().Register(func(w engine.WorkflowContext, child string, _ engine.ProviderRecoveryFailure, control engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
						assert.Equal(t, "issued-child", child)
						assert.Equal(t, 10000, shared)
						shared++
						return func(w engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
							assert.IsType(t, engine.ProviderWaitRequest{}, message)
							assert.Equal(t, 10001, shared)
							shared++
							_, err := control.Send(w, engine.ProviderRecoveryPermission{ExpiresAt: w.Now().Add(time.Minute)})
							return err
						}, nil
					}); err != nil {
						return nil, err
					}
					if err := w.ProviderRecovery().RegisterPauseHandler(func(_ engine.WorkflowContext, child string, _ engine.ProviderRecoveryPaused) error {
						assert.Equal(t, "issued-child", child)
						assert.Equal(t, 10002, shared)
						shared++
						close(applied)
						return nil
					}); err != nil {
						return nil, err
					}
					child, err := w.StartChildWorkflow(w.Context(), recoveryChildRequest("issued-child", "child"))
					if err != nil {
						return nil, err
					}
					select {
					case <-queued:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					// A callback on any other goroutine would race this loop.
					for range 10000 {
						shared++
						runtime.Gosched()
					}
					assert.Equal(t, 10000, shared)
					if err := recoveryWait(w, child, wait, func() bool { return shared == 10003 }); err != nil {
						return nil, err
					}
					assert.Equal(t, 10003, shared)
					_, err = child.Get(w.Context())
					return &api.RunOutput{RunID: "root"}, err
				},
			}))
			h, err := implementation.StartWorkflow(ctx, recoveryStartRequest("root", "root"))
			require.NoError(t, err)
			_, err = h.Wait(ctx)
			require.NoError(t, err)
			assert.Equal(t, 10003, shared)
		})
	}
}

func TestProviderRecoveryWrapperForwardingAndPauseOwnership(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint("registered=", registered), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			implementation := New()
			var rootChildren, rootPublications []string
			var rootPauses int
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "leaf",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					assert.True(t, w.ProviderRecovery().HasParent())
					replies := 0
					for _, publication := range []string{"first", "second"} {
						request, err := w.ProviderRecovery().Open(w, recoveryFailure(publication), func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
							assert.IsType(t, engine.ProviderRecoverySettled{}, message)
							replies++
							return nil
						})
						if err != nil {
							return nil, err
						}
						if _, err := request.Send(w, engine.ProviderAttemptSucceeded{}); err != nil {
							return nil, err
						}
						now := w.Now()
						if _, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{StartedAt: now.Add(-time.Millisecond), Through: now}); err != nil {
							return nil, err
						}
					}
					return &api.RunOutput{RunID: "leaf"}, w.Await(func() bool { return replies == 2 })
				},
			}))
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "wrapper",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					assert.True(t, w.ProviderRecovery().HasParent())
					if registered {
						err := w.ProviderRecovery().Register(func(w engine.WorkflowContext, child string, _ engine.ProviderRecoveryFailure, incoming engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
							assert.Equal(t, "leaf", child)
							onward, err := incoming.Forward(w, func(w engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
								_, err := incoming.Send(w, message)
								return err
							})
							if err != nil {
								return nil, err
							}
							return func(w engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
								_, err := onward.Send(w, message)
								return err
							}, nil
						})
						if err != nil {
							return nil, err
						}
					}
					child, err := w.StartChildWorkflow(w.Context(), recoveryChildRequest("leaf", "leaf"))
					if err != nil {
						return nil, err
					}
					_, err = child.Get(w.Context())
					if err != nil {
						return nil, err
					}
					var retained int
					err = w.ProviderRecovery().RegisterPauseHandler(func(_ engine.WorkflowContext, child string, _ engine.ProviderRecoveryPaused) error {
						assert.Equal(t, "leaf", child)
						retained++
						return nil
					})
					assert.Equal(t, 2, retained)
					return &api.RunOutput{RunID: "wrapper"}, err
				},
			}))
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "root",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					assert.False(t, w.ProviderRecovery().HasParent())
					err := w.ProviderRecovery().Register(func(_ engine.WorkflowContext, child string, failure engine.ProviderRecoveryFailure, incoming engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
						rootChildren = append(rootChildren, child)
						rootPublications = append(rootPublications, failure.PublicationBatchID)
						return func(w engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
							assert.IsType(t, engine.ProviderAttemptSucceeded{}, message)
							_, err := incoming.Send(w, engine.ProviderRecoverySettled{})
							return err
						}, nil
					})
					if err != nil {
						return nil, err
					}
					if err := w.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
						rootPauses++
						return nil
					}); err != nil {
						return nil, err
					}
					child, err := w.StartChildWorkflow(w.Context(), recoveryChildRequest("wrapper", "wrapper"))
					if err != nil {
						return nil, err
					}
					_, err = child.Get(w.Context())
					return &api.RunOutput{RunID: "root"}, err
				},
			}))
			h, err := implementation.StartWorkflow(ctx, recoveryStartRequest("root", "root"))
			require.NoError(t, err)
			_, err = h.Wait(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{"wrapper", "wrapper"}, rootChildren)
			assert.Equal(t, []string{"first", "second"}, rootPublications)
			assert.Zero(t, rootPauses)
		})
	}
}

func TestProviderRecoveryDrainsCompletionAndStopsCanceledWait(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			implementation := New()
			issued := make(chan engine.ChildWorkflowHandle, 1)
			reported := make(chan struct{})
			release := make(chan struct{})
			var accepted bool
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "child",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					now := w.Now()
					if _, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{StartedAt: now, Through: now}); err != nil {
						return nil, err
					}
					close(reported)
					if canceled {
						return nil, context.Canceled
					}
					return &api.RunOutput{RunID: "child"}, nil
				},
			}))
			require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "root",
				Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					if err := w.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
						accepted = true
						return nil
					}); err != nil {
						return nil, err
					}
					child, err := w.StartChildWorkflow(w.Context(), recoveryChildRequest("child", "child"))
					if err != nil {
						return nil, err
					}
					issued <- child
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if !canceled {
						_, err = child.Get(w.Context())
					}
					return &api.RunOutput{RunID: "root"}, err
				},
			}))
			h, err := implementation.StartWorkflow(ctx, recoveryStartRequest("root", "root"))
			require.NoError(t, err)
			var child engine.ChildWorkflowHandle
			select {
			case child = <-issued:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-reported:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if canceled {
				// Observe the native handle without driving the parent's queue.
				_, err = child.(*childHandle).h.Wait(ctx)
				require.ErrorIs(t, err, context.Canceled)
			} else {
				assert.False(t, child.IsReady(), "ordinary completion must wait for parent acceptance")
			}
			close(release)
			_, err = h.Wait(ctx)
			require.NoError(t, err)
			assert.True(t, accepted)
			if !canceled {
				_, err = child.(*childHandle).h.Wait(ctx)
				require.NoError(t, err)
			}
		})
	}
}

func registerRecoveryActivities(t *testing.T, implementation engine.Engine, accepted <-chan struct{}) {
	t.Helper()
	wait := func(ctx context.Context) error {
		select {
		case <-accepted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	require.NoError(t, implementation.RegisterPlannerActivity(t.Context(), "planner", engine.ActivityOptions{},
		func(ctx context.Context, _ *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
			return &api.PlanActivityOutput{PublicationBatchID: "done"}, wait(ctx)
		}))
	require.NoError(t, implementation.RegisterStorageActivity(t.Context(), "storage", engine.ActivityOptions{},
		func(ctx context.Context, _ *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
			return &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}, wait(ctx)
		}))
	require.NoError(t, implementation.RegisterExecuteToolActivity(t.Context(), "tool", engine.ActivityOptions{},
		func(ctx context.Context, _ *api.ToolInput) (*api.ToolOutput, error) {
			return &api.ToolOutput{}, wait(ctx)
		}))
	require.NoError(t, implementation.RegisterAgentChildActivity(t.Context(), "prepare", engine.ActivityOptions{},
		func(ctx context.Context, _ *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error) {
			return &api.AgentChildActivityOutput{}, wait(ctx)
		}))
}

func recoveryWait(w engine.WorkflowContext, child engine.ChildWorkflowHandle, kind string, ready func() bool) error {
	switch kind {
	case "await":
		return w.Await(ready)
	case "timer":
		timer, err := w.NewTimer(w.Context(), time.Millisecond)
		if err != nil {
			return err
		}
		_, err = timer.Get(w.Context())
		return err
	case "planner":
		_, err := w.ExecutePlannerActivity(engine.PlannerActivityCall{Name: "planner", Input: &api.PlanActivityInput{RunID: "root"}})
		return err
	case "storage":
		_, err := w.ExecuteStorageActivity(engine.StorageActivityCall{Name: "storage", Command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{}}})
		return err
	case "tool":
		_, err := w.ExecuteToolActivity(engine.ToolActivityCall{Name: "tool", Input: &api.ToolInput{}})
		return err
	case "tool_future":
		future, err := w.ExecuteToolActivityAsync(engine.ToolActivityCall{Name: "tool", Input: &api.ToolInput{}})
		if err != nil {
			return err
		}
		_, err = future.Get(w.Context())
		return err
	case "prepare_child":
		_, err := w.ExecuteAgentChildActivity(engine.AgentChildActivityCall{Name: "prepare", Input: &api.AgentChildActivityInput{}})
		return err
	case "child":
		_, err := child.Get(w.Context())
		return err
	default:
		return errors.New("unknown test wait")
	}
}

func recoveryFailure(publication string) engine.ProviderRecoveryFailure {
	now := time.Now()
	return engine.ProviderRecoveryFailure{
		PublicationBatchID: publication,
		StartedAt:          now.Add(-time.Millisecond),
		EndedAt:            now,
		Err: model.NewProviderError("synthetic", "complete", 503, model.ProviderErrorKindUnavailable,
			"busy", "capacity", "provider-request", true, nil),
	}
}

func recoveryChildRequest(id, workflow string) engine.ChildWorkflowRequest {
	return engine.ChildWorkflowRequest{
		ID: id, Workflow: workflow, TaskQueue: "test",
		Input: &api.RunInput{RunID: id},
	}
}

func recoveryStartRequest(id, workflow string) engine.WorkflowStartRequest {
	return engine.WorkflowStartRequest{
		ID: id, Workflow: workflow, TaskQueue: "test", Input: &api.RunInput{RunID: id},
	}
}
