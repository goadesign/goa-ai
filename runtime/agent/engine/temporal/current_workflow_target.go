// Package temporal reads workflow routing from Temporal's saved task queue data.
// Callers use this read before accepting a new execution; it does not inspect
// poller health, change routing, or replace a target already retained by a run.
package temporal

import (
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"

	"goa.design/goa-ai/runtime/agent/engine"
)

// CurrentWorkflowTarget returns the deployment and build currently selected for
// new workflows on queue. A nil target means Temporal reports unversioned routing
// or no current version. This read uses saved routing even when no worker polls
// the queue; an unavailable or malformed owner response returns an error.
func (e *Engine) CurrentWorkflowTarget(ctx context.Context, queue string) (*engine.WorkerDeploymentVersion, error) {
	response, err := e.client.DescribeTaskQueue(ctx, queue, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
	if err != nil {
		return nil, fmt.Errorf("describe workflow task queue: %w", err)
	}
	if response == nil {
		return nil, errors.New("temporal returned no workflow task queue description")
	}
	current := response.GetVersioningInfo().GetCurrentDeploymentVersion()
	if current == nil {
		return nil, nil
	}
	if current.DeploymentName == "" || current.BuildId == "" {
		return nil, errors.New("temporal returned an incomplete current workflow target")
	}
	return &engine.WorkerDeploymentVersion{DeploymentName: current.DeploymentName, BuildID: current.BuildId}, nil
}
