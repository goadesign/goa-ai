package startrecipe

// Child request identity covers every supported launch field and uses a
// different hash domain from an otherwise identical root request.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestChildDigestBindsCompleteAcceptedRequest(t *testing.T) {
	request := engine.ChildWorkflowRequest{
		ID: "child", Workflow: "workflow", TaskQueue: "queue",
		Input: &api.RunInput{RunID: "child", Metadata: map[string]any{"value": "one"}},
	}
	first, err := SnapshotChildRequest(request)
	require.NoError(t, err)
	retry, err := SnapshotChildRequest(first.Request)
	require.NoError(t, err)
	assert.Equal(t, first.Digest, retry.Digest)
	root, err := SnapshotRequest(engine.WorkflowStartRequest{
		ID: request.ID, Workflow: request.Workflow, TaskQueue: request.TaskQueue, Input: request.Input,
	})
	require.NoError(t, err)
	assert.NotEqual(t, root.Digest, first.Digest)
	for _, test := range []struct {
		name   string
		change func(*engine.ChildWorkflowRequest)
	}{
		{"workflow", func(r *engine.ChildWorkflowRequest) { r.Workflow = "different" }},
		{"queue", func(r *engine.ChildWorkflowRequest) { r.TaskQueue = "different" }},
		{"timeout", func(r *engine.ChildWorkflowRequest) { r.RunTimeout = time.Minute }},
		{"retry", func(r *engine.ChildWorkflowRequest) { r.RetryPolicy.MaxAttempts = 2 }},
		{"input", func(r *engine.ChildWorkflowRequest) {
			r.Input = &api.RunInput{RunID: "child", Metadata: map[string]any{"value": "two"}}
		}},
		{"id", func(r *engine.ChildWorkflowRequest) {
			r.ID = "next-child"
			r.Input = &api.RunInput{RunID: "next-child", Metadata: map[string]any{"value": "one"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := first.Request
			test.change(&changed)
			snapshot, err := SnapshotChildRequest(changed)
			require.NoError(t, err)
			assert.NotEqual(t, first.Digest, snapshot.Digest)
		})
	}
	request.Input.Metadata["value"] = "caller mutation"
	assert.Equal(t, "one", first.Request.Input.Metadata["value"])
}
