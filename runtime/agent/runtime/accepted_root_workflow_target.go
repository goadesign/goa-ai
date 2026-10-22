// Package runtime starts new engine workflows for saved answers and cancellation.
// Applications supply the immutable target from their accepted root request so
// these new workflows do not select a newer build after the original suspends.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// WithAcceptedRootWorkflowTarget supplies the owner read used for new saved
// continuation and cancellation workflows. The ID is the original root
// invocation, including when the current run is a successor or child. Nil means
// that exact accepted root used unversioned workers; errors prevent a new start.
// Already published continuation requests retain their saved launch target.
// Runtimes without this option make no deployment owner read.
func WithAcceptedRootWorkflowTarget(read func(context.Context, string) (*engine.WorkerDeploymentVersion, error)) RuntimeOption {
	if read == nil {
		panic("runtime: accepted root workflow target reader is required")
	}
	return func(options *Options) { options.acceptedRootWorkflowTarget = read }
}

// acceptedWorkflowTarget follows validated parents and admitted successors to
// the original root invocation. Only that root's owner may select its build;
// neither the current queue nor a later execution supplies this value.
func (r *Runtime) acceptedWorkflowTarget(ctx context.Context, owner session.RunMeta) (*engine.WorkerDeploymentVersion, error) {
	if r.acceptedRootWorkflowTarget == nil {
		return nil, nil
	}
	root, err := r.cancellationRoot(ctx, owner)
	if err != nil {
		return nil, err
	}
	origin, ok := root.Origin.Invocation.AsRoot()
	if !ok || origin.RunID == "" {
		return nil, storage.NewContractError(errors.New("saved workflow has no original root invocation"))
	}
	target, err := r.acceptedRootWorkflowTarget(ctx, origin.RunID)
	if err != nil {
		return nil, fmt.Errorf("read accepted root workflow target: %w", err)
	}
	if target == nil {
		return nil, nil
	}
	if target.DeploymentName == "" || target.BuildID == "" || !utf8.ValidString(target.DeploymentName) || !utf8.ValidString(target.BuildID) {
		return nil, storage.NewContractError(errors.New("accepted root workflow target requires valid deployment and build names"))
	}
	owned := *target
	return &owned, nil
}
