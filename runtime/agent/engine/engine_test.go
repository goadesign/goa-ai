package engine

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
)

func TestValidateWorkflowStartRequest(t *testing.T) {
	valid := WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     &api.RunInput{RunID: "run-1"},
	}
	require.NoError(t, ValidateWorkflowStartRequest(valid))

	tests := []struct {
		name    string
		wantErr string
		change  func(*WorkflowStartRequest)
	}{
		{name: "missing id", wantErr: "workflow id is required", change: func(req *WorkflowStartRequest) {
			req.ID = ""
		}},
		{name: "missing workflow", wantErr: "workflow name is required", change: func(req *WorkflowStartRequest) {
			req.Workflow = ""
		}},
		{name: "missing task queue", wantErr: "workflow task queue is required", change: func(req *WorkflowStartRequest) {
			req.TaskQueue = ""
		}},
		{name: "missing input", wantErr: "workflow input is required", change: func(req *WorkflowStartRequest) {
			req.Input = nil
		}},
		{name: "mismatched run id", wantErr: "workflow id must match input run id", change: func(req *WorkflowStartRequest) {
			req.Input = &api.RunInput{RunID: "other-run"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.change(&req)
			require.EqualError(t, ValidateWorkflowStartRequest(req), test.wantErr)
		})
	}
}

func TestValidateChildWorkflowRequest(t *testing.T) {
	valid := ChildWorkflowRequest{
		ID:        "child-1",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     &api.RunInput{RunID: "child-1"},
	}
	require.NoError(t, ValidateChildWorkflowRequest(valid))

	tests := []struct {
		name    string
		wantErr string
		change  func(*ChildWorkflowRequest)
	}{
		{name: "missing id", wantErr: "child workflow id is required", change: func(req *ChildWorkflowRequest) {
			req.ID = ""
		}},
		{name: "missing workflow", wantErr: "child workflow name is required", change: func(req *ChildWorkflowRequest) {
			req.Workflow = ""
		}},
		{name: "missing task queue", wantErr: "child workflow task queue is required", change: func(req *ChildWorkflowRequest) {
			req.TaskQueue = ""
		}},
		{name: "missing input", wantErr: "child workflow input is required", change: func(req *ChildWorkflowRequest) {
			req.Input = nil
		}},
		{name: "mismatched run id", wantErr: "child workflow id must match input run id", change: func(req *ChildWorkflowRequest) {
			req.Input = &api.RunInput{RunID: "other-run"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.change(&req)
			require.EqualError(t, ValidateChildWorkflowRequest(req), test.wantErr)
		})
	}
}

func TestValidateWorkflowOptionsForRootAndChild(t *testing.T) {
	tests := []struct {
		name       string
		runTimeout time.Duration
		retry      RetryPolicy
		wantErr    string
	}{
		{name: "zero policy"},
		{
			name: "finite attempts",
			retry: RetryPolicy{
				MaxAttempts:        2,
				InitialInterval:    time.Second,
				BackoffCoefficient: 2,
			},
		},
		{
			name: "unlimited attempts",
			retry: RetryPolicy{
				UnlimitedAttempts: true,
				InitialInterval:   time.Second,
			},
		},
		{name: "negative run timeout", runTimeout: -time.Second, wantErr: "workflow run timeout must not be negative"},
		{name: "negative attempts", retry: RetryPolicy{MaxAttempts: -1}, wantErr: "workflow retry max attempts must not be negative"},
		{name: "negative interval", retry: RetryPolicy{InitialInterval: -time.Second}, wantErr: "workflow retry initial interval must not be negative"},
		{name: "negative backoff", retry: RetryPolicy{BackoffCoefficient: -1}, wantErr: "workflow retry backoff coefficient must be zero or at least one"},
		{name: "fractional backoff", retry: RetryPolicy{BackoffCoefficient: 0.5}, wantErr: "workflow retry backoff coefficient must be zero or at least one"},
		{name: "NaN backoff", retry: RetryPolicy{BackoffCoefficient: math.NaN()}, wantErr: "workflow retry backoff coefficient must be zero or at least one"},
		{name: "infinite backoff", retry: RetryPolicy{BackoffCoefficient: math.Inf(1)}, wantErr: "workflow retry backoff coefficient must be zero or at least one"},
		{
			name:    "conflicting attempt controls",
			retry:   RetryPolicy{MaxAttempts: 2, UnlimitedAttempts: true},
			wantErr: "workflow retry cannot set both unlimited attempts and max attempts",
		},
		{
			name:    "interval without attempt control",
			retry:   RetryPolicy{InitialInterval: time.Second},
			wantErr: "workflow retry timing requires max attempts or unlimited attempts",
		},
		{
			name:    "backoff without attempt control",
			retry:   RetryPolicy{BackoffCoefficient: 2},
			wantErr: "workflow retry timing requires max attempts or unlimited attempts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := WorkflowStartRequest{
				ID: "run", Workflow: "agent.workflow", TaskQueue: "agent.queue",
				Input: &api.RunInput{RunID: "run"}, RunTimeout: test.runTimeout,
				RetryPolicy: test.retry,
			}
			child := ChildWorkflowRequest{
				ID: "child", Workflow: "agent.workflow", TaskQueue: "agent.queue",
				Input: &api.RunInput{RunID: "child"}, RunTimeout: test.runTimeout,
				RetryPolicy: test.retry,
			}
			if test.wantErr == "" {
				require.NoError(t, ValidateWorkflowStartRequest(root))
				require.NoError(t, ValidateChildWorkflowRequest(child))
				return
			}
			require.EqualError(t, ValidateWorkflowStartRequest(root), test.wantErr)
			require.EqualError(t, ValidateChildWorkflowRequest(child), test.wantErr)
		})
	}
}
