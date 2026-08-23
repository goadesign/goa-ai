package gateway

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/model"
)

// remoteProvider implements raw transport operations using caller-supplied RPC
// functions over normalized model types.
type remoteProvider struct {
	doComplete func(ctx context.Context, req *model.Request) (*model.Response, error)
	doStream   func(ctx context.Context, req *model.Request) (model.Streamer, error)
}

// NewRemoteClient constructs a validated model.Client around a raw remote
// provider. The caller therefore validates provider and gateway middleware
// output after it crosses the transport.
func NewRemoteClient(
	complete func(ctx context.Context, req *model.Request) (*model.Response, error),
	stream func(ctx context.Context, req *model.Request) (model.Streamer, error),
) (model.Client, error) {
	raw, err := NewRemoteProvider(complete, stream)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewRemoteProvider constructs the raw transport provider for provider-side
// composition.
func NewRemoteProvider(
	complete func(ctx context.Context, req *model.Request) (*model.Response, error),
	stream func(ctx context.Context, req *model.Request) (model.Streamer, error),
) (model.Provider, error) {
	if complete == nil {
		return nil, errors.New("gateway: complete callback is required")
	}
	if stream == nil {
		return nil, errors.New("gateway: stream callback is required")
	}
	return &remoteProvider{doComplete: complete, doStream: stream}, nil
}

func (c *remoteProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	return c.doComplete(ctx, req)
}

func (c *remoteProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	return c.doStream(ctx, req)
}
