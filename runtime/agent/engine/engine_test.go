package engine

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateWorkflowStartRequestRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name    string
		request WorkflowStartRequest
	}{
		{name: "negative run timeout", request: WorkflowStartRequest{RunTimeout: -time.Second}},
		{name: "negative attempts", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{MaxAttempts: -1}}},
		{name: "negative retry interval", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{InitialInterval: -time.Second}}},
		{name: "negative backoff", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{BackoffCoefficient: -1}}},
		{name: "fractional backoff", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{BackoffCoefficient: 0.5}}},
		{name: "NaN backoff", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{BackoffCoefficient: math.NaN()}}},
		{name: "infinite backoff", request: WorkflowStartRequest{RetryPolicy: RetryPolicy{BackoffCoefficient: math.Inf(1)}}},
		{name: "conflicting attempt controls", request: WorkflowStartRequest{
			RetryPolicy: RetryPolicy{MaxAttempts: 2, UnlimitedAttempts: true},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, ValidateWorkflowStartRequest(test.request))
		})
	}
	require.NoError(t, ValidateWorkflowStartRequest(WorkflowStartRequest{
		RunTimeout: time.Second,
		RetryPolicy: RetryPolicy{
			MaxAttempts:        2,
			InitialInterval:    time.Second,
			BackoffCoefficient: 1,
		},
	}))
}
