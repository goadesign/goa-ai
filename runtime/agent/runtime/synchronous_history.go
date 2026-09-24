package runtime

// Callback starts publish their empty history and exact start command before
// executing user code. Each storage command is retried unchanged after a
// temporary failure, including a lost reply after the store committed it.

import (
	"context"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/storage"
)

// synchronousSeedWriter uses the existing non-workflow retry contract only for
// RunOneShot. General caller preparation keeps its context-aware error behavior.
type synchronousSeedWriter struct {
	r *Runtime
}

func (w synchronousSeedWriter) BeginRunSeed(ctx context.Context, declaration storage.SeedDeclaration) (storage.RunSeed, error) {
	result, err := w.r.storageCommandUntilApplied(ctx, &api.StorageActivityCommand{SeedBegin: &declaration})
	if err != nil {
		return storage.RunSeed{}, err
	}
	return *result.SeedBegin, nil
}

func (w synchronousSeedWriter) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	result, err := w.r.storageCommandUntilApplied(ctx, &api.StorageActivityCommand{SeedAppend: &command})
	if err != nil {
		return "", err
	}
	return result.SeedAppend.EndID, nil
}

func (w synchronousSeedWriter) PublishRunSeed(ctx context.Context, publication storage.SeedPublication) error {
	_, err := w.r.storageCommandUntilApplied(ctx, &api.StorageActivityCommand{SeedPublish: &publication})
	return err
}
