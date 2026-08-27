package gateway

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// remoteProvider implements raw transport operations using caller-supplied
	// RPC functions over normalized model types.
	remoteProvider struct {
		doComplete func(ctx context.Context, req *model.Request) (*model.Response, error)
		doStream   func(ctx context.Context, req *model.Request) (model.Streamer, error)
	}

	// countingRemoteProvider adds the optional exact token-count operation only
	// when the transport supplies it.
	countingRemoteProvider struct {
		*remoteProvider
		doCount func(ctx context.Context, req *model.Request) (model.TokenCount, error)
	}
)

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

// NewCountingRemoteClient constructs a validated client whose remote provider
// also exposes exact token counting.
func NewCountingRemoteClient(
	complete func(ctx context.Context, req *model.Request) (*model.Response, error),
	stream func(ctx context.Context, req *model.Request) (model.Streamer, error),
	count func(ctx context.Context, req *model.Request) (model.TokenCount, error),
) (model.Client, error) {
	raw, err := NewCountingRemoteProvider(complete, stream, count)
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
	return newRemoteProvider(complete, stream)
}

// NewCountingRemoteProvider constructs a raw transport provider that preserves
// the remote endpoint's exact token-count capability.
func NewCountingRemoteProvider(
	complete func(ctx context.Context, req *model.Request) (*model.Response, error),
	stream func(ctx context.Context, req *model.Request) (model.Streamer, error),
	count func(ctx context.Context, req *model.Request) (model.TokenCount, error),
) (model.Provider, error) {
	if count == nil {
		return nil, errors.New("gateway: count callback is required")
	}
	raw, err := newRemoteProvider(complete, stream)
	if err != nil {
		return nil, err
	}
	return &countingRemoteProvider{
		remoteProvider: raw,
		doCount:        count,
	}, nil
}

func (c *remoteProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	return c.doComplete(ctx, req)
}

func (c *remoteProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	return c.doStream(ctx, req)
}

// CountTokens forwards one exact token-count request to the remote endpoint.
func (c *countingRemoteProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	return c.doCount(ctx, req)
}

// newRemoteProvider validates the two required remote completion operations.
func newRemoteProvider(
	complete func(ctx context.Context, req *model.Request) (*model.Response, error),
	stream func(ctx context.Context, req *model.Request) (model.Streamer, error),
) (*remoteProvider, error) {
	if complete == nil {
		return nil, errors.New("gateway: complete callback is required")
	}
	if stream == nil {
		return nil, errors.New("gateway: stream callback is required")
	}
	return &remoteProvider{doComplete: complete, doStream: stream}, nil
}
