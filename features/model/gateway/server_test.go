package gateway

import (
	"context"
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubStreamer struct{ meta map[string]any }

func (s *stubStreamer) Recv() (model.Chunk, error) { return model.Chunk{}, errors.New("eof") }
func (s *stubStreamer) Close() error               { return nil }
func (s *stubStreamer) Metadata() map[string]any   { return s.meta }

type stubProvider struct{}

func (stubProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	return &model.Response{Content: []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
}
func (stubProvider) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
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
		return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) error {
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
	if err := srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error { return errors.New("eof") }); err == nil {
		t.Fatal("expected error from stream")
	}

	if !calledUnary {
		t.Fatal("unary middleware not invoked")
	}
	if !calledStream {
		t.Fatal("stream middleware not invoked")
	}
}
