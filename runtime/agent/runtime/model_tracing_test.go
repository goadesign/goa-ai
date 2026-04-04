package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/telemetry"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

type (
	recordingTelemetryTracer struct {
		spans []*recordingTelemetrySpan
	}

	recordingTelemetrySpan struct {
		statusCode codes.Code
		statusDesc string
		errs       []error
		ended      bool
	}

	stubModelClient struct {
		complete func(context.Context, *model.Request) (*model.Response, error)
		stream   func(context.Context, *model.Request) (model.Streamer, error)
	}

	stubStreamer struct {
		recvErr  error
		closeErr error
	}
)

func TestTracedClientStreamIgnoresCanceledStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, context.Canceled
		},
	}, tracer, telemetry.NewNoopLogger(), "bedrock")

	stream, err := client.Stream(ctx, &model.Request{Stream: true})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, stream)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, tracer.spans[0].errs)
	assert.Equal(t, codes.Unset, tracer.spans[0].statusCode)
	assert.True(t, tracer.spans[0].ended)
}

func TestTracedClientCompleteIgnoresContextTermination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return nil, grpcStatus.Error(grpcCodes.Canceled, "context canceled")
		},
	}, tracer, telemetry.NewNoopLogger(), "bedrock")

	resp, err := client.Complete(ctx, &model.Request{})
	require.Equal(t, grpcCodes.Canceled, grpcStatus.Code(err))
	assert.Nil(t, resp)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, tracer.spans[0].errs)
	assert.Equal(t, codes.Unset, tracer.spans[0].statusCode)
	assert.True(t, tracer.spans[0].ended)
}

func TestTracedStreamRecvIgnoresContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:   ctx,
		inner: stubStreamer{recvErr: context.Canceled},
		span:  span,
	}

	_, err := stream.Recv()
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, span.errs)
	assert.Equal(t, codes.Unset, span.statusCode)
	assert.True(t, span.ended)
}

func TestTracedStreamRecvRecordsNonCancellationError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:   context.Background(),
		inner: stubStreamer{recvErr: wantErr},
		span:  span,
	}

	_, err := stream.Recv()
	require.ErrorIs(t, err, wantErr)
	require.Len(t, span.errs, 1)
	require.ErrorIs(t, span.errs[0], wantErr)
	assert.Equal(t, codes.Error, span.statusCode)
	assert.Equal(t, "stream recv failed", span.statusDesc)
	assert.True(t, span.ended)
}

func (t *recordingTelemetryTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	span := &recordingTelemetrySpan{}
	t.spans = append(t.spans, span)
	return ctx, span
}

func (t *recordingTelemetryTracer) Span(context.Context) telemetry.Span {
	if len(t.spans) == 0 {
		return &recordingTelemetrySpan{}
	}
	return t.spans[len(t.spans)-1]
}

func (s *recordingTelemetrySpan) End(...trace.SpanEndOption) {
	s.ended = true
}

func (s *recordingTelemetrySpan) AddEvent(string, ...any) {}

func (s *recordingTelemetrySpan) SetStatus(code codes.Code, description string) {
	s.statusCode = code
	s.statusDesc = description
}

func (s *recordingTelemetrySpan) RecordError(err error, _ ...trace.EventOption) {
	if err != nil {
		s.errs = append(s.errs, err)
	}
}

func (c stubModelClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if c.complete == nil {
		return nil, errors.New("unexpected Complete call")
	}
	return c.complete(ctx, req)
}

func (c stubModelClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	return c.stream(ctx, req)
}

func (s stubStreamer) Recv() (model.Chunk, error) {
	return model.Chunk{}, s.recvErr
}

func (s stubStreamer) Close() error {
	return s.closeErr
}

func (s stubStreamer) Metadata() map[string]any {
	return nil
}
