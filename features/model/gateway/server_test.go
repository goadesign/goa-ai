package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubStreamer struct {
	meta                 map[string]any
	response             *model.Response
	chunks               []model.Chunk
	recvErr              error
	closeErr             error
	index                int
	closed               bool
	responseClearedClose bool
}

func (s *stubStreamer) Recv() (model.Chunk, error) {
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	return nil, errors.New("eof")
}
func (s *stubStreamer) Close() error {
	s.closed = true
	return s.closeErr
}
func (s *stubStreamer) Response() *model.Response {
	if s.responseClearedClose && s.closed {
		return nil
	}
	return s.response
}
func (s *stubStreamer) Metadata() map[string]any { return s.meta }

type stubProvider struct {
	response *model.Response
	streamer model.Streamer
}

func (p stubProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	if p.response != nil {
		return p.response, nil
	}
	return &model.Response{
		Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
		StopReason: "done",
	}, nil
}
func (p stubProvider) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	if p.streamer != nil {
		return p.streamer, nil
	}
	return &stubStreamer{}, nil
}

func TestNewServer_BuildsChains(t *testing.T) {
	prov := stubProvider{}
	calledUnary := false
	calledStream := false

	u := func(next UnaryHandler) UnaryHandler {
		return func(ctx context.Context, req *model.Request) (*model.Response, error) {
			calledUnary = true
			return next(ctx, req)
		}
	}
	s := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			calledStream = true
			return next(ctx, req, send)
		}
	}

	srv, err := NewServer(WithProvider(prov), WithUnary(u), WithStream(s))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	if _, err := srv.Complete(context.Background(), &model.Request{Model: "m"}); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if _, err := srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error { return errors.New("eof") }); err == nil {
		t.Fatal("expected error from stream")
	}

	if !calledUnary {
		t.Fatal("unary middleware not invoked")
	}
	if !calledStream {
		t.Fatal("stream middleware not invoked")
	}
}

func TestServerStreamTreatsEOFAsSuccessAndPropagatesCloseFailure(t *testing.T) {
	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "clean close"},
		{name: "close failure", closeErr: errors.New("close failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, err := NewServer(WithProvider(stubProvider{streamer: &stubStreamer{
				recvErr:              io.EOF,
				closeErr:             test.closeErr,
				responseClearedClose: true,
				chunks:               []model.Chunk{model.StopChunk{Reason: "done"}},
				response: &model.Response{
					Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
					StopReason: "done",
				},
			}}))
			if err != nil {
				t.Fatalf("NewServer error: %v", err)
			}

			var sent int
			_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error {
				sent++
				return nil
			})

			if !errors.Is(err, test.closeErr) {
				t.Fatalf("stream error = %v, want %v", err, test.closeErr)
			}
			if sent != 1 {
				t.Fatalf("sent chunks = %d, want 1", sent)
			}
		})
	}
}

func TestServerCompleteRejectsProviderResponseBeforeMiddleware(t *testing.T) {
	repairsResponse := func(next UnaryHandler) UnaryHandler {
		return func(ctx context.Context, req *model.Request) (*model.Response, error) {
			_, err := next(ctx, req)
			if err != nil {
				return nil, err
			}
			return &model.Response{
				Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "repaired"}}}},
				StopReason: "done",
			}, nil
		}
	}
	srv, err := NewServer(
		WithProvider(stubProvider{response: &model.Response{}}),
		WithUnary(repairsResponse),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	_, err = srv.Complete(context.Background(), &model.Request{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "provider returned invalid canonical response") {
		t.Fatalf("complete error = %v, want provider validation error", err)
	}
}

func TestServerStreamRejectsProviderChunkBeforeMiddleware(t *testing.T) {
	dropsChunks := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, _ func(model.Chunk) error) (*model.Response, error) {
			return next(ctx, req, func(model.Chunk) error { return nil })
		}
	}
	srv, err := NewServer(
		WithProvider(stubProvider{streamer: &stubStreamer{
			chunks:  []model.Chunk{model.ToolCallChunk{ToolCall: model.ToolCall{Name: "svc.lookup"}}},
			recvErr: io.EOF,
			response: &model.Response{
				Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
				StopReason: "done",
			},
		}}),
		WithStream(dropsChunks),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "provider returned invalid stream chunk") {
		t.Fatalf("stream error = %v, want provider chunk validation error", err)
	}
}

func TestServerStreamRejectsChunkErrorIgnoredByMiddleware(t *testing.T) {
	ignoresSendError := func(StreamHandler) StreamHandler {
		return func(_ context.Context, _ *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			if err := send(model.ToolCallChunk{ToolCall: model.ToolCall{Name: "svc.lookup"}}); err == nil {
				return nil, errors.New("expected invalid chunk error")
			}
			return &model.Response{
				Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
				StopReason: "done",
			}, nil
		}
	}
	srv, err := NewServer(WithProvider(stubProvider{}), WithStream(ignoresSendError))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "gateway: invalid stream chunk") {
		t.Fatalf("stream error = %v, want invalid chunk", err)
	}
}
