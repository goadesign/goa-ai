package gateway

import (
	"context"

	"goa.design/goa-ai/runtime/agent/model"
)

// RemoteClient implements model.Client using caller-supplied RPC functions
// that operate on normalized runtime model types. This keeps the adapter agnostic
// of the concrete transport (HTTP/GRPC) and generated packages.
type RemoteClient struct {
	doComplete func(ctx context.Context, req model.Request) (model.Response, error)
	doStream   func(ctx context.Context, req model.Request) (model.Streamer, error)
}

// NewRemoteClient constructs a model.Client from normalized RPC functions.
func NewRemoteClient(
	complete func(ctx context.Context, req model.Request) (model.Response, error),
	stream func(ctx context.Context, req model.Request) (model.Streamer, error),
) *RemoteClient {
	return &RemoteClient{doComplete: complete, doStream: stream}
}

func (c *RemoteClient) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	return c.doComplete(ctx, req)
}

func (c *RemoteClient) Stream(ctx context.Context, req model.Request) (model.Streamer, error) {
	return c.doStream(ctx, req)
}
