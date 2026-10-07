package inmem

// This regression uses native child retries to delay a pause until its issuing
// attempt times out. The parent must receive that original report exactly once
// even after the same child handle starts its next execution.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestIndependentRetainsPauseFromPriorIssuedAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	implementation := New()
	secondStarted := make(chan struct{})
	firstReport := make(chan engine.Future[struct{}], 1)
	attempts := 0
	require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "child",
		Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			attempts++
			if attempts == 1 {
				now := w.Now()
				report, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{StartedAt: now.Add(-time.Millisecond), Through: now})
				if err != nil {
					return nil, err
				}
				firstReport <- report
				timer, err := w.NewTimer(w.Context(), time.Hour)
				if err != nil {
					return nil, err
				}
				_, err = timer.Get(w.Context())
				if !errors.Is(err, context.DeadlineExceeded) {
					return nil, fmt.Errorf("expected native run timeout, got %w", err)
				}
				return nil, err
			}
			close(secondStarted)
			return &api.RunOutput{RunID: "child"}, nil
		},
	}))
	require.NoError(t, implementation.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name: "parent",
		Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			accepted := 0
			if err := w.ProviderRecovery().RegisterPauseHandler(func(_ engine.WorkflowContext, child string, _ engine.ProviderRecoveryPaused) error {
				if child != "child" {
					return fmt.Errorf("wrong child %q", child)
				}
				accepted++
				return nil
			}); err != nil {
				return nil, err
			}
			child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
				ID: "child", Workflow: "child", TaskQueue: "test", Input: &api.RunInput{RunID: "child"},
				RunTimeout: 10 * time.Millisecond, RetryPolicy: engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Millisecond},
			})
			if err != nil {
				return nil, err
			}
			// Hold parent dispatch until the engine starts the next issued attempt.
			select {
			case <-secondStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if _, err := child.Get(w.Context()); err != nil {
				return nil, err
			}
			report := <-firstReport
			_, err = report.Get(ctx)
			if err != nil {
				return nil, fmt.Errorf("prior issued attempt pause rejected (accepted=%d): %w", accepted, err)
			}
			if accepted != 1 {
				return nil, fmt.Errorf("accepted %d prior attempt pauses, want 1", accepted)
			}
			return &api.RunOutput{RunID: "parent"}, nil
		},
	}))
	handle, err := implementation.StartWorkflow(ctx, engine.WorkflowStartRequest{ID: "parent", Workflow: "parent", TaskQueue: "test", Input: &api.RunInput{RunID: "parent"}})
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
}
