// Package gateway provides a Goa-generated gRPC server and client transport
// for Server, Complete, Stream, and CountTokens.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	gengrpcclient "goa.design/goa-ai/features/model/gateway/gen/grpc/model_gateway/client"
	genmodelgatewaypb "goa.design/goa-ai/features/model/gateway/gen/grpc/model_gateway/pb"
	gengrpcserver "goa.design/goa-ai/features/model/gateway/gen/grpc/model_gateway/server"
	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type (
	// grpcService implements the Goa-generated model_gateway.Service over a
	// gateway.Server.
	grpcService struct {
		// server is the underlying gateway server handling Complete, Stream, and CountTokens.
		server *Server
	}

	// grpcStreamer adapts a Goa-generated StreamClientStream into a model.Streamer.
	grpcStreamer struct {
		// ctx is the cancelable stream context.
		ctx context.Context
		// cancel aborts the in-flight gRPC stream when Close is called.
		cancel context.CancelFunc
		// stream is the Goa-generated gRPC client stream.
		stream genmodelgateway.StreamClientStream

		// mu guards terminal state (response, finalErr, finished).
		mu sync.RWMutex
		// response holds the terminal provider response after clean EOF.
		response *model.Response
		// finalErr holds the terminal stream error when the stream failed.
		finalErr error
		// finished reports whether the stream has reached a terminal state.
		finished bool
	}
)

// RegisterGRPCServer registers the Goa-generated model_gateway gRPC server
// backed by server on registrar.
func RegisterGRPCServer(registrar grpc.ServiceRegistrar, server *Server) {
	if registrar == nil {
		panic("gateway: gRPC service registrar is required")
	}
	if server == nil {
		panic("gateway: server is required")
	}
	svc := &grpcService{server: server}
	endpoints := genmodelgateway.NewEndpoints(svc)
	genmodelgatewaypb.RegisterModelGatewayServer(registrar, gengrpcserver.New(endpoints, nil, nil))
}

// NewGRPCClient constructs a validated model.Client backed by the Goa-generated
// gRPC transport over conn.
func NewGRPCClient(conn *grpc.ClientConn, opts ...grpc.CallOption) (model.Client, error) {
	raw, err := newGRPCProvider(conn, opts...)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// Complete decodes a typed wire request, invokes the gateway Server unary
// chain, and returns the typed response or structured error so the remote
// client can reconstruct exact OutputValidationError, ProviderError, and
// RetainedUsage error trees across the process boundary.
func (s *grpcService) Complete(
	ctx context.Context,
	payload *genmodelgateway.ModelRequest,
) (*genmodelgateway.CompleteResult, error) {
	req, err := fromGenRequest(payload)
	if err != nil {
		return &genmodelgateway.CompleteResult{
			Outcome: genmodelgateway.NewCompleteOutcomeError(toGenError(err)),
		}, nil
	}
	resp, err := s.server.Complete(ctx, req)
	if err != nil {
		return &genmodelgateway.CompleteResult{
			Outcome: genmodelgateway.NewCompleteOutcomeError(toGenError(err)),
		}, nil
	}
	genResp, err := toGenResponse(resp)
	if err != nil {
		return &genmodelgateway.CompleteResult{
			Outcome: genmodelgateway.NewCompleteOutcomeError(toGenError(err)),
		}, nil
	}
	return &genmodelgateway.CompleteResult{
		Outcome: genmodelgateway.NewCompleteOutcomeResponse(genResp),
	}, nil
}

// Stream decodes a typed wire request, invokes the gateway Server stream chain,
// forwards each chunk frame, and terminates with a response or error frame.
func (s *grpcService) Stream(
	ctx context.Context,
	payload *genmodelgateway.ModelRequest,
	stream genmodelgateway.StreamServerStream,
) error {
	req, err := fromGenRequest(payload)
	if err != nil {
		return stream.Send(&genmodelgateway.StreamFrame{
			Payload: genmodelgateway.NewStreamFramePayloadError(toGenError(err)),
		})
	}
	var sendErr error
	resp, err := s.server.Stream(ctx, req, func(chunk model.Chunk) error {
		gc, encErr := toGenChunk(chunk)
		if encErr != nil {
			return encErr
		}
		if err := stream.Send(&genmodelgateway.StreamFrame{
			Payload: genmodelgateway.NewStreamFramePayloadChunk(gc),
		}); err != nil {
			sendErr = err
			return err
		}
		return nil
	})
	if sendErr != nil {
		return sendErr
	}
	if err != nil {
		return stream.Send(&genmodelgateway.StreamFrame{
			Payload: genmodelgateway.NewStreamFramePayloadError(toGenError(err)),
		})
	}
	genResp, encErr := toGenResponse(resp)
	if encErr != nil {
		return stream.Send(&genmodelgateway.StreamFrame{
			Payload: genmodelgateway.NewStreamFramePayloadError(toGenError(encErr)),
		})
	}
	return stream.Send(&genmodelgateway.StreamFrame{
		Payload: genmodelgateway.NewStreamFramePayloadResponse(genResp),
	})
}

// CountTokens decodes a typed wire request, invokes the gateway Server token
// counter, and returns the typed count or structured error.
func (s *grpcService) CountTokens(
	ctx context.Context,
	payload *genmodelgateway.ModelRequest,
) (*genmodelgateway.CountTokensResult, error) {
	req, err := fromGenRequest(payload)
	if err != nil {
		return &genmodelgateway.CountTokensResult{
			Outcome: genmodelgateway.NewCountTokensOutcomeError(toGenError(err)),
		}, nil
	}
	count, err := s.server.CountTokens(ctx, req)
	if err != nil {
		return &genmodelgateway.CountTokensResult{
			Outcome: genmodelgateway.NewCountTokensOutcomeError(toGenError(err)),
		}, nil
	}
	return &genmodelgateway.CountTokensResult{
		Outcome: genmodelgateway.NewCountTokensOutcomeCount(toGenTokenCount(count)),
	}, nil
}

// Recv reads the next typed frame from the gRPC stream and translates it into a
// model.Chunk, terminal io.EOF, or structured error.
func (s *grpcStreamer) Recv() (model.Chunk, error) {
	s.mu.RLock()
	if s.finished {
		err := s.finalErr
		s.mu.RUnlock()
		return nil, err
	}
	s.mu.RUnlock()

	frame, err := s.stream.RecvWithContext(s.ctx)
	if err != nil {
		err = normalizeTransportError(s.ctx, err)
		// An unexpected EOF before a terminal response frame is a transport
		// truncation failure, never clean completion.
		//nolint:errorlint // Exact equality is required by the model stream contract.
		if err == io.EOF {
			err = fmt.Errorf("gateway: gRPC stream ended before terminal frame: %w", io.ErrUnexpectedEOF)
		}
		s.finish(nil, err)
		return nil, err
	}
	if frame == nil {
		nilErr := errors.New("gateway: gRPC stream emitted nil frame")
		s.finish(nil, nilErr)
		return nil, nilErr
	}
	if err := frame.Payload.Validate(); err != nil {
		valErr := fmt.Errorf("gateway: invalid stream frame payload: %w", err)
		s.finish(nil, valErr)
		return nil, valErr
	}

	switch frame.Payload.Kind() {
	case genmodelgateway.StreamFramePayloadKindChunk:
		genChunk, _ := frame.Payload.AsChunk()
		chunk, err := fromGenChunk(genChunk)
		if err != nil {
			s.finish(nil, err)
			return nil, err
		}
		return chunk, nil
	case genmodelgateway.StreamFramePayloadKindError:
		genErr, _ := frame.Payload.AsError()
		streamErr := fromGenError(genErr)
		if streamErr == nil {
			streamErr = errors.New("gateway: stream failed without error details")
		}
		s.finish(nil, streamErr)
		return nil, streamErr
	case genmodelgateway.StreamFramePayloadKindResponse:
		genResp, _ := frame.Payload.AsResponse()
		resp, err := fromGenResponse(genResp)
		if err != nil {
			s.finish(nil, err)
			return nil, err
		}
		// Require clean EOF immediately after the terminal response frame. Any
		// trailing frame is a protocol violation; any non-EOF read error is a
		// transport failure. Retain any observed terminal usage on the error
		// while keeping Response() nil.
		extraFrame, tailErr := s.stream.RecvWithContext(s.ctx)
		if tailErr == nil || extraFrame != nil {
			dupErr := errors.New("gateway: gRPC stream emitted unexpected frame after terminal response")
			if resp.HasObservedUsage() {
				if resp.UsageIncomplete {
					dupErr = model.RetainIncompleteUsage(dupErr, resp.Usage)
				} else {
					dupErr = model.RetainUsage(dupErr, resp.Usage)
				}
			}
			s.finish(nil, dupErr)
			return nil, dupErr
		}
		//nolint:errorlint // Exact io.EOF equality is required by the stream contract.
		if tailErr != io.EOF {
			normErr := normalizeTransportError(s.ctx, tailErr)
			if resp.HasObservedUsage() && model.UsageFromError(normErr) == nil {
				if resp.UsageIncomplete {
					normErr = model.RetainIncompleteUsage(normErr, resp.Usage)
				} else {
					normErr = model.RetainUsage(normErr, resp.Usage)
				}
			}
			s.finish(nil, normErr)
			return nil, normErr
		}
		s.finish(resp, io.EOF)
		return nil, io.EOF
	default:
		kindErr := fmt.Errorf("gateway: unknown stream frame kind %q", frame.Payload.Kind())
		s.finish(nil, kindErr)
		return nil, kindErr
	}
}

// Response returns the terminal provider response after Recv reports io.EOF.
func (s *grpcStreamer) Response() *model.Response {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.response
}

// Close cancels the stream context so any in-flight Recv or server handler
// stops promptly.
func (s *grpcStreamer) Close() error {
	s.cancel()
	return nil
}

// newGRPCProvider constructs the raw model.Provider (and model.TokenCounter)
// backed by the Goa-generated gRPC transport over conn.
func newGRPCProvider(conn *grpc.ClientConn, opts ...grpc.CallOption) (model.Provider, error) {
	if conn == nil {
		return nil, errors.New("gateway: gRPC client connection is required")
	}
	transport := gengrpcclient.NewClient(conn, opts...)
	client := genmodelgateway.NewClient(
		transport.Complete(),
		transport.Stream(),
		transport.CountTokens(),
	)
	return NewCountingRemoteProvider(
		func(ctx context.Context, req *model.Request) (*model.Response, error) {
			return invokeGRPCComplete(ctx, client, req)
		},
		func(ctx context.Context, req *model.Request) (model.Streamer, error) {
			return invokeGRPCStream(ctx, client, req)
		},
		func(ctx context.Context, req *model.Request) (model.TokenCount, error) {
			return invokeGRPCCountTokens(ctx, client, req)
		},
	)
}

// invokeGRPCComplete executes one unary Complete call over the Goa gRPC client.
func invokeGRPCComplete(
	ctx context.Context,
	client *genmodelgateway.Client,
	req *model.Request,
) (*model.Response, error) {
	payload, err := toGenRequest(req)
	if err != nil {
		return nil, err
	}
	res, err := client.Complete(ctx, payload)
	if err != nil {
		return nil, normalizeTransportError(ctx, err)
	}
	if res == nil {
		return nil, errors.New("gateway: unary response envelope is nil")
	}
	if err := res.Outcome.Validate(); err != nil {
		return nil, fmt.Errorf("gateway: invalid unary outcome: %w", err)
	}
	switch res.Outcome.Kind() {
	case genmodelgateway.CompleteOutcomeKindError:
		genErr, _ := res.Outcome.AsError()
		if err := fromGenError(genErr); err != nil {
			return nil, err
		}
		return nil, errors.New("gateway: unary failed without error details")
	case genmodelgateway.CompleteOutcomeKindResponse:
		genResp, _ := res.Outcome.AsResponse()
		return fromGenResponse(genResp)
	default:
		return nil, fmt.Errorf("gateway: unknown unary outcome kind %q", res.Outcome.Kind())
	}
}

// invokeGRPCStream opens one server-streaming Stream call over the Goa gRPC client.
func invokeGRPCStream(
	ctx context.Context,
	client *genmodelgateway.Client,
	req *model.Request,
) (model.Streamer, error) {
	payload, err := toGenRequest(req)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	stream, err := client.Stream(cctx, payload)
	if err != nil {
		cancel()
		return nil, normalizeTransportError(ctx, err)
	}
	return &grpcStreamer{
		ctx:    cctx,
		cancel: cancel,
		stream: stream,
	}, nil
}

// invokeGRPCCountTokens executes one CountTokens call over the Goa gRPC client.
func invokeGRPCCountTokens(
	ctx context.Context,
	client *genmodelgateway.Client,
	req *model.Request,
) (model.TokenCount, error) {
	payload, err := toGenRequest(req)
	if err != nil {
		return model.TokenCount{}, err
	}
	res, err := client.CountTokens(ctx, payload)
	if err != nil {
		return model.TokenCount{}, normalizeTransportError(ctx, err)
	}
	if res == nil {
		return model.TokenCount{}, errors.New("gateway: token count envelope is nil")
	}
	if err := res.Outcome.Validate(); err != nil {
		return model.TokenCount{}, fmt.Errorf("gateway: invalid token count outcome: %w", err)
	}
	switch res.Outcome.Kind() {
	case genmodelgateway.CountTokensOutcomeKindError:
		genErr, _ := res.Outcome.AsError()
		if err := fromGenError(genErr); err != nil {
			return model.TokenCount{}, err
		}
		return model.TokenCount{}, errors.New("gateway: token count failed without error details")
	case genmodelgateway.CountTokensOutcomeKindCount:
		genCount, _ := res.Outcome.AsCount()
		return fromGenTokenCount(genCount)
	default:
		return model.TokenCount{}, fmt.Errorf("gateway: unknown token count outcome kind %q", res.Outcome.Kind())
	}
}

// normalizeTransportError preserves context cancellation and deadline errors
// when the gRPC transport reports Canceled or DeadlineExceeded.
func normalizeTransportError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	//nolint:exhaustive // Only Canceled and DeadlineExceeded map to standard Go context errors.
	switch status.Code(err) {
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return err
	}
}

// finish latches the terminal response and error on grpcStreamer once.
func (s *grpcStreamer) finish(resp *model.Response, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	s.response = resp
	s.finalErr = err
}
